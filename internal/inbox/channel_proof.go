package inbox

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arafathusayn/herdsman/internal/herdr"
)

// Channel is an inbox opened by a herdsman message command: the inbox and
// the herdsman executable the setup proof may name.
//
// The setup proof has two forms. The script form lists every installed
// channel script and adapter under .scripts[] (PreflightValid). The binary
// form, written by a setup after the port, names the herdsman executable
// instead:
//
//	{"inbox":..., "policy_verified":true, "rules":[{"path":...,"checksum":...}],
//	 "binary":{"path":"/abs/herdsman","checksum":"<crc>-<size>"}}
//
// A command accepts either form, so an inbox set up before the port keeps
// working while the scripts exist.
type Channel struct {
	*Root
	// Executable is the absolute path of the running herdsman binary, links
	// resolved; "" accepts no binary proof.
	Executable string
	// MutationHeld: this process owns the shared input lock, so a boundary
	// check publishes a waiting exposure itself (e93bcc1).
	MutationHeld bool
}

// OpenChannel opens an inbox as Open does, accepting a binary proof of exe
// where the script proof does not verify.
func OpenChannel(o Options, exe string) (*Channel, error) {
	ro := o
	ro.ReadOnly = true
	r, err := Open(ro)
	if err != nil {
		return nil, err
	}
	r.ReadOnly = o.ReadOnly
	ch := &Channel{Root: r, Executable: exe}
	if r.TimingStale && !o.ReadOnly {
		return nil, die("invalid or conflicting shared timing configuration")
	}
	if r.ProofStale && ch.BinaryProofValid() {
		r.ProofStale = false
	}
	if r.ProofStale && !o.ReadOnly {
		return nil, die("sandbox policy or protected installation proof stale; repeat human setup")
	}
	if !o.ReadOnly {
		for _, d := range []string{"state", "ids", "enrolled", "alerts"} {
			if err := os.MkdirAll(r.Path(d), 0o700); err != nil {
				return nil, die("cannot create %s: %v", d, err)
			}
		}
	}
	return ch, nil
}

// ProofValid is preflight_valid for either proof form.
func (ch *Channel) ProofValid() bool {
	return ch.PreflightValid() || ch.BinaryProofValid()
}

// BinaryProofValid checks the binary form of the setup proof: the proof
// names this inbox with a verified policy, lists rules (each an absolute
// path and a checksum, every one matching) and names the running herdsman
// executable with its checksum.
func (ch *Channel) BinaryProofValid() bool {
	r := ch.Root
	exe := ch.Executable
	if exe == "" || !filepath.IsAbs(exe) || r.Preflight == "" || r.Preflight != r.Path("setup.json") {
		return false
	}
	b, ok := ReadStrict(r.Dir, r.Preflight)
	if !ok {
		return false
	}
	v, _ := Decode(b)
	proof := Object(v)
	if proof == nil || proof["inbox"] != r.Dir || proof["policy_verified"] != true {
		return false
	}
	rules, rok := proof["rules"].([]any)
	if !rok || len(rules) == 0 {
		return false
	}
	entries := append(append([]any{}, rules...), proof["binary"])
	for i, e := range entries {
		m := Object(e)
		if len(m) != 2 {
			return false
		}
		p, pok := String(m, "path")
		c, cok := String(m, "checksum")
		if !pok || !cok || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\t\r\n\\") || !checksumShape(c) {
			return false
		}
		if i == len(entries)-1 && p != exe {
			return false
		}
		if !Regular(r.Dir, p) {
			return false
		}
		if sum, err := DigestFileCached(p); err != nil || sum != c {
			return false
		}
	}
	return true
}

// cksumTable is POSIX cksum's CRC table (polynomial 0x04C11DB7, MSB first).
var cksumTable = func() *[256]uint32 {
	var t [256]uint32
	for i := range t {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return &t
}()

// DigestReader is Digest of a stream, table driven, for large files such
// as the herdsman executable.
func DigestReader(rd io.Reader) (string, error) {
	var crc uint32
	var n int64
	buf := make([]byte, 64<<10)
	for {
		k, err := rd.Read(buf)
		for _, c := range buf[:k] {
			crc = crc<<8 ^ cksumTable[byte(crc>>24)^c]
		}
		n += int64(k)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	for l := n; l > 0; l >>= 8 {
		crc = crc<<8 ^ cksumTable[byte(crc>>24)^byte(l)]
	}
	return strconv.FormatUint(uint64(^crc), 10) + "-" + strconv.FormatInt(n, 10), nil
}

// DigestFileFast is DigestReader of a file.
func DigestFileFast(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return DigestReader(f)
}

// Boundary is BoundaryOK with either proof form.
func (ch *Channel) Boundary(ctx context.Context, c *herdr.Client) (bool, string, []Unknown) {
	r := ch.Root
	if r.ProofStale || !ch.ProofValid() {
		return false, FaultStatic, nil
	}
	if _, ok := r.Registry(); !ok || Exists(r.Path("state", "boundary-tainted")) {
		return false, FaultStatic, nil
	}
	if ch.ExposurePending() {
		// The owner of the input lock publishes it now; any other check
		// fails closed until the next owner does (e93bcc1).
		if ch.MutationHeld {
			_ = ch.FoldExposure()
			return false, FaultStatic, nil
		}
		return false, FaultPending, nil
	}
	pb, _ := os.ReadFile(r.Path("peers.approved"))
	pair, _ := Decode(pb)
	pb2, _ := os.ReadFile(r.Preflight)
	pv, _ := Decode(pb2)
	b := Object(Object(pv)["boundary"])
	if !boundaryFields(b, pair) {
		return false, FaultStatic, nil
	}
	agents, err := c.AgentList(ctx)
	if err != nil || len(agents) == 0 {
		return false, FaultList, nil
	}
	var unknown []Unknown
	for _, a := range agents {
		var sess any
		if len(a.AgentSession) > 0 {
			sess, _ = Decode(a.AgentSession)
		}
		var harness any
		if a.Agent.Agent != nil {
			harness = *a.Agent.Agent
		}
		if knownAgent(b, map[string]any{"pane": a.PaneID, "session": sess}, harness) {
			continue
		}
		raw := a.AgentSession
		if len(raw) == 0 {
			raw = json.RawMessage("null")
		}
		unknown = append(unknown, Unknown{Pane: a.PaneID, Agent: harness, Session: raw})
	}
	if len(unknown) > 0 {
		return false, FaultInventory, unknown
	}
	return true, "", nil
}

// Authority is message-authority.sh with either proof form.
func (ch *Channel) Authority(ctx context.Context, c *herdr.Client) (string, int) {
	if ok, _, _ := ch.Boundary(ctx, c); ok {
		return RelayLine, 0
	}
	return HumanLine, 3
}

type exposure struct {
	Reason string    `json:"reason"`
	Agents []Unknown `json:"agents"`
	Time   int64     `json:"time"`
}
