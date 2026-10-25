package web

import "github.com/arafathusayn/herdsman/internal/inbox"

// The route form's inbox checks (main's routes.go) read through the shared
// inbox package, as the Messages tab does since the inbox core moved there.

func regularFile(p string) bool { return inbox.RegularFile(p) }

func readBounded(p string, max int64) ([]byte, error) { return inbox.ReadBounded(p, max) }

func validRegistry(b []byte, readErr error) ([]ChannelPeer, bool) { return webRegistry(b, readErr) }
