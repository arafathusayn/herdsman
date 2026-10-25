package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// The TA and Y6 cases of the skill's message-tests.sh.

type rig struct {
	t  *testing.T
	tc string // the audit folder: memory/ and project/ under it
}

func newRig(t *testing.T) *rig {
	tc, _ := filepath.EvalSymlinks(t.TempDir())
	_ = os.MkdirAll(filepath.Join(tc, "memory"), 0o755)
	_ = os.MkdirAll(filepath.Join(tc, "project"), 0o755)
	return &rig{t: t, tc: tc}
}

func jsonl(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// tu is a Claude tool_use record.
func tu(id, name string, input any, session ...string) string {
	s := "coord-session"
	if len(session) > 0 {
		s = session[0]
	}
	return jsonl(map[string]any{"type": "assistant", "sessionId": s, "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}})
}

func sh(id, cmd string) string { return tu(id, "Bash", map[string]any{"command": cmd}) }

func (r *rig) file(name string, lines ...string) string {
	p := filepath.Join(r.tc, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	return p
}

// aud runs the audit as message-tests.sh's aud does: AUDIT_CHANNEL_DIR=/skill/scripts.
func (r *rig) aud(o Options) string {
	if o.ChannelDir == "-" {
		o.ChannelDir = ""
	} else if o.ChannelDir == "" {
		o.ChannelDir = "/skill/scripts"
	}
	var b bytes.Buffer
	code := Run(o, &b)
	return b.String() + fmt.Sprintf("rc=%d", code)
}

func match(t *testing.T, name, re, s string) {
	t.Helper()
	if !regexp.MustCompile("(?m)" + re).MatchString(s) {
		t.Errorf("%s: want /%s/ in %q", name, re, s)
	}
}

func noMatch(t *testing.T, name, re, s string) {
	t.Helper()
	if regexp.MustCompile("(?m)" + re).MatchString(s) {
		t.Errorf("%s: unexpected /%s/ in %q", name, re, s)
	}
}

func flat(s string) string { return strings.ReplaceAll(s, "\n", " ") }

func (r *rig) clean() []string {
	return []string{
		jsonl(map[string]any{"type": "user", "sessionId": "coord-session", "message": map[string]any{"content": "please run git push and npm test"}}),
		jsonl(map[string]any{"type": "assistant", "sessionId": "coord-session", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "I will not run git push or gh pr create."}}}}),
		tu("ok1", "Read", map[string]any{"file_path": "/any/file"}),
		sh("ok2", "/bin/bash /skill/scripts/message.sh status"),
		sh("ok3", "git log --oneline -3"),
		tu("ok4", "Write", map[string]any{"file_path": filepath.Join(r.tc, "memory", "note.md"), "content": "x"}),
		sh("ok5", "gh pr view 9 --json state"),
	}
}

func TestClaudeTranscripts(t *testing.T) {
	r := newRig(t)
	mem := filepath.Join(r.tc, "memory") + "/"
	clean := r.file("clean.jsonl", r.clean()...)
	out := r.aud(Options{Harness: "claude", Transcript: clean, Session: "coord-session", Roots: []string{mem}})
	match(t, "TA1 allowed calls and prose that names forbidden commands are clean", `^AUDIT clean calls=5 .*rc=0`, flat(out))

	flagged := r.file("flagged.jsonl", append(r.clean(),
		tu("w1", "Write", map[string]any{"file_path": filepath.Join(r.tc, "project", "app.ts"), "content": "x"}),
		tu("w2", "Edit", map[string]any{"file_path": r.tc + "/memory/../project/app.ts"}),
		sh("b1", "git commit -m change"),
		sh("b2", "cd /repo && git -C /repo push origin HEAD"),
		sh("b3", "gh pr create --title t --body-file f"),
		sh("b4", "gh api -X POST repos/o/r/issues/1/comments -f body=SECRETVALUE"),
		sh("b5", "curl -X POST https://example.invalid/hook"),
		sh("b6", "npm test"),
		sh("b7", "/bin/bash scripts/test/run-tests.sh"),
		sh("b8", "docker compose up -d"),
		tu("m1", "mcp__chat__send_message", map[string]any{"text": "hello"}),
	)...)
	out = r.aud(Options{Harness: "claude", Transcript: flagged, Session: "coord-session", Roots: []string{mem}})
	for _, want := range []string{"w1 class=project-write", "w2 class=project-write", "b1 class=git-write", "b2 class=git-write", "b3 class=outward-write",
		"b4 class=outward-write", "b5 class=outward-write", "b6 class=test-or-package", "b7 class=test-or-package", "b8 class=service-or-container",
		"m1 class=tracker-or-chat-write"} {
		match(t, "TA2 flagged: "+want, "call="+want, out)
	}
	match(t, "TA2b the audit reports the flagged count and exits 1", `AUDIT flagged=11 calls=16.*rc=1`, flat(out))
	noMatch(t, "TA2c targets are redacted: no argument values or full paths", "SECRETVALUE|"+regexp.QuoteMeta(r.tc+"/project"), out)
	match(t, "TA2d a flag names the tool and the call id", `^FLAG tool=Bash call=b1 `, out)
	match(t, "TA2e (Go) the program word is the target, never an argument", `call=b2 class=git-write target=git$`, out)

	out = r.aud(Options{Harness: "claude", Transcript: clean, Session: "other-session", Roots: []string{mem}})
	match(t, "TA3 another session's transcript is an audit gap, never clean", `^GAP transcript is not the named current session.*rc=2`, flat(out))
	mixed := r.file("mixed.jsonl", append(r.clean(), tu("x1", "Bash", map[string]any{"command": "git push"}, "other-session"))...)
	match(t, "TA3b records of two sessions are a gap", `^GAP .*rc=2`, flat(r.aud(Options{Harness: "claude", Transcript: mixed, Session: "coord-session", Roots: []string{mem}})))
	match(t, "TA4 a missing transcript is a gap", `^GAP transcript missing.*rc=2`, flat(r.aud(Options{Harness: "claude", Transcript: filepath.Join(r.tc, "missing.jsonl"), Session: "coord-session"})))
	match(t, "TA4b an unsupported harness is a gap", `^GAP no transcript adapter.*rc=2`, flat(r.aud(Options{Harness: "opencode", Transcript: clean, Session: "coord-session"})))
	broken := r.file("broken.jsonl", append(r.clean(), `{"type":"assistant",`)...)
	match(t, "TA4c a malformed record is a gap", `^GAP malformed transcript record.*rc=2`, flat(r.aud(Options{Harness: "claude", Transcript: broken, Session: "coord-session"})))
	match(t, "TA4d a relative transcript path is a gap", `^GAP transcript path must be absolute.*rc=2`, flat(r.aud(Options{Harness: "claude", Transcript: "audit/clean.jsonl", Session: "coord-session"})))
	empty := filepath.Join(r.tc, "empty.jsonl")
	_ = os.WriteFile(empty, nil, 0o644)
	match(t, "TA4e an empty transcript is a gap", `^GAP no session record.*rc=2`, flat(r.aud(Options{Harness: "claude", Transcript: empty, Session: "coord-session"})))
	_ = os.Symlink(clean, filepath.Join(r.tc, "link.jsonl"))
	match(t, "TA4f a linked transcript is a gap", `^GAP transcript missing or not a regular file.*rc=2`, flat(r.aud(Options{Harness: "claude", Transcript: filepath.Join(r.tc, "link.jsonl"), Session: "coord-session"})))

	grants := r.file("grants", "git commit -m change")
	out = r.aud(Options{Harness: "claude", Transcript: flagged, Session: "coord-session", Roots: []string{mem}, GrantFile: grants})
	noMatch(t, "TA5 an exact granted command line is not flagged", `call=b1 `, out)
	match(t, "TA5b and the grant exempts no other line", `call=b2 class=git-write`, out)

	before, _ := inbox.DigestFile(flagged)
	r.aud(Options{Harness: "claude", Transcript: flagged, Session: "coord-session"})
	if after, _ := inbox.DigestFile(flagged); after != before {
		t.Error("TA7 the audit leaves the transcript unchanged")
	}
}

func TestSettingsGaps(t *testing.T) {
	r := newRig(t)
	clean := r.file("clean.jsonl", r.clean()...)
	match(t, "an invalid session id", `^GAP invalid session id`, r.aud(Options{Harness: "claude", Transcript: clean, Session: "a/b"}))
	match(t, "a relative root", `^GAP allowed write root must be absolute and end in /: rel/`, r.aud(Options{Harness: "claude", Transcript: clean, Session: "coord-session", Roots: []string{"rel/"}}))
	match(t, "a root without the slash", `^GAP allowed write root must be absolute and end in /: /x`, r.aud(Options{Harness: "claude", Transcript: clean, Session: "coord-session", Roots: []string{"/x"}}))
	match(t, "a relative channel folder", `^GAP AUDIT_CHANNEL_DIR must be absolute`, r.aud(Options{Harness: "claude", Transcript: clean, Session: "coord-session", ChannelDir: "rel"}))
	match(t, "a missing grant file", `^GAP grant file unavailable`, r.aud(Options{Harness: "claude", Transcript: clean, Session: "coord-session", GrantFile: filepath.Join(r.tc, "none")}))
	if os.Geteuid() != 0 {
		_ = os.Chmod(clean, 0)
		match(t, "an unreadable transcript", `^GAP transcript missing`, r.aud(Options{Harness: "claude", Transcript: clean, Session: "coord-session"}))
	}
}

func TestCodexTranscripts(t *testing.T) {
	r := newRig(t)
	mem := filepath.Join(r.tc, "memory") + "/"
	fc := func(name, id string, args any) string {
		var a string
		if s, ok := args.(string); ok {
			a = s
		} else {
			a = jsonl(args)
		}
		return jsonl(map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call", "name": name, "call_id": id, "arguments": a}})
	}
	patch := func(id, body string) string {
		return jsonl(map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": id, "input": body}})
	}
	meta := jsonl(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "codex-session"}})
	codex := r.file("codex.jsonl", meta,
		fc("exec_command", "c1", map[string]any{"cmd": "/bin/bash /skill/scripts/message.sh read"}),
		fc("shell", "c2", map[string]any{"command": []any{"bash", "-lc", "git push origin HEAD"}}),
		patch("c3", "*** Begin Patch\n*** Update File: "+r.tc+"/project/app.ts\n@@\n-a\n+b\n*** End Patch"),
		patch("c4", "*** Begin Patch\n*** Add File: "+r.tc+"/memory/note.md\n+x\n*** End Patch"),
		jsonl(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "git push is not mine to run"}}}}),
	)
	out := r.aud(Options{Harness: "codex", Transcript: codex, Session: "codex-session", Roots: []string{mem}})
	match(t, "TA6 Codex: a shell git push is flagged", `call=c2 class=git-write`, out)
	match(t, "TA6b Codex: a patch outside the allowed roots is flagged", `call=c3 class=project-write`, out)
	noMatch(t, "TA6c Codex: channel commands and an allowed patch are not flagged", `call=c1 |call=c4 `, out)
	match(t, "TA6d Codex: two flags of four calls", `AUDIT flagged=2 calls=4.*rc=1`, flat(out))
	match(t, "TA6e Codex: another session is a gap", `^GAP transcript is not the named current session.*rc=2`, flat(r.aud(Options{Harness: "codex", Transcript: codex, Session: "other-session"})))
	bad := r.file("codex-bad2.jsonl", meta, fc("shell", "c9", "not json"))
	match(t, "TA6f Codex: unreadable call arguments are a gap", `^GAP malformed tool-call arguments: call c9.*rc=2`, flat(r.aud(Options{Harness: "codex", Transcript: bad, Session: "codex-session"})))
	y6 := r.file("y6-codex.jsonl", meta, fc("exec", "x1", map[string]any{"code": `tools.exec_command({cmd:"git push origin HEAD"})`}))
	match(t, "Y6d a Codex nested execution tool is a gap", `^GAP unsupported tool exec.*rc=2`, flat(r.aud(Options{Harness: "codex", Transcript: y6, Session: "codex-session"})))

	// The other Codex record forms.
	more := r.file("more.jsonl", meta,
		fc("apply_patch", "p1", map[string]any{"input": "*** Begin Patch\n*** Delete File: /etc/x\n*** Move to: " + r.tc + "/memory/y\n*** End Patch"}),
		fc("apply_patch", "p2", map[string]any{"patch": "no files"}),
		fc("shell_command", "s1", map[string]any{"command": "npm test"}),
		fc("shell", "s2", map[string]any{"command": []any{"git", "push", 1}}),
		jsonl(map[string]any{"type": "response_item", "payload": map[string]any{"type": "local_shell_call", "call_id": "l1", "action": map[string]any{"command": []any{"rm", "-rf", "/x"}}}}),
		jsonl(map[string]any{"type": "response_item", "payload": map[string]any{"type": "local_shell_call", "call_id": "l2", "action": map[string]any{"command": "ls"}}}),
		jsonl(map[string]any{"type": "response_item", "payload": "x"}),
		`"a string record"`,
	)
	out = r.aud(Options{Harness: "codex", Transcript: more, Session: "codex-session", Roots: []string{mem}})
	match(t, "a deleted file outside the roots", `call=p1 class=project-write target=.../x$`, out)
	noMatch(t, "a move into the roots", `target=.../y`, out)
	match(t, "a patch with no file is checked as a path-less write", `call=p2 class=project-write target=.../$`, out)
	match(t, "shell_command", `call=s1 class=test-or-package target=npm$`, out)
	match(t, "a command array with a number", `call=s2 class=git-write`, out)
	match(t, "a local shell call", `call=l1 class=project-write target=rm$`, out)
	s3 := r.file("s3.jsonl", meta, fc("exec_command", "s3", map[string]any{}))
	trail := r.file("trail.jsonl", meta, fc("shell", "s4", `{"command":["ls"]} 1`))
	match(t, "arguments with trailing data are a gap", `^GAP malformed tool-call arguments: call s4`, r.aud(Options{Harness: "codex", Transcript: trail, Session: "codex-session"}))
	match(t, "an exec without a command is a gap", `^GAP malformed tool-call arguments: call s3`, r.aud(Options{Harness: "codex", Transcript: s3, Session: "codex-session"}))
	noSession := r.file("nosession.jsonl", jsonl(map[string]any{"type": "session_meta", "payload": map[string]any{}}))
	match(t, "a session record without an id", `^GAP no session record`, r.aud(Options{Harness: "codex", Transcript: noSession, Session: "codex-session"}))
}

