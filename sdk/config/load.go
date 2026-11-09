// Package config loads the herdsman configuration: it finds the files, decodes
// any of the four formats into one generic tree, layers them, and binds the
// result to the generated message through its descriptor.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/toml"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
	"github.com/arafathusayn/herdsman/sdk/internal/encoding/yaml"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const envConfigPath = "HERDSMAN_CONFIG"

// projectFormats are the extensions a discovered project file can have.
var projectFormats = []string{"toml", "json", "yaml", "yml", "bin"}

// userFormats are the extensions of the user file, which is always text.
var userFormats = []string{"toml", "json", "yaml"}

// Options says where to look. The zero value reads the real machine.
type Options struct {
	// ConfigPath is a file named by the caller, the strongest choice.
	ConfigPath string
	// RepoRoot enables discovery next to and inside a repository.
	RepoRoot string
	// UserDir holds the user file; empty means the standard folder.
	UserDir string
	// ProjectKey selects the user file's overrides for one repository.
	ProjectKey string
	// Flags are command-line values by dotted key.
	Flags map[string]string
	// Environ replaces the process environment when not nil.
	Environ []string
	// Strict turns every warning into an error.
	Strict bool
}

// Result is a loaded configuration.
type Result struct {
	Config *configv1.Config
	// Sources maps each dotted key to where its value came from.
	Sources map[string]string
	// Warnings lists unknown keys, in the order they were found.
	Warnings []Warning
	// ProjectPath is the project file used, or empty.
	ProjectPath string
	// UserPath is the user file used, or empty.
	UserPath string

	tree *tree.Node
}

// Get returns the effective value of a dotted key as text.
func (r *Result) Get(key string) (string, error) {
	resolved, err := resolveKey(key, configDescriptor())
	if err != nil {
		return "", err
	}
	node, ok := r.tree.Lookup(resolved.segments...)
	if !ok {
		return "", fmt.Errorf("%s: not set", key)
	}
	return renderNode(node), nil
}

func configDescriptor() protoreflect.MessageDescriptor {
	return (&configv1.Config{}).ProtoReflect().Descriptor()
}

// Load layers defaults, the user file, the project file, the environment and
// flags, weakest first.
func Load(opts Options) (*Result, error) {
	environ := opts.Environ
	if environ == nil {
		environ = os.Environ()
	}
	md := configDescriptor()
	rep := &report{strict: opts.Strict}
	sources := map[string]string{}
	merged := tree.NewTable()
	merge(merged, messageTree(Defaults().ProtoReflect()), "", SourceDefault, sources)
	result := &Result{Sources: sources}

	projectPath, err := findProjectFile(opts, environ)
	if err != nil {
		return nil, err
	}
	if projectPath == "" {
		implied, _, err := detected(opts.RepoRoot)
		if err != nil {
			return nil, err
		}
		merge(merged, messageTree(implied.ProtoReflect()), "", SourceDefault, sources)
	}
	userPath, err := findUserFile(opts, environ)
	if err != nil {
		return nil, err
	}
	if userPath != "" {
		layer, err := readLayer(userPath, rep)
		if err != nil {
			return nil, err
		}
		result.UserPath = userPath
		applyUserLayer(merged, layer, opts.ProjectKey, sources)
	}
	if projectPath != "" {
		layer, err := readLayer(projectPath, rep)
		if err != nil {
			return nil, err
		}
		result.ProjectPath = projectPath
		merge(merged, layer, "", SourceProject, sources)
	}
	if err := applyEnvironment(merged, environ, md, rep, sources); err != nil {
		return nil, err
	}
	if err := applyFlags(merged, opts.Flags, md, sources); err != nil {
		return nil, err
	}
	if node, ok := merged.Lookup("schema", "version"); ok {
		if err := checkSchemaVersion(node); err != nil {
			return nil, err
		}
	}
	msg := &configv1.Config{}
	if err := bindMessage(merged, msg.ProtoReflect(), ""); err != nil {
		return nil, err
	}
	result.Config, result.tree, result.Warnings = msg, merged, rep.warnings
	return result, nil
}

