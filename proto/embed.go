// Package proto holds the contract sources, so the binary can print them.
package proto

import "embed"

// Files holds every .proto file under herdsman/.
//
//go:embed herdsman/*/v1/*.proto
var Files embed.FS
