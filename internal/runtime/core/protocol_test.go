package core_test

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/arafathusayn/herdsman/internal/launch"
)

func TestOldWrapperCannotIgnoreRestrictedDescriptor(t *testing.T) {
	w := newWorld(t)
	c, err := net.DialTimeout("unix", w.reg.Path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(c).Encode(launch.WrapRequest{Op: launch.WrapAuthenticate, Protocol: 1, Intent: "old-wrapper"}); err != nil {
		t.Fatal(err)
	}
	var reply launch.WrapReply
	if err := json.NewDecoder(c).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.OK || reply.Descriptor != nil || !strings.Contains(reply.Error, "protocol 2") {
		t.Fatalf("old wrapper received authority: %+v", reply)
	}
}
