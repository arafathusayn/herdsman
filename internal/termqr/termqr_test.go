package termqr

import (
	"bytes"
	"strings"
	"testing"

	"rsc.io/qr"
)

// The drawing must decode back to the same modules: every cell is two
// vertically stacked modules of the code plus the quiet zone.
func TestWriteMatchesCode(t *testing.T) {
	const url = "http://192.168.1.20:7878/#t=0123456789abcdef0123456789abcdef0123456789abcdef"
	var buf bytes.Buffer
	if err := Write(&buf, url, "  "); err != nil {
		t.Fatal(err)
	}
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	n := code.Size + 2*quiet
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if want := (n + 1) / 2; len(lines) != want {
		t.Fatalf("lines = %d, want %d", len(lines), want)
	}
	for row, line := range lines {
		line = strings.TrimSuffix(strings.TrimPrefix(line, "  "+colors), reset)
		cells := []rune(line)
		if len(cells) != n {
			t.Fatalf("row %d has %d cells, want %d", row, len(cells), n)
		}
		for x, c := range cells {
			top, bottom := c == '█' || c == '▀', c == '█' || c == '▄'
			if top != module(code, x, 2*row) || bottom != module(code, x, 2*row+1) {
				t.Fatalf("cell (%d,%d) = %q does not match the code", x, row, c)
			}
		}
	}
}

func module(code *qr.Code, x, y int) bool {
	x, y = x-quiet, y-quiet
	return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
}
