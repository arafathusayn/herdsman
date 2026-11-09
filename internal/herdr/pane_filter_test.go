package herdr_test

import (
	"context"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/herdr"
	"github.com/arafathusayn/herdsman/internal/herdr/herdrtest"
)

func TestPaneFilterCoversEveryPaneRead(t *testing.T) {
	fake := herdrtest.Start(t)
	fake.Handle("pane.read", func(p map[string]any) (any, string) {
		return map[string]any{"read": map[string]any{"pane_id": p["pane_id"], "text": "a known-value b"}}, ""
	})
	c := herdr.New(fake.Socket)
	ctx := context.Background()
	herdr.SetPaneFilter(func(s string) string { return strings.ReplaceAll(s, "known-value", "X") })
	t.Cleanup(func() { herdr.SetPaneFilter(nil) })

	text, err := c.ReadText(ctx, "w1:p1", herdr.SourceVisible, 5)
	if err != nil || text != "a X b" {
		t.Fatalf("ReadText %q %v", text, err)
	}
	read, err := c.ReadPane(ctx, "w1:p1", herdr.ReadOptions{Lines: 5})
	if err != nil || read.Text != "a X b" {
		t.Fatalf("ReadPane %q %v", read.Text, err)
	}

	herdr.SetPaneFilter(nil)
	if text, _ := c.ReadText(ctx, "w1:p1", herdr.SourceVisible, 5); text != "a known-value b" {
		t.Fatalf("no filter: %q", text)
	}
}
