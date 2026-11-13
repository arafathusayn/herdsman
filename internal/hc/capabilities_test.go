package hc_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/arafathusayn/herdsman/internal/hc"
	"github.com/arafathusayn/herdsman/internal/hc/hctest"
)

// A core without one listed method is refused at hello, and the error names
// that method, on the control and on the subscription connection.
func TestCapabilityCheckNamesTheMissingMethod(t *testing.T) {
	for _, m := range hc.Methods {
		if m == hc.MethodHello {
			continue
		}
		t.Run(m, func(t *testing.T) {
			core := hctest.Start(t)
			core.Drop(m)
			_, err := hc.Dial(testCtx(t), core.Socket, hc.Options{})
			var missing *hc.MissingMethodError
			if !errors.As(err, &missing) || !slices.Equal(missing.Methods, []string{m}) {
				t.Fatalf("dial: %v", err)
			}
			if !strings.Contains(err.Error(), "method "+m) || !strings.Contains(err.Error(), hctest.Version) {
				t.Fatalf("error does not name %s and the version: %v", m, err)
			}
			_, err = hc.Subscribe(testCtx(t), core.Socket, hc.Filter{}, hc.Options{})
			if !errors.As(err, &missing) || missing.Methods[0] != m {
				t.Fatalf("subscribe: %v", err)
			}
			if core.Calls(m) != 0 {
				t.Fatalf("%s was called on a core that lacks it", m)
			}
		})
	}
}

func TestCapabilityCheckNamesEveryMissingMethodInListOrder(t *testing.T) {
	core := hctest.Start(t)
	core.Drop(hc.MethodExec)
	core.Drop(hc.MethodRun)
	core.DropFeature("events")
	_, err := hc.Dial(testCtx(t), core.Socket, hc.Options{})
	var missing *hc.MissingMethodError
	if !errors.As(err, &missing) {
		t.Fatalf("dial: %v", err)
	}
	if !slices.Equal(missing.Methods, []string{hc.MethodRun, hc.MethodExec}) || !slices.Equal(missing.Features, []string{"events"}) {
		t.Fatalf("missing = %+v", missing)
	}
}

func TestCoreWithoutHelloIsRefused(t *testing.T) {
	core := hctest.Start(t)
	core.Drop(hc.MethodHello)
	if _, err := hc.Dial(testCtx(t), core.Socket, hc.Options{}); !hc.IsKind(err, hc.KindUnknownMethod) {
		t.Fatalf("dial: %v", err)
	}
}

func TestMissingComparesWithTheList(t *testing.T) {
	h := hc.HelloResult{Methods: hctest.Methods, Features: hctest.Features}
	if m, f := hc.Missing(h); len(m) != 0 || len(f) != 0 {
		t.Fatalf("hc 0.1.20's lists lack %v %v", m, f)
	}
	h.Methods = slices.DeleteFunc(slices.Clone(hctest.Methods), func(s string) bool { return s == hc.MethodKeep })
	if m, _ := hc.Missing(h); !slices.Equal(m, []string{hc.MethodKeep}) {
		t.Fatalf("missing = %v", m)
	}
}

// Herdsman never calls a method off its list, even one core serves.
func TestUnlistedMethodsAreLeftOut(t *testing.T) {
	for _, m := range []string{"terminal.keys", "terminal.wait", "terminal.attach", "doc.set", "server.stop", "server.restart", "terminal.rm"} {
		if slices.Contains(hc.Methods, m) {
			t.Errorf("%s is on the list", m)
		}
		if !slices.Contains(hctest.Methods, m) {
			t.Errorf("the fake core does not serve %s", m)
		}
	}
}

func TestExpectedCoreIdentity(t *testing.T) {
	core := hctest.Start(t)
	if _, err := hc.Dial(testCtx(t), core.Socket, hc.Options{ExpectCore: core.ID}); err != nil {
		t.Fatal(err)
	}
	_, err := hc.Dial(testCtx(t), core.Socket, hc.Options{ExpectCore: "another"})
	var changed *hc.CoreChangedError
	if !errors.As(err, &changed) || changed.Got != core.ID || changed.Want != "another" {
		t.Fatalf("dial: %v", err)
	}
}
