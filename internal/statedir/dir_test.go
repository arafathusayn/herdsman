package statedir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveOrder(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, option string
		vars         map[string]string
		root         string
		overridden   bool
	}{
		{"default", "", nil, filepath.Join(home, ".herdsman-private"), false},
		{"private home variable", "", map[string]string{envPrivateHome: "/p"}, "/p", false},
		{"option beats private home", "/o", map[string]string{envPrivateHome: "/p"}, "/o", false},
		{"state dir beats private home", "", map[string]string{Env: "/s", envPrivateHome: "/p"}, "/s", true},
		{"agreeing option", "/s/", map[string]string{Env: "/s"}, "/s", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := Resolve(c.option, env(c.vars))
			if err != nil || d.Root != c.root || d.Overridden != c.overridden {
				t.Fatalf("got %+v, %v", d, err)
			}
		})
	}
}

func TestResolveRefuses(t *testing.T) {
	if _, err := Resolve("/o", env(map[string]string{Env: "/s"})); err == nil || !strings.Contains(err.Error(), "different state directories") {
		t.Fatalf("conflict: %v", err)
	}
	if _, err := Resolve("", env(map[string]string{Env: "relative"})); err == nil {
		t.Fatal("a relative state directory was accepted")
	}
}

func TestResolveWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := Resolve("", env(nil)); err == nil {
		t.Fatal("no home and no override must be an error")
	}
}

func TestLockHome(t *testing.T) {
	if got := (Dir{Root: "/s", Overridden: true}).LockHome("/h"); got != "/s" {
		t.Fatal(got)
	}
	if got := (Dir{Root: "/s"}).LockHome("/h"); got != "/h" {
		t.Fatal(got)
	}
}
