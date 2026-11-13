package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	configv1 "github.com/arafathusayn/herdsman/sdk/config/v1"
	"google.golang.org/protobuf/proto"
)

// Ecosystem is what a repository's root files imply for setting up a worktree.
type Ecosystem struct {
	Share          []string // Ignored folders worth sharing from the main tree.
	Install        []string // Commands that install dependencies, in order.
	GoCachePerTask bool     // Give each task its own Go build cache.
	Compose        bool     // The repository defines a compose project.
	EnvFiles       []string // .env files to mention; none is ever copied by default.
}

// DetectEcosystem reads the root of a repository once.
func DetectEcosystem(root string) (Ecosystem, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return Ecosystem{}, err
	}
	present := make(map[string]bool, len(entries))
	for _, entry := range entries {
		present[entry.Name()] = true
	}
	var found Ecosystem
	if install := nodeInstall(present); install != "" {
		found.Share = append(found.Share, "node_modules")
		found.Install = append(found.Install, install)
	}
	if present["Cargo.lock"] {
		found.Share = append(found.Share, "target")
	}
	if present["uv.lock"] {
		found.Install = append(found.Install, "uv sync")
	}
	if present["poetry.lock"] {
		found.Install = append(found.Install, "poetry install")
	}
	found.GoCachePerTask = present["go.sum"]
	found.Compose = present["compose.yaml"] || present["compose.yml"] ||
		present["docker-compose.yml"] || present["docker-compose.yaml"]
	if present[".env.example"] {
		for _, entry := range entries {
			if name := entry.Name(); name == ".env" || strings.HasPrefix(name, ".env.") {
				found.EnvFiles = append(found.EnvFiles, name)
			}
		}
	}
	return found, nil
}

// nodeInstall picks one install command when several lock files are present,
// since they would all write the same node_modules.
func nodeInstall(present map[string]bool) string {
	switch {
	case present["bun.lock"]:
		return "bun install --frozen-lockfile"
	case present["pnpm-lock.yaml"]:
		return "pnpm install --frozen-lockfile"
	case present["package-lock.json"]:
		return "npm ci"
	case present["yarn.lock"] && present[".yarnrc.yml"]:
		return "yarn install --immutable" // Yarn 2 and later dropped the older flag.
	case present["yarn.lock"]:
		return "yarn install --frozen-lockfile"
	}
	return ""
}

// detected is the settings an ecosystem implies. A repository that cannot be
// read implies none.
func detected(root string) (*configv1.Config, Ecosystem, error) {
	if root == "" {
		return &configv1.Config{}, Ecosystem{}, nil
	}
	found, err := DetectEcosystem(root)
	if errors.Is(err, fs.ErrNotExist) {
		return &configv1.Config{}, Ecosystem{}, nil
	}
	if err != nil {
		return nil, Ecosystem{}, fmt.Errorf("detect ecosystem: %w", err)
	}
	out := &configv1.Config{}
	if len(found.Share) > 0 || len(found.Install) > 0 {
		out.Setup = &configv1.Setup{Share: slices.Clone(found.Share)}
		if len(found.Install) > 0 {
			out.Setup.Install = proto.String(strings.Join(found.Install, " && "))
		}
	}
	return out, found, nil
}

// keyComments explains each key a new file can hold.
var keyComments = map[string]string{
	"schema.version":            "The version of the settings contract this file follows.",
	"worktree.path":             "Where a task's worktree is created.",
	"branch.name":               "How a task's branch is named.",
	"branch.types":              "The change types a branch name may start with.",
	"setup.env":                 "Ignored files copied into each worktree; none are copied unless listed.",
	"setup.install":             "The command that installs dependencies in a new worktree.",
	"setup.ports":               "How many ports each task reserves.",
	"setup.share":               "Ignored folders cloned copy-on-write from the main tree.",
	"teardown.containers":       "How a task's containers are found and stopped.",
	"teardown.grace":            "Seconds between asking a process to stop and forcing it.",
	"teardown.volumes":          "When a task's container volumes are removed.",
	"merge.strategy":            "How a task's commits land on the target branch.",
	"merge.land":                "Whether landing goes through a pull request or directly.",
	"merge.verify":              "Whether checks must pass before landing.",
	"merge.clean":               "Whether a landed task's worktree is removed.",
	"commit.stage":              "What a commit takes from the working tree.",
	"commit.convention":         "The message style commits follow.",
	"commit.generation.enabled": "Whether commit messages are generated.",
	"commit.generation.command": "\"auto\", or a program that reads a prompt and prints the message.",
	"commit.generation.summary": "Whether branch listings show a generated summary.",
}

// InitExplanations describes every key of a new file, for formats that cannot
// hold comments.
func InitExplanations() []string {
	out := make([]string, 0, len(keyComments))
	for key, text := range keyComments {
		out = append(out, key+": "+text)
	}
	slices.Sort(out)
	return out
}

// commentTOML puts each key's explanation on the line above it.
func commentTOML(data []byte) []byte {
	var out strings.Builder
	section := ""
	for line := range strings.SplitAfterSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			section = strings.Trim(trimmed, "[]")
		} else if key, _, ok := strings.Cut(trimmed, " = "); ok {
			if text := keyComments[joinKey(section, key)]; text != "" {
				out.WriteString("# " + text + "\n")
			}
		}
		out.WriteString(line)
	}
	return []byte(out.String())
}

// InitDetected writes the defaults plus what root's ecosystem implies to a new
// file, with an explanation of each key when the format has comments. It never
// replaces a file, and returns what it found so the caller can mention it.
func InitDetected(path, format, root string) (Ecosystem, error) {
	implied, found, err := detected(root)
	if err != nil {
		return Ecosystem{}, err
	}
	settings := Defaults()
	proto.Merge(settings, implied)
	data, err := encodeMessage(settings, format)
	if err != nil {
		return Ecosystem{}, err
	}
	if format == FormatTOML {
		data = commentTOML(data)
	}
	file, err := openNew(path)
	if err != nil {
		return Ecosystem{}, err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return Ecosystem{}, err
	}
	return found, file.Close()
}