// applyUserLayer lays down the general settings, then the chosen repository's
// overrides. The overrides table itself is not a setting of the result.
func applyUserLayer(merged, layer *tree.Node, projectKey string, sources map[string]string) {
	projects, _ := layer.Get("projects")
	general := clone(layer)
	general.Delete("projects")
	merge(merged, general, "", SourceUser, sources)
	if projects == nil || projectKey == "" {
		return
	}
	if override, ok := projects.Get(projectKey); ok {
		merge(merged, override, "", SourceUser, sources)
	}
}

// ReadFile reads one file on its own, with no defaults and no other layer.
func ReadFile(path string) (*Result, error) {
	rep := &report{}
	layer, err := readLayer(path, rep)
	if err != nil {
		return nil, err
	}
	msg := &configv1.Config{}
	if err := bindMessage(layer, msg.ProtoReflect(), ""); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	sources := map[string]string{}
	recordSources(layer, "", SourceProject, sources)
	return &Result{Config: msg, tree: layer, Sources: sources, Warnings: rep.warnings, ProjectPath: path}, nil
}

// readLayer decodes a file into a checked, canonical tree.
func readLayer(path string, rep *report) (*tree.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	format, err := formatOf(path)
	if err != nil {
		return nil, err
	}
	layer, err := decodeLayer(format, data, rep)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return layer, nil
}

func decodeLayer(format string, data []byte, rep *report) (*tree.Node, error) {
	var root *tree.Node
	var err error
	if format == FormatBin {
		root, err = decodeBin(data, rep)
	} else {
		root, err = decodeText(format, data)
	}
	if err != nil {
		return nil, err
	}
	layer, err := normalize(root, configDescriptor(), "", false, rep)
	if err != nil {
		return nil, err
	}
	if err := checkLayer(layer); err != nil {
		return nil, err
	}
	return layer, nil
}

func checkLayer(layer *tree.Node) error {
	if node, ok := layer.Lookup("schema", "version"); ok {
		if err := checkSchemaVersion(node); err != nil {
			return err
		}
	}
	projects, ok := layer.Get("projects")
	if !ok {
		return nil
	}
	for _, name := range projects.Keys() {
		override, _ := projects.Get(name)
		for _, forbidden := range []string{"projects", "aliases"} {
			if _, found := override.Get(forbidden); found {
				return fmt.Errorf("projects.%s: %s cannot be overridden per repository", name, forbidden)
			}
		}
	}
	return nil
}

// parseVersion reads a SemVer core; ok is false for anything else.
func parseVersion(text string) (numbers [3]int, ok bool) {
	parts := strings.Split(text, ".")
	if len(parts) != 3 {
		return numbers, false
	}
	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return numbers, false
		}
		numbers[i] = number
	}
	return numbers, true
}

func checkSchemaVersion(node *tree.Node) error {
	if node.Kind != tree.String {
		return fmt.Errorf("schema.version: expected a string, found %s", node.Kind)
	}
	numbers, ok := parseVersion(node.Str)
	if !ok {
		return fmt.Errorf("schema.version: %q is not SemVer", node.Str)
	}
	if known, _ := parseVersion(SchemaVersion); numbers[0] > known[0] {
		return fmt.Errorf("schema.version: the file needs %s, this binary knows %s; upgrade herdsman", node.Str, SchemaVersion)
	}
	return nil
}

func lookupEnv(environ []string, name string) string {
	for _, pair := range environ {
		if key, value, ok := strings.Cut(pair, "="); ok && key == name {
			return value
		}
	}
	return ""
}

// UserDir is the folder of the user file for an environment. It follows
// os.UserConfigDir, computed from environ so that a caller can supply one.
func UserDir(environ []string) string {
	home := lookupEnv(environ, "HOME")
	if runtime.GOOS == "darwin" {
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "herdsman")
	}
	if base := lookupEnv(environ, "XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "herdsman")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "herdsman")
}

