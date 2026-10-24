// Package termqr draws a QR code in a terminal with half-block characters,
// two modules per character cell, dark on light whatever the terminal theme.
package termqr

import (
	"fmt"
	"io"
	"strings"

	"rsc.io/qr"
)

// quiet is the margin, in modules, that scanners need around the code.
const quiet = 2

const (
	colors = "\x1b[38;5;16m\x1b[48;5;231m" // black on white
	reset  = "\x1b[0m"
)

// Write draws text as a QR code, each line indented by indent.
func Write(w io.Writer, text, indent string) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return err
	}
	n := code.Size + 2*quiet
	dark := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		b.WriteString(indent)
		b.WriteString(colors)
		for x := range n {
			top, bottom := dark(x, y), y+1 < n && dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString(reset)
		b.WriteByte('\n')
	}
	_, err = fmt.Fprint(w, b.String())
	return err
}
