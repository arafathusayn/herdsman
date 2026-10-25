package inbox

import (
	"slices"
	"time"
	"unicode/utf8"
)

// AsOwner runs fn with whether this run owns the shared input lock, the
// answer held fixed for fn's duration (Channel.Owner). fn must not take or
// free a lock.
func (ch *Channel) AsOwner(fn func(held bool) error) error {
	if ch.Owner == nil {
		return fn(false)
	}
	return ch.Owner(ch.Path(MutationLock), fn)
}

// KeepPending leaves one exposure record waiting for the next owner of the
// input lock (the caller could not take it).
func (ch *Channel) KeepPending(unknown []Unknown) error { return ch.keepPending(unknown) }

// TryOnce takes the lock folder dir with one attempt; nil when busy. A
// caller that must keep its lock list exact retries itself, between
// attempts, with nothing held half-way.
func TryOnce(dir string) *Lock { return TryDir(dir, 1, func(time.Duration) {}) }

// AllocationEnvelope is Envelope's check for an allocation copy
// (ids/<id>/envelope): the same keys and field rules, with id the
// allocation's own. Only a copy that passes may be retired (review 4).
func AllocationEnvelope(root, p, id string) (map[string]any, bool) {
	b, ok := ReadStrict(root, p)
	if !ok {
		return nil, false
	}
	v, _ := Decode(b)
	m := Object(v)
	if m == nil || len(m) != len(envelopeKeys) {
		return nil, false
	}
	for _, k := range envelopeKeys {
		if _, ok := m[k]; !ok {
			return nil, false
		}
	}
	mid, _ := String(m, "id")
	from, _ := String(m, "from")
	to, _ := String(m, "to")
	kind, _ := String(m, "kind")
	reply, rok := String(m, "reply_to")
	body, bok := String(m, "body")
	t, tok := Int(m, "time")
	if !Equal(m["version"], jsonOne) || mid != id || !IDOK(id) ||
		!NameShape(from) || !NameShape(to) || !slices.Contains(Kinds, kind) ||
		!rok || (reply != "" && !IDOK(reply)) || !tok || t < 0 || !bok || utf8.RuneCountInString(body) > MaxBody {
		return nil, false
	}
	return m, envelopeIdentity(m["from_identity"]) && envelopeIdentity(m["to_identity"])
}
