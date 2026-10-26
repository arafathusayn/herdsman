package inbox

import (
	"bytes"
	"encoding/json"
)

// An exposure record stays within MaxSize, the strict reader's bound (PR
// #24 P0): when every unknown agent does not fit, the record keeps the
// agents that do and counts the rest in "omitted", so a large observation
// is kept and published, never lost to a write or read that refuses it.

// fitAgents is the longest prefix of unknown whose record, built by build,
// fits MaxSize, and the record.
func fitAgents(unknown []Unknown, build func(agents []Unknown, omitted int) []byte) []byte {
	if b := build(unknown, 0); len(b) <= MaxSize {
		return b
	}
	lo, hi := 0, len(unknown)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if len(build(unknown[:mid], len(unknown)-mid)) <= MaxSize {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return build(unknown[:lo], len(unknown)-lo)
}

// pendingRecord is a waiting exposure record, bounded as record_exposure
// keeps it (PR 24, a38ff72): every agent when the record fits MaxSize;
// otherwise the first twenty, marked truncated with the full count; when
// even those do not fit, the minimal record without agents, truncated.
// keepPending passes a non-nil list.
func pendingRecord(unknown []Unknown, now int64) []byte {
	enc := func(rec map[string]any) []byte {
		b, _ := json.Marshal(rec)
		return append(b, '\n')
	}
	if b := enc(map[string]any{"agents": unknown, "time": now}); len(b) <= MaxSize {
		return b
	}
	first := unknown[:min(pendingFirst, len(unknown))]
	if b := enc(map[string]any{"agents": first, "truncated": true, "count": len(unknown), "time": now}); len(b) <= MaxSize {
		return b
	}
	return enc(map[string]any{"agents": []Unknown{}, "truncated": true, "count": len(unknown), "time": now})
}

// pendingFirst is how many agents a truncated waiting record keeps.
const pendingFirst = 20

// taintedRecord is state/boundary-tainted, bounded; omitted adds the
// agents that waiting records had already left out.
func taintedRecord(unknown []Unknown, omitted int, now int64) []byte {
	return fitAgents(unknown, func(agents []Unknown, more int) []byte {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(exposure{Reason: "live agent outside the protected setup", Agents: agents, Omitted: omitted + more, Time: now})
		return b.Bytes()
	})
}