func TestScriptRuns(t *testing.T) {
	r := newRig(t)
	scripts := r.file("scripts.jsonl",
		jsonl(map[string]any{"type": "user", "sessionId": "coord-session", "message": map[string]any{"content": "start"}}),
		sh("s1", "bash /tmp/do-push.sh"),
		sh("s2", "./deploy.sh --now"),
		sh("s3", `python3 -c "import os"`),
		sh("s4", "/bin/bash /skill/scripts/../tmp/message.sh status"),
		sh("s5", "/bin/bash /other/scripts/message.sh status"),
		sh("s6", "source ./env.sh"),
		sh("s7", "sh -c true"),
		sh("s8", "ls /tmp && /bin/bash /skill/scripts/message.sh status && perl x.pl"),
		sh("s9", "env X=1 /skill/scripts/hidden.sh"),
		sh("s10", "/bin/bash -c /skill/scripts/message.sh"),
		sh("k1", "ls -la /tmp"),
		sh("k2", "cd /x && /bin/bash /skill/scripts/message.sh read | jq -c ."),
		sh("k3", "HERDSMAN_NAME=route-adjutant /bin/bash '/skill/scripts/message.sh' status"),
		sh("k4", "cat /x/notes.md"),
		sh("k5", "/skill/scripts/message.sh status"),
	)
	out := r.aud(Options{Harness: "claude", Transcript: scripts, Session: "coord-session"})
	for i := 1; i <= 10; i++ {
		match(t, fmt.Sprintf("TA8 a script or interpreter run is flagged: s%d", i), fmt.Sprintf("call=s%d class=script-run", i), out)
	}
	noMatch(t, "TA8b exact channel scripts and plain reads stay clean", `call=k[0-9]`, out)
	match(t, "TA8c ten flags of fourteen calls", `AUDIT flagged=10 calls=15.*rc=1`, flat(out))
	out = r.aud(Options{Harness: "claude", Transcript: scripts, Session: "coord-session", ChannelDir: "-"})
	match(t, "TA8d without the channel folder even a channel script run is flagged", `call=k2 class=script-run`, out)

	// The herdsman binary: its channel commands stay clean, any other run is a script run.
	bin := r.file("bin.jsonl",
		jsonl(map[string]any{"type": "user", "sessionId": "coord-session", "message": map[string]any{"content": "start"}}),
		sh("h1", "herdsman message status"),
		sh("h2", "HERDSMAN_NAME=route-adjutant /usr/local/bin/herdsman message read"),
		sh("h3", "herdsman approval-gate consume /x/go.json post pr-comment head1 -"),
		sh("h4", "herdsman checkpointer guard route-orchestrator w1:p2"),
		sh("h5", "herdsman message leak-gate /x/pr.md"),
		sh("h6", "herdsman message courier"),
		sh("h7", "herdsman message card-watch route-adjutant"),
		sh("h8", "herdsman message resume-goal im-a"),
		sh("h9", "herdsman message register --name route-adjutant"),
		sh("f1", "herdsman message folder"),
		sh("f2", "herdsman checkpointer --once"),
		sh("f3", "herdsman route log /x/00-route.md done"),
		sh("f4", "herdsman"),
		sh("f5", "herdsman message"),
	)
	out = r.aud(Options{Harness: "claude", Transcript: bin, Session: "coord-session"})
	noMatch(t, "TA9 (Go) the binary's channel commands stay clean", `call=h[0-9]`, out)
	for i := 1; i <= 5; i++ {
		match(t, fmt.Sprintf("TA9b (Go) another run of the binary is a script run: f%d", i), fmt.Sprintf("call=f%d class=script-run target=herdsman", i), out)
	}
}

