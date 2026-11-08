package docgen

import (
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	configMessage = "herdsman.config.v1.Config"
	hookMessage   = "herdsman.config.v1.HookSpec"
)

var (
	protoPackage = regexp.MustCompile(`^package\s+([\w.]+)\s*;`)
	protoBlock   = regexp.MustCompile(`^(message|enum)\s+(\w+)\s*\{`)
	protoField   = regexp.MustCompile(`^(?:optional\s+|repeated\s+)?(?:map<[^>]+>|[\w.]+)\s+(\w+)\s*=\s*\d+`)
	upperWord    = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

// protoComments maps the full name of each message, enum and field in a
// contract source to the comment directly above it. Compiled descriptors drop
// comments, so `spec proto` is where the descriptions come from.
func protoComments(source string) map[string]string {
	comments := map[string]string{}
	pkg := ""
	var stack, pending []string
	for _, raw := range strings.Split(source, "\n") {
		line := strings.TrimSpace(raw)
		if text, ok := strings.CutPrefix(line, "//"); ok {
			pending = append(pending, strings.TrimSpace(text))
			continue
		}
		if match := protoPackage.FindStringSubmatch(line); match != nil {
			pkg, stack = match[1], nil
		}
		record := func(name string) {
			if text := strings.Join(pending, " "); text != "" {
				comments[qualified(pkg, stack, name)] = text
			}
		}
		block := protoBlock.FindStringSubmatch(line)
		switch {
		case block != nil:
			record(block[2])
		case strings.Contains(line, "{"):
		default:
			if field := protoField.FindStringSubmatch(line); field != nil {
				record(field[1])
			}
		}
		pending = nil
		if block != nil {
			stack = append(stack, block[2])
		} else if strings.Contains(line, "{") {
			stack = append(stack, "")
		}
		for range strings.Count(line, "}") {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return comments
}

// qualified joins a name onto its enclosing scopes; a oneof is an empty scope.
func qualified(pkg string, stack []string, name string) string {
	parts := []string{pkg}
	for _, scope := range stack {
		if scope != "" {
			parts = append(parts, scope)
		}
	}
	return strings.Join(append(parts, name), ".")
}

// ConfigPage renders every key of the configuration from the contract's
// descriptor set and, for the descriptions, its source.
func ConfigPage(descriptorSet []byte, protoSource string) (string, error) {
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(descriptorSet, set); err != nil {
		return "", fmt.Errorf("read the descriptor set: %w", err)
	}
	files, err := protodesc.NewFiles(set)
	if err != nil {
		return "", fmt.Errorf("read the descriptor set: %w", err)
	}
	found, err := files.FindDescriptorByName(configMessage)
	if err != nil {
		return "", fmt.Errorf("the descriptor set has no %s: %w", configMessage, err)
	}
	root, ok := found.(protoreflect.MessageDescriptor)
	if !ok {
		return "", fmt.Errorf("%s is not a message", configMessage)
	}
	r := configRenderer{comments: protoComments(protoSource)}
	r.page.WriteString("# Configuration\n\n" + generatedNote + "\n\n")
	r.page.WriteString("Every key of the configuration file, generated from `herdsman spec descriptor` and `herdsman spec proto`. ")
	r.page.WriteString("Keys may be written with underscores or hyphens.\n")
	fields := root.Fields()
	for i := range fields.Len() {
		r.section(fields.Get(i), string(fields.Get(i).Name()), 2, []protoreflect.FullName{root.FullName()})
	}
	return r.page.String(), nil
}

type configRenderer struct {
	comments map[string]string
	page     strings.Builder
}

// section writes the heading of a field that is its own table in the file,
// then the message behind it. Depth is the Markdown heading level.
func (r *configRenderer) section(fd protoreflect.FieldDescriptor, path string, depth int, parents []protoreflect.FullName) {
	heading := path
	if fd.IsMap() && fd.MapValue().Kind() == protoreflect.MessageKind {
		heading += ".<key>"
	}
	// A repeated message is an array of tables in the file; the loader refuses
	// the single-table form, and people copy these headings.
	format := "\n%s [%s]\n"
	if fd.IsList() {
		format = "\n%s [[%s]]\n"
	}
	fmt.Fprintf(&r.page, format, strings.Repeat("#", min(depth, 6)), heading)
	message := sectionMessage(fd)
	if text := r.describe(fd); text != "" {
		fmt.Fprintf(&r.page, "\n%s\n", text)
	}
	if fd.IsList() {
		r.page.WriteString("\nWrite this array table once per entry, in order.\n")
	}
	if message == nil {
		fmt.Fprintf(&r.page, "\nType: %s.\n", fieldType(fd))
		return
	}
	for _, parent := range parents {
		if parent == message.FullName() {
			fmt.Fprintf(&r.page, "\nEach value has the shape of `%s`, one level deep.\n", message.Name())
			return
		}
	}
	r.message(message, heading, depth, append(parents, message.FullName()))
}

// message writes a table of the plain keys and then a section for each key
// that holds a table of its own.
func (r *configRenderer) message(md protoreflect.MessageDescriptor, path string, depth int, parents []protoreflect.FullName) {
	fields := md.Fields()
	wroteHeader := false
	for i := range fields.Len() {
		fd := fields.Get(i)
		if sectionMessage(fd) != nil {
			continue
		}
		if !wroteHeader {
			r.page.WriteString("\n| Key | Type | Description |\n| --- | --- | --- |\n")
			wroteHeader = true
		}
		fmt.Fprintf(&r.page, "| `%s` | %s | %s |\n", fd.Name(), fieldType(fd), tableCell(r.describe(fd)))
	}
	for i := range fields.Len() {
		fd := fields.Get(i)
		if sectionMessage(fd) != nil {
			r.section(fd, path+"."+string(fd.Name()), depth+1, parents)
		}
	}
}

// sectionMessage is the message a field opens as a table; a hook is written
// inline, and every other kind of field is a plain key.
func sectionMessage(fd protoreflect.FieldDescriptor) protoreflect.MessageDescriptor {
	if fd.IsMap() {
		if fd.MapValue().Kind() == protoreflect.MessageKind {
			return fd.MapValue().Message()
		}
		return nil
	}
	if fd.Kind() != protoreflect.MessageKind || string(fd.Message().FullName()) == hookMessage {
		return nil
	}
	return fd.Message()
}

func (r *configRenderer) describe(fd protoreflect.FieldDescriptor) string {
	if text := r.comments[string(fd.FullName())]; text != "" {
		return text
	}
	switch {
	case fd.IsMap() && fd.MapValue().Kind() == protoreflect.MessageKind:
		return r.comments[string(fd.MapValue().Message().FullName())]
	case fd.IsMap():
		return ""
	case fd.Kind() == protoreflect.MessageKind:
		return r.comments[string(fd.Message().FullName())]
	case fd.Kind() == protoreflect.EnumKind:
		return r.comments[string(fd.Enum().FullName())]
	}
	return ""
}

func tableCell(text string) string {
	return strings.ReplaceAll(text, "|", `\|`)
}

func fieldType(fd protoreflect.FieldDescriptor) string {
	switch {
	case fd.IsMap():
		return "table of " + kindName(fd.MapValue())
	case fd.IsList():
		return "array of " + kindName(fd)
	default:
		return kindName(fd)
	}
}

func kindName(fd protoreflect.FieldDescriptor) string {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return "boolean"
	case protoreflect.StringKind, protoreflect.BytesKind:
		return "string"
	case protoreflect.EnumKind:
		return enumChoices(fd.Enum())
	case protoreflect.MessageKind:
		// Every other message opens a section instead of a table row.
		return "string, table or array"
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return "number"
	default:
		return "integer"
	}
}

// enumChoices spells the values the way a file writes them, leaving out the
// zero value, which means unset.
func enumChoices(ed protoreflect.EnumDescriptor) string {
	prefix := strings.ToUpper(upperWord.ReplaceAllString(string(ed.Name()), "${1}_${2}")) + "_"
	values := ed.Values()
	var choices []string
	for i := range values.Len() {
		if value := values.Get(i); value.Number() != 0 {
			text := strings.ToLower(strings.TrimPrefix(string(value.Name()), prefix))
			choices = append(choices, "`"+strings.ReplaceAll(text, "_", "-")+"`")
		}
	}
	return strings.Join(choices, ` \| `)
}
