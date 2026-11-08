// Package suggest embeds the bundled suggestion catalog: every *.md file in
// this folder is one entry, reviewed like code. It lives beside the entries
// because go:embed cannot reach a parent folder; internal/suggest reads and
// validates them.
package suggest

import "embed"

// Entries holds the bundled entry files at its root.
//
//go:embed *.md
var Entries embed.FS