func userDir(opts Options, environ []string) string {
	if opts.UserDir != "" {
		return opts.UserDir
	}
	return UserDir(environ)
}

func findUserFile(opts Options, environ []string) (string, error) {
	dir := userDir(opts, environ)
	if dir == "" {
		return "", nil
	}
	return findOne(dir, "config", userFormats)
}

// FileIn returns the project file directly in dir, or "" when there is none.
func FileIn(dir string) (string, error) { return findOne(dir, "herdsman.config", projectFormats) }

// UserFile returns the user file's path and whether it exists yet. A new file
// would be TOML.
func UserFile(opts Options) (string, bool, error) {
	environ := opts.Environ
	if environ == nil {
		environ = os.Environ()
	}
	dir := userDir(opts, environ)
	if dir == "" {
		return "", false, errors.New("no user configuration folder: HOME is unset")
	}
	found, err := findOne(dir, "config", userFormats)
	if err != nil || found != "" {
		return found, found != "", err
	}
	return filepath.Join(dir, "config.toml"), false, nil
}

// ProjectFile returns the project file discovery chooses, or "" when there
// is none.
func ProjectFile(opts Options) (string, error) {
	environ := opts.Environ
	if environ == nil {
		environ = os.Environ()
	}
	return findProjectFile(opts, environ)
}

func findProjectFile(opts Options, environ []string) (string, error) {
	named := opts.ConfigPath
	if named == "" {
		named = lookupEnv(environ, envConfigPath)
	}
	if named != "" {
		abs, err := filepath.Abs(named)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", err
		}
		return abs, nil
	}
	if opts.RepoRoot == "" {
		return "", nil
	}
	for _, dir := range []string{filepath.Dir(filepath.Clean(opts.RepoRoot)), opts.RepoRoot} {
		found, err := findOne(dir, "herdsman.config", projectFormats)
		if err != nil || found != "" {
			return found, err
		}
	}
	return "", nil
}

// findOne returns the file of that stem in dir. Two of them is an error,
// because neither could be said to win.
func findOne(dir, stem string, formats []string) (string, error) {
	var found []string
	for _, format := range formats {
		path := filepath.Join(dir, stem+"."+format)
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("two configuration files side by side, keep one: %s", strings.Join(found, " and "))
	}
}