func TestDirectWrites(t *testing.T) {
	r := newRig(t)
	mem := filepath.Join(r.tc, "memory") + "/"
	start := jsonl(map[string]any{"type": "user", "sessionId": "coord-session", "message": map[string]any{"content": "start"}})
	tgap := func(record string) string {
		return flat(r.aud(Options{Harness: "claude", Transcript: r.file("y6.jsonl", start, record), Session: "coord-session", Roots: []string{mem}}))
	}
	match(t, "Y6 an unknown tool is a gap", `^GAP unsupported tool send_email.*rc=2`, tgap(tu("y1", "send_email", map[string]any{"to": "someone"})))
	match(t, "Y6b a shell call without a command string is a gap", `^GAP malformed tool-call arguments: call y2.*rc=2`, tgap(tu("y2", "Bash", map[string]any{"command": []any{"git", "push"}})))
	match(t, "Y6c an MCP call that is neither a known read nor a write is a gap", `^GAP unsupported tool mcp__mail__frobnicate.*rc=2`, tgap(tu("y3", "mcp__mail__frobnicate", map[string]any{})))
	flags := r.file("y6-flags.jsonl", start,
		sh("f1", "gh api -f body=hello repos/o/r/issues/1/comments"),
		sh("f2", "curl --data payload https://example.invalid/hook"),
		sh("f3", "gh api -fbody=hello repos/o/r/issues/1/comments"),
		sh("f4", "env -i /tmp/do-push.sh"),
		sh("f5", `awk 'BEGIN { system("git push") }'`),
		sh("f6", "printf changed > /project/app.ts"),
		sh("f7", "echo x >> /project/notes.md"),
		sh("f8", "cp /tmp/a /project/app.ts"),
		sh("r1", "gh api repos/o/r/pulls"),
		sh("r2", "curl -s https://example.invalid/status"),
		sh("r3", `awk '{print $1}' /x/list.txt`),
		sh("r4", "ls /x 2>/dev/null"),
		sh("r5", "printf note > "+r.tc+"/memory/n.md"),
		tu("r6", "mcp__tracker__get_issue", map[string]any{"id": "1"}),
	)
	out := r.aud(Options{Harness: "claude", Transcript: flags, Session: "coord-session", Roots: []string{mem}})
	for i := 1; i <= 8; i++ {
		match(t, fmt.Sprintf("Y6e a direct write or effect-capable run is flagged: f%d", i), fmt.Sprintf("call=f%d ", i), out)
	}
	noMatch(t, "Y6f reads, a null redirect and an allowed-root write stay clean", `call=r[0-9]`, out)
	match(t, "Y6g eight flags of fourteen calls", `AUDIT flagged=8 calls=14.*rc=1`, flat(out))

	// More forms: sed -i, a 2>&1 redirect, an awk -f program, an empty tool name, a record without content.
	more := r.file("more.jsonl", start,
		sh("m1", "sed -i s/a/b/ /x/f"),
		sh("m2", "make 2>&1"),
		sh("m3", "awk -f prog.awk /x/list"),
		sh("m4", "ls >&2"),
		sh("m5", "nice -n 5 ./x.sh"),
		sh("m6", "xargs rm"),
		sh("m7", "kubectl get pods"),
		sh("m8", "./run-tests.sh"),
		tu("", "", map[string]any{}),
		tu("n1", "Read", "not an object"),
		jsonl(map[string]any{"type": "assistant", "sessionId": "coord-session", "message": "x"}),
	)
	out = r.aud(Options{Harness: "claude", Transcript: more, Session: "coord-session", Roots: []string{mem}})
	match(t, "sed -i", `call=m1 class=project-write target=sed`, out)
	match(t, "make", `call=m2 class=test-or-package target=make`, out)
	match(t, "awk -f", `call=m3 class=script-run target=awk`, out)
	noMatch(t, "a redirect to a descriptor", `call=m4`, out)
	match(t, "a wrapper with a count", `call=m5 class=script-run target=nice`, out)
	match(t, "xargs", `call=m6 class=script-run target=xargs`, out)
	match(t, "kubectl", `call=m7 class=service-or-container target=kubectl`, out)
	match(t, "a test script", `call=m8 class=test-or-package`, out)
	match(t, "an empty tool name is no call", `calls=9`, out)
}

func TestInternals(t *testing.T) {
	if gap("x %d", 1).Error() != "x 1" {
		t.Error("gap text")
	}
	if _, err := readRecords(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("a missing file reads")
	}
	if program("") != "" {
		t.Error("an empty match has a program")
	}
}
