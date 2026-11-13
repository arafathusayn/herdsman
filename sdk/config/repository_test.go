package config

import "testing"

func TestRepositoryKeyNamesHostAndPath(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:acme/app.git":             "github.com/acme/app",
		"git@github.com:acme/app":                 "github.com/acme/app",
		"https://github.com/acme/app.git":         "github.com/acme/app",
		"https://user:token@github.com/acme/app/": "github.com/acme/app",
		"ssh://git@GitHub.com:22/acme/app.git":    "github.com/acme/app",
		"git://host.example/group/sub/app":        "host.example/group/sub/app",
		"http://[::1]:3000/acme/app.git":          "::1/acme/app",
		"/srv/git/app.git":                        "",
		"./relative/app":                          "",
		"file:///srv/git/app.git":                 "",
		"":                                        "",
		"nohost":                                  "",
		"https://github.com":                      "",
		"git@github.com:":                         "",
	} {
		if got := RepositoryKey(remote); got != want {
			t.Errorf("RepositoryKey(%q) = %q, want %q", remote, got, want)
		}
	}
}
