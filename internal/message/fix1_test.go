package message

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/inbox"
)

// toBinaryProof rewrites the case's proof in the binary form (no scripts,
// the executable exe), keeping its rules and boundary.
func (c *tc) toBinaryProof(exe string) {
	c.write(exe, strings.Repeat("herdsman binary\n", 64))
	sum, _ := inbox.DigestFileFast(exe)
	c.editJSON(c.ic.Proof, func(m map[string]any) {
		delete(m, "scripts")
		m["binary"] = map[string]any{"path": exe, "checksum": sum}
	})
}

// The command itself (message authority) prints the exact RELAY or HUMAN
// line with exit 0 or 3, for the script proof and the binary proof, and
// --json gives the same status as one object.
func TestAuthorityCommandExitCodes(t *testing.T) {
	for _, form := range []string{"script", "binary"} {
		t.Run(form, func(t *testing.T) {
			c := newCase(t, true)
			c.relayBoundary()
			env := map[string]string{"HERDSMAN_NAME": O}
			if form == "binary" {
				exe := c.base("bin", "herdsman")
				os.MkdirAll(filepath.Dir(exe), 0o700)
				c.toBinaryProof(exe)
				env["@EXE"] = exe
			}
			out, code := c.run("receiver-pane", env, "message", "authority")
			if code != 0 || out != inbox.RelayLine+"\n" {
				t.Fatalf("relay: exit %d %q", code, out)
			}
			out, _ = c.run("receiver-pane", env, "message", "--json", "authority")
			check(t, form+" json relay", `^\{"type":"authority","status":"relay","text":"RELAY `, out)
			c.write(c.in("state", "boundary-tainted"), "{}")
			out, code = c.run("receiver-pane", env, "message", "authority")
			if code != 3 || out != inbox.HumanLine+"\n" {
				t.Fatalf("human: exit %d %q", code, out)
			}
			out, code = c.run("receiver-pane", env, "message", "--json", "authority")
			check(t, form+" json human", `^3 \{"type":"authority","status":"human","text":"HUMAN `, strconv.Itoa(code)+" "+out)
		})
	}
}

func TestResumeGoalJSON(t *testing.T) {
	c := newCase(t, true)
	out, _ := c.run("sender-pane", map[string]string{"HERDSMAN_NAME": A}, "message", "--json", "resume-goal", O)
	check(t, "resume-goal --json", `^\{"type":"resume","status":"(held|noop|sent)","text":"`, out)
}
