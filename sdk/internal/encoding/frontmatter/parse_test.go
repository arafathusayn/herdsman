package frontmatter

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name, src string
		format    Format
		key, want string
		raw, body string
	}{
		{"yaml", "---\nid: x\n---\nbody\n", YAML, "id", "x", "id: x\n", "body\n"},
		{"toml", "+++\nid = \"x\"\n+++\nbody\n", TOML, "id", "x", "id = \"x\"\n", "body\n"},
		{"crlf", "---\r\nid: x\r\n---\r\nbody", YAML, "id", "x", "id: x\r\n", "body"},
		{"bom", "\xef\xbb\xbf---\nid: x\n---\n", YAML, "id", "x", "id: x\n", ""},
		{"fence with trailing spaces", "--- \t\nid: x\n---  \nbody", YAML, "id", "x", "id: x\n", "body"},
		{"closing fence at end of file", "---\nid: x\n---", YAML, "id", "x", "id: x\n", ""},
		{"body keeps fences", "---\nid: x\n---\n---\nmore\n", YAML, "id", "x", "id: x\n", "---\nmore\n"},
		{"empty block", "---\n---\nbody", YAML, "", "", "", "body"},
		{"comment-only block", "---\n# nothing\n---\nbody", YAML, "", "", "# nothing\n", "body"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := Parse([]byte(c.src))
			if err != nil {
				t.Fatal(err)
			}
			if doc.Format != c.format || doc.Raw != c.raw || string(doc.Body) != c.body {
				t.Fatalf("got format %q raw %q body %q", doc.Format, doc.Raw, doc.Body)
			}
			if c.key != "" {
				if v, ok := doc.Meta.Get(c.key); !ok || v.Str != c.want {
					t.Fatalf("Meta[%s] = %+v, %v", c.key, v, ok)
				}
			} else if doc.Meta.Len() != 0 {
				t.Fatalf("Meta has %d keys", doc.Meta.Len())
			}
		})
	}
}

func TestParseWithoutFrontMatter(t *testing.T) {
	for _, src := range []string{"", "# Title\n", "--- not a fence\n", "----\nx\n----\n", "text\n---\nid: x\n---\n", "---"} {
		doc, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
		if doc.Format != None || doc.Meta.Len() != 0 || string(doc.Body) != src {
			t.Fatalf("Parse(%q) = %+v", src, doc)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for name, c := range map[string]struct{ src, msg string }{
		"no closing fence":       {"---\nid: x\n", "closing"},
		"bad yaml":               {"---\na: b: c\n---\n", "yaml:"},
		"bad toml":               {"+++\na =\n+++\n", "toml:"},
		"yaml scalar":            {"---\njust text\n---\n", "mapping"},
		"yaml sequence":          {"---\n- a\n---\n", "mapping"},
		"toml fence, yaml fence": {"+++\na = 1\n---\n", "closing"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.src))
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("Parse error = %v, want it to mention %q", err, c.msg)
			}
			if !strings.HasPrefix(err.Error(), "frontmatter: ") {
				t.Fatalf("error %q lacks the package prefix", err)
			}
		})
	}
}
