package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/arafathusayn/herdsman/internal/doctor"
)

// A32: the form asks which gh account the route uses, with the probed one
// suggested.
func TestGitHubAccessEndpoint(t *testing.T) {
	f := newRouteFixture(t)
	path := "/api/routes/github?folder=" + url.QueryEscape(f.folder)
	if res := f.request(t, http.MethodGet, path, "", f.authed); res.StatusCode != http.StatusNotFound {
		t.Fatalf("no probe: %d", res.StatusCode)
	}
	var asked string
	f.srv.cfg.GitHub = func(_ context.Context, dir string) (doctor.GitHubAccess, error) {
		asked = dir
		if dir == f.home {
			return doctor.GitHubAccess{}, errors.New("no origin remote")
		}
		return doctor.GitHubAccess{Repo: "o/r", Accounts: []doctor.Account{{Name: "u", Active: true, CanRead: true}}, Suggested: "u"}, nil
	}
	res := f.request(t, http.MethodGet, path, "", f.authed)
	var g doctor.GitHubAccess
	if err := json.NewDecoder(res.Body).Decode(&g); err != nil || res.StatusCode != 200 || g.Suggested != "u" || asked != f.folder {
		t.Fatalf("%d %+v %v", res.StatusCode, g, err)
	}
	res = f.request(t, http.MethodGet, "/api/routes/github?folder="+url.QueryEscape(f.home), "", f.authed)
	var e struct {
		Accounts []doctor.Account `json:"accounts"`
		Error    string           `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&e); err != nil || res.StatusCode != 200 || e.Error != "no origin remote" || e.Accounts == nil {
		t.Fatalf("%d %+v %v", res.StatusCode, e, err)
	}
	f.srv.cfg.GitHub = func(context.Context, string) (doctor.GitHubAccess, error) {
		return doctor.GitHubAccess{Repo: "o/r"}, nil
	}
	res = f.request(t, http.MethodGet, path, "", f.authed)
	if err := json.NewDecoder(res.Body).Decode(&e); err != nil || e.Accounts == nil {
		t.Fatalf("%+v %v", e, err)
	}
	for _, bad := range []string{"relative", "/no/such/folder"} {
		if res := f.request(t, http.MethodGet, "/api/routes/github?folder="+url.QueryEscape(bad), "", f.authed); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, res.StatusCode)
		}
	}
	if res := f.request(t, http.MethodGet, path, "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unpaired: %d", res.StatusCode)
	}
}
