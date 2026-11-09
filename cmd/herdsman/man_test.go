package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const manDir = "../../docs/man"

// roff escapes text for a man page: a backslash is a roff escape, and a line
// starting with a dot or an apostrophe is a roff request.
func roff(text string) string {
	var b strings.Builder
	for line := range strings.Lines(text) {
		line = strings.ReplaceAll(line, `\`, `\e`)
		if strings.HasPrefix(line, ".") || strings.HasPrefix(line, "'") {
			b.WriteString(`\&`)
		}
		b.WriteString(line)
	}
	return b.String()
}

func manPage(title, summary, body string, seeAlso []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, ".TH %s 1 \"\" \"herdsman\" \"herdsman manual\"\n", strings.ToUpper(title))
	fmt.Fprintf(&b, ".SH NAME\n%s \\- %s\n", title, roff(summary))
	b.WriteString(".SH DESCRIPTION\n.nf\n")
	b.WriteString(roff(strings.TrimRight(body, "\n")))
	b.WriteString("\n.fi\n")
	if len(seeAlso) > 0 {
		b.WriteString(".SH SEE ALSO\n")
		for i, s := range seeAlso {
			sep := ",\n"
			if i == len(seeAlso)-1 {
				sep = "\n"
			}
			fmt.Fprintf(&b, ".BR %s (1)%s", s, sep)
		}
	}
	return b.String()
}

// specEntry is the part of a `spec cli` command the pages use.
type specEntry struct {
	Name        string      `json:"name"`
	Summary     string      `json:"summary"`
	Help        string      `json:"help"`
	Subcommands []specEntry `json:"subcommands"`
}

type cliSpecDoc struct {
	Commands []specEntry `json:"commands"`
}

// manPages renders docs/man from `spec cli`, the one list that holds both the
// registered commands and those main.go dispatches itself.
func manPages() map[string]string {
	var spec bytes.Buffer
	if err := writeSpecCLI(&spec); err != nil {
		panic(err)
	}
	var doc cliSpecDoc
	if err := json.Unmarshal(spec.Bytes(), &doc); err != nil {
		panic(err)
	}
	return manPagesFrom(doc)
}

// manPagesFrom makes one page for the program and one for each command, so the
// pages cannot say other than --help does.
func manPagesFrom(doc cliSpecDoc) map[string]string {
	var usageText bytes.Buffer
	printUsage(&usageText)
	refs := make([]string, len(doc.Commands))
	for i, c := range doc.Commands {
		refs[i] = "herdsman-" + c.Name
	}
	sort.Strings(refs)
	pages := map[string]string{
		"herdsman.1": manPage("herdsman", "run a team of coding agents inside herdr", usageText.String(), refs),
	}
	for _, c := range doc.Commands {
		body := c.Help
		if c.Name == "web" {
			// web parses its own flags, so the spec holds no help text for it.
			body = usage
		}
		if body == "" {
			body = c.Summary + "\n"
		}
		for _, s := range c.Subcommands {
			body += "\n" + c.Name + " " + s.Name + ":\n" + s.Help
		}
		pages["herdsman-"+c.Name+".1"] = manPage("herdsman-"+c.Name, c.Summary, body, []string{"herdsman"})
	}
	return pages
}

func TestRoffEscapes(t *testing.T) {
	if got := roff(".dot\n'tick\nback\\slash\nplain\n"); got != "\\&.dot\n\\&'tick\nback\\eslash\nplain\n" {
		t.Fatalf("got %q", got)
	}
}

// Run with HERDSMAN_WRITE_MAN=1 to regenerate docs/man after changing a command.
func TestManPagesAreCurrent(t *testing.T) {
	pages := manPages()
	if os.Getenv("HERDSMAN_WRITE_MAN") == "1" {
		if err := os.MkdirAll(manDir, 0o755); err != nil {
			t.Fatal(err)
		}
		old, _ := filepath.Glob(filepath.Join(manDir, "*.1"))
		for _, f := range old {
			os.Remove(f)
		}
		for name, body := range pages {
			if err := os.WriteFile(filepath.Join(manDir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	const fix = "; regenerate with HERDSMAN_WRITE_MAN=1 go test ./cmd/herdsman -run TestManPagesAreCurrent"
	for name, body := range pages {
		got, err := os.ReadFile(filepath.Join(manDir, name))
		if err != nil || string(got) != body {
			t.Errorf("docs/man/%s is out of date%s", name, fix)
		}
	}
	files, err := filepath.Glob(filepath.Join(manDir, "*.1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, ok := pages[filepath.Base(f)]; !ok {
			t.Errorf("docs/man/%s has no command%s", filepath.Base(f), fix)
		}
	}
}

// The commands main.go dispatches itself are read from its source, so a new
// one without a page fails here rather than matching a list kept beside it.
func TestManPagesCoverBuiltinCommands(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	_, after, _ := bytes.Cut(src, []byte("func run(args"))
	dispatch, _, _ := bytes.Cut(after, []byte("\n}\n"))
	found := 0
	for _, m := range regexp.MustCompile(`(?m)^\s*case "([a-z]+)"`).FindAllSubmatch(dispatch, -1) {
		found++
		if _, ok := manPages()["herdsman-"+string(m[1])+".1"]; !ok {
			t.Errorf("the built-in command %s has no page", m[1])
		}
	}
	if found < 3 {
		t.Fatalf("found %d built-ins: the search is wrong", found)
	}
}

func TestManPagesCoverTheProgramAndEveryCommand(t *testing.T) {
	pages := manPages()
	if len(pages) != len(commands)+len(builtinCommands)+1 {
		t.Fatalf("%d pages for %d commands", len(pages), len(commands))
	}
	if !strings.Contains(pages["herdsman-update.1"], "exit codes") {
		t.Fatal("the update page lost its help text")
	}
	if !strings.Contains(pages["herdsman.1"], ".BR herdsman-update (1)") {
		t.Fatal("the main page does not point at the command pages")
	}
}