// applyEnvironment reads HERDSMAN_<SECTION>__<KEY> variables. Other
// HERDSMAN_ variables have no double underscore and are not settings.
func applyEnvironment(merged *tree.Node, environ []string, md protoreflect.MessageDescriptor, rep *report, sources map[string]string) error {
	for _, pair := range environ {
		name, value, ok := strings.Cut(pair, "=")
		rest, prefixed := strings.CutPrefix(name, "HERDSMAN_")
		if !ok || !prefixed || !strings.Contains(rest, "__") {
			continue
		}
		parts := strings.Split(rest, "__")
		resolved, err := resolveParts(rest, parts, md, envFieldName)
		if err != nil {
			if rep.strict || inSafetySection(parts, md, envFieldName) {
				return fmt.Errorf("%s: %w", name, err)
			}
			rep.warnings = append(rep.warnings, Warning{Key: name})
			continue
		}
		if err := applyText(merged, resolved, value, SourceEnv, sources); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func applyFlags(merged *tree.Node, flags map[string]string, md protoreflect.MessageDescriptor, sources map[string]string) error {
	for key, value := range flags {
		resolved, err := resolveKey(key, md)
		if err != nil {
			return err
		}
		if err := applyText(merged, resolved, value, SourceFlag, sources); err != nil {
			return err
		}
	}
	return nil
}

func applyText(merged *tree.Node, resolved *resolved, value, source string, sources map[string]string) error {
	node, err := textNode(resolved, value)
	if err != nil {
		return err
	}
	layer := tree.NewTable()
	if err := setPath(layer, resolved.segments, node); err != nil {
		return err
	}
	merge(merged, layer, "", source, sources)
	return nil
}

// writeAtomic replaces a file so that a reader never sees half of it.
func writeAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".herdsman-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if info, err := os.Stat(path); err == nil {
		if err := temp.Chmod(info.Mode().Perm()); err != nil {
			temp.Close()
			return err
		}
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// writtenPath finds the spelling a key already has in a file, so that an edit
// changes it where it is instead of adding a second spelling. Only field names
// have other spellings: a map key or a hook's own name is matched exactly.
func writtenPath(root *tree.Node, segments []string, md protoreflect.MessageDescriptor) []string {
	out := make([]string, 0, len(segments))
	node := root
	literal := false
	for _, segment := range segments {
		chosen := segment
		if node != nil && !literal {
			for _, key := range node.Keys() {
				if canonicalKey(key) == segment {
					chosen = key
					break
				}
			}
		}
		if node != nil {
			node, _ = node.Get(chosen)
		}
		out = append(out, chosen)
		if literal {
			literal = false
			continue
		}
		fd := md.Fields().ByName(protoreflect.Name(segment))
		switch {
		case isMessageMap(fd):
			literal, md = true, fd.MapValue().Message()
		case fd.IsMap():
			literal = true
		case isSingularMessage(fd) && string(fd.Message().FullName()) == hookSpecName:
			literal = true
		case isSingularMessage(fd):
			md = fd.Message()
		}
	}
	return out
}

// checkEdit proves an edited file still loads before it is written.
func checkEdit(format string, data []byte) error {
	layer, err := decodeLayer(format, data, &report{})
	if err != nil {
		return err
	}
	return bindMessage(layer, (&configv1.Config{}).ProtoReflect(), "")
}

// encodeFile writes a whole tree in a file's format. For the binary form,
// original is the file being replaced, whose unknown fields are carried over.
func encodeFile(format string, root *tree.Node, original []byte) ([]byte, error) {
	if format != FormatBin {
		return encodeText(format, root)
	}
	msg := &configv1.Config{}
	layer, err := normalize(root, configDescriptor(), "", false, &report{})
	if err != nil {
		return nil, err
	}
	if err := refuseEmptyLists(layer, configDescriptor(), ""); err != nil {
		return nil, err
	}
	if err := bindMessage(layer, msg.ProtoReflect(), ""); err != nil {
		return nil, err
	}
	prior := &configv1.Config{}
	if err := proto.Unmarshal(original, prior); err != nil {
		return nil, err
	}
	carryUnknown(prior.ProtoReflect(), msg.ProtoReflect())
	return encodeBin(msg)
}

func decodeRaw(format string, data []byte) (*tree.Node, error) {
	if format == FormatBin {
		return decodeBin(data, &report{})
	}
	return decodeText(format, data)
}

func readForEdit(path string) (string, []byte, *tree.Node, error) {
	format, err := formatOf(path)
	if err != nil {
		return "", nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, nil, err
	}
	root, err := decodeRaw(format, data)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return format, data, root, nil
}

// SetFile writes one value into a file, keeping the comments of a format that
// has them. The file is not touched unless the result is valid.
func SetFile(path, key, value string) error {
	_, err := EditFile(path, key, value, false)
	return err
}

// Edit says what a change to a file did. Old is meaningful when HadOld is true.
type Edit struct {
	Path, Key, Old, New string
	HadOld              bool
}

// EditFile sets a key and reports the old and new value. With appendItem the
// value is added, as one item, to the end of a list held in the file.
func EditFile(path, key, value string, appendItem bool) (Edit, error) {
	resolved, err := resolveKey(key, configDescriptor())
	if err != nil {
		return Edit{}, err
	}
	var node *tree.Node
	if !appendItem {
		if node, err = textNode(resolved, value); err != nil {
			return Edit{}, err
		}
	} else if !resolved.field.IsList() || resolved.field.Kind() != protoreflect.StringKind || resolved.hookEntry || resolved.mapEntry {
		return Edit{}, fmt.Errorf("%s: not a list of strings, so nothing can be appended", key)
	}
	format, data, root, err := readForEdit(path)
	if err != nil {
		return Edit{}, err
	}
	written := writtenPath(root, resolved.segments, configDescriptor())
	edit := Edit{Path: path, Key: strings.Join(resolved.segments, ".")}
	current, had := root.Lookup(written...)
	if had {
		edit.HadOld, edit.Old = true, renderNode(current)
	}
	if appendItem {
		node = tree.NewArray()
		if had {
			if current.Kind != tree.Array {
				return Edit{}, fmt.Errorf("%s: not a list of strings, so nothing can be appended", key)
			}
			node.Items = append(node.Items, current.Items...)
		}
		node.Items = append(node.Items, tree.NewString(value))
	}
	edit.New = renderNode(node)
	if err := writeSet(path, format, data, root, written, node); err != nil {
		return Edit{}, err
	}
	return edit, nil
}

// writeSet stores node at written, in the file's own format.
func writeSet(path, format string, data []byte, root *tree.Node, written []string, node *tree.Node) error {
	var err error
	whole := func() ([]byte, error) {
		if err := setPath(root, written, node); err != nil {
			return nil, err
		}
		return encodeFile(format, root, data)
	}
	var out []byte
	switch format {
	case FormatTOML:
		doc, err := toml.Parse(data)
		if err != nil {
			return err
		}
		if current, ok := root.Lookup(written...); ok && current.Kind == tree.Table {
			if _, err := doc.UnsetTable(written); err != nil {
				return err
			}
		}
		if err := doc.Set(written, node); err != nil {
			return err
		}
		out = doc.Encode()
	case FormatYAML:
		if out, err = editYAML(data, func(doc *yaml.Document) error { return doc.Set(written, node) }, whole); err != nil {
			return err
		}
	default:
		if out, err = whole(); err != nil {
			return err
		}
	}
	if err := checkEdit(format, out); err != nil {
		return err
	}
	return writeAtomic(path, out)
}

// editYAML applies an edit that keeps the file's comments. A file with no
// block mapping at its top is rewritten whole, unless that would drop a
// comment.
func editYAML(data []byte, edit func(*yaml.Document) error, whole func() ([]byte, error)) ([]byte, error) {
	doc, err := yaml.Parse(data)
	if errors.Is(err, yaml.ErrNotEditable) {
		if bytes.Contains(data, []byte("#")) {
			return nil, errors.New("this YAML file's layout cannot be edited without losing a comment; write the top level as a block mapping")
		}
		return whole()
	}
	if err != nil {
		return nil, err
	}
	if err := edit(doc); err != nil {
		return nil, err
	}
	return doc.Encode(), nil
}

// UnsetFile removes one key from a file and reports whether it was there.
func UnsetFile(path, key string) (bool, error) {
	resolved, err := resolveKey(key, configDescriptor())
	if err != nil {
		return false, err
	}
	format, data, root, err := readForEdit(path)
	if err != nil {
		return false, err
	}
	written := writtenPath(root, resolved.segments, configDescriptor())
	if _, ok := root.Lookup(written...); !ok {
		return false, nil
	}
	whole := func() ([]byte, error) {
		parent, _ := root.Lookup(written[:len(written)-1]...)
		parent.Delete(written[len(written)-1])
		return encodeFile(format, root, data)
	}
	var out []byte
	switch format {
	case FormatTOML:
		doc, err := toml.Parse(data)
		if err != nil {
			return false, err
		}
		if current, _ := root.Lookup(written...); current.Kind == tree.Table {
			_, err = doc.UnsetTable(written)
		} else {
			_, err = doc.Unset(written)
		}
		if err != nil {
			return false, err
		}
		out = doc.Encode()
	case FormatYAML:
		if out, err = editYAML(data, func(doc *yaml.Document) error {
			_, err := doc.Unset(written)
			return err
		}, whole); err != nil {
			return false, err
		}
	default:
		if out, err = whole(); err != nil {
			return false, err
		}
	}
	if err := checkEdit(format, out); err != nil {
		return false, err
	}
	return true, writeAtomic(path, out)
}

// ExportFile converts a file to another format. Keys the message does not
// define are dropped with a warning when the file is read. A conversion the
// target format cannot hold exactly is refused rather than made lossy.
func ExportFile(path, format string) ([]byte, error) {
	result, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	source, err := formatOf(path)
	if err != nil {
		return nil, err
	}
	var original []byte
	if source == FormatBin {
		if original, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	switch format {
	case FormatBin:
		return encodeFile(FormatBin, result.tree, original)
	case FormatTOML, FormatYAML, FormatJSON:
		if source == FormatBin {
			if err := refuseUnknownInText(original); err != nil {
				return nil, err
			}
		}
		return encodeText(format, result.tree)
	default:
		return nil, fmt.Errorf("unknown format %q; use toml, yaml, json or bin", format)
	}
}

func encodeMessage(msg *configv1.Config, format string) ([]byte, error) {
	switch format {
	case FormatBin:
		return encodeBin(msg)
	case FormatTOML, FormatYAML, FormatJSON:
		return encodeText(format, messageTree(msg.ProtoReflect()))
	default:
		return nil, fmt.Errorf("unknown format %q; use toml, yaml, json or bin", format)
	}
}

// schemaOutdated reports a supported version below the one this build writes.
func schemaOutdated(root *tree.Node) bool {
	node, ok := root.Lookup("schema", "version")
	if !ok || node.Kind != tree.String {
		return false
	}
	found, valid := parseVersion(node.Str)
	known, _ := parseVersion(SchemaVersion)
	return valid && slices.Compare(found[:], known[:]) < 0
}

// UpdateFile rewrites a file to the current contract: canonical key spellings
// and the schema version this build writes. Comments survive in the formats
// that have them, and a key it does not recognize stays. Two spellings of one
// key are refused, since keeping either would lose the other.
func UpdateFile(path string) error {
	format, data, root, err := readForEdit(path)
	if err != nil {
		return err
	}
	if _, err := normalize(root, configDescriptor(), "", false, &report{}); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	pending := renames(root, configDescriptor(), nil, nil)
	schemaPath := []string{"schema", "version"}
	outdated := schemaOutdated(root)
	if len(pending) == 0 && !outdated {
		return nil
	}
	whole := func() ([]byte, error) {
		for _, item := range pending {
			parent, _ := root.Lookup(item.from[:len(item.from)-1]...)
			parent.Delete(item.from[len(item.from)-1])
			if err := setPath(root, item.to, item.value); err != nil {
				return nil, err
			}
		}
		if outdated {
			if err := setPath(root, schemaPath, tree.NewString(SchemaVersion)); err != nil {
				return nil, err
			}
		}
		return encodeFile(format, root, data)
	}
	var out []byte
	switch format {
	case FormatTOML:
		doc, err := toml.Parse(data)
		if err != nil {
			return err
		}
		for _, item := range pending {
			if item.value.Kind == tree.Table {
				moved, err := doc.RenameTable(item.from, item.to)
				if err != nil {
					return err
				}
				if moved {
					continue
				}
			}
			if _, err := doc.Unset(item.from); err != nil {
				return err
			}
			if err := doc.Set(item.to, item.value); err != nil {
				return err
			}
		}
		if outdated {
			if err := doc.Set(schemaPath, tree.NewString(SchemaVersion)); err != nil {
				return err
			}
		}
		out = doc.Encode()
	case FormatYAML:
		edit := func(doc *yaml.Document) error {
			for _, item := range pending {
				if err := doc.Rename(item.from, item.to[len(item.to)-1]); err != nil {
					return err
				}
			}
			if outdated {
				return doc.Set(schemaPath, tree.NewString(SchemaVersion))
			}
			return nil
		}
		if out, err = editYAML(data, edit, whole); err != nil {
			return err
		}
	default:
		if out, err = whole(); err != nil {
			return err
		}
	}
	if err := checkEdit(format, out); err != nil {
		return err
	}
	return writeAtomic(path, out)
}

// InitFile writes the defaults to a new file. It never replaces one.
func InitFile(path, format string) error {
	data, err := encodeMessage(Defaults(), format)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
