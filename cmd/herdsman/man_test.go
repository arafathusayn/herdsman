package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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

// manPages renders docs/man from the command table: one page for the program
// and one for each command, so the pages cannot say other than --help does.
func manPages() map[string]string {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	var usageText bytes.Buffer
	printUsage(&usageText)
	refs := make([]string, len(names))
	for i, n := range names {
		refs[i] = "herdsman-" + n
	}
	pages := map[string]string{
		"herdsman.1": manPage("herdsman", "run a team of coding agents inside herdr", usageText.String(), refs),
	}
	for _, n := range names {
		c := commands[n]
		body := c.help
		if body == "" {
			body = c.summary + "\n"
		}
		subs := make([]string, 0, len(c.subHelp))
		for s := range c.subHelp {
			subs = append(subs, s)
		}
		sort.Strings(subs)
		for _, s := range subs {
			body += "\n" + n + " " + s + ":\n" + c.subHelp[s]
		}
		pages["herdsman-"+n+".1"] = manPage("herdsman-"+n, c.summary, body, []string{"herdsman"})
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

func TestManPagesCoverTheProgramAndEveryCommand(t *testing.T) {
	pages := manPages()
	if len(pages) != len(commands)+1 {
		t.Fatalf("%d pages for %d commands", len(pages), len(commands))
	}
	if !strings.Contains(pages["herdsman-update.1"], "exit codes") {
		t.Fatal("the update page lost its help text")
	}
	if !strings.Contains(pages["herdsman.1"], ".BR herdsman-update (1)") {
		t.Fatal("the main page does not point at the command pages")
	}
}
