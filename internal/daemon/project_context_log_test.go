package daemon

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	contextrepo "github.com/arafathusayn/herdsman/internal/context"
	daemonv1 "github.com/arafathusayn/herdsman/sdk/daemon/v1"
	"github.com/arafathusayn/herdsman/sdk/rpc"
)

// A history far larger than one response, with characters a cut could split.
func largeHistory(t *testing.T) (*daemonv1.Scope, string, func(*daemonv1.ProjectServiceLogRequest) (*daemonv1.ProjectServiceLogResponse, error), func(string)) {
	t.Helper()
	sdk := contextClient(t)
	ctx := context.Background()
	root, inside := boundFolder(t)
	scope := &daemonv1.Scope{Folder: inside}
	if _, err := sdk.Project.ContextInit(ctx, &daemonv1.ProjectServiceContextInitRequest{Scope: scope}); err != nil {
		t.Fatal(err)
	}
	note := func(body string) {
		t.Helper()
		if _, err := sdk.Project.ContextNote(ctx, &daemonv1.ProjectServiceContextNoteRequest{Scope: scope, Actor: "someone", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 40 {
		note(fmt.Sprintf("%d %s", i, strings.Repeat("€", 45_000)))
	}
	read := func(req *daemonv1.ProjectServiceLogRequest) (*daemonv1.ProjectServiceLogResponse, error) {
		req.Scope = scope
		return sdk.Project.Log(ctx, req)
	}
	return scope, root, read, note
}

func rendered(t *testing.T, root string, asJSON bool) string {
	t.Helper()
	c, err := contextrepo.Log(root, contextrepo.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	render := c.WriteText
	if asJSON {
		render = c.WriteJSON
	}
	if err := render(&out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestLogComesInBoundedPages(t *testing.T) {
	_, root, read, _ := largeHistory(t)
	for _, asJSON := range []bool{false, true} {
		var joined strings.Builder
		token, pages := "", 0
		for {
			page, err := read(&daemonv1.ProjectServiceLogRequest{Json: asJSON, PageToken: token})
			if err != nil {
				t.Fatalf("json %v, page %d: %v", asJSON, pages, err)
			}
			if len(page.GetText()) > rpc.MaxBody/2 || !utf8.ValidString(page.GetText()) {
				t.Fatalf("json %v: page of %d bytes", asJSON, len(page.GetText()))
			}
			joined.WriteString(page.GetText())
			pages++
			if token = page.GetNextPageToken(); token == "" {
				break
			}
		}
		if pages < 2 || joined.String() != rendered(t, root, asJSON) {
			t.Fatalf("json %v: %d pages, %d bytes", asJSON, pages, joined.Len())
		}
	}
}

func TestLogPagesBelongToOneHistory(t *testing.T) {
	_, _, read, note := largeHistory(t)
	first, err := read(&daemonv1.ProjectServiceLogRequest{})
	if err != nil || first.GetNextPageToken() == "" {
		t.Fatalf("%v, %v", first.GetNextPageToken(), err)
	}
	note("one more")
	_, err = read(&daemonv1.ProjectServiceLogRequest{PageToken: first.GetNextPageToken()})
	wantCode(t, err, rpc.Aborted)
}

func TestLogRefusesABadPageToken(t *testing.T) {
	_, root, read, _ := largeHistory(t)
	first, err := read(&daemonv1.ProjectServiceLogRequest{})
	if err != nil {
		t.Fatal(err)
	}
	_, digest, _ := strings.Cut(first.GetNextPageToken(), ".")
	inRune := strings.Index(rendered(t, root, false), "€") + 1
	for _, token := range []string{"nonsense", "x." + digest, "-1." + digest, "999999999." + digest, strconv.Itoa(inRune) + "." + digest} {
		_, err := read(&daemonv1.ProjectServiceLogRequest{PageToken: token})
		wantCode(t, err, rpc.InvalidArgument)
	}
}

// An event added by hand or by git can hold anything; a response cannot.
func TestLogReplacesBytesThatAreNotText(t *testing.T) {
	sdk := contextClient(t)
	ctx := context.Background()
	root, inside := boundFolder(t)
	scope := &daemonv1.Scope{Folder: inside}
	if _, err := sdk.Project.ContextInit(ctx, &daemonv1.ProjectServiceContextInitRequest{Scope: scope}); err != nil {
		t.Fatal(err)
	}
	const id = "01KB2A00000000000000000001"
	file := "---\nid: " + id + "\nkind: note\nat: 2026-01-01T00:00:00Z\nactor: someone\nparents: []\n---\nbad \xff byte\n"
	if err := os.WriteFile(filepath.Join(root, contextrepo.EventsDir, id+".md"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	log, err := sdk.Project.Log(ctx, &daemonv1.ProjectServiceLogRequest{Scope: scope})
	if err != nil || !strings.Contains(log.GetText(), "bad \uFFFD byte") {
		t.Fatalf("%q, %v", log.GetText(), err)
	}
}
