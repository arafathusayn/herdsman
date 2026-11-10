package hc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

// Lines hc 0.1.20 sent to a private core during probing, and near misses.
var frameSeeds = []string{
	`{"id":1,"jsonrpc":"2.0","result":{"complete":true,"how":"screen","text":"hi"}}`,
	`{"error":{"code":-32000,"data":{"hint":"see ` + "`hc ls --all`" + `","kind":"not_found"},"message":"no terminal \"t999\""},"id":14,"jsonrpc":"2.0"}`,
	`{"error":{"code":-32700,"data":{"hint":"see ` + "`hc schema`" + `","kind":"parse_error"},"message":"expected ident at line 1 column 2"},"id":null,"jsonrpc":"2.0"}`,
	`{"jsonrpc":"2.0","method":"event","params":{"data":{"labels":{"probe":"1","x":"y"}},"seq":3,"terminal":"t1","type":"labels"}}`,
	`{"jsonrpc":"2.0","method":"heartbeat","params":{"seq":0}}`,
	`{"jsonrpc":"2.0","method":"missed","params":{"seq":9,"terminals":[]}}`,
	`{"id":1,"jsonrpc":"2.0","result":null}`,
	`{"id":-1,"jsonrpc":"2.0","result":{}}`,
	`{"id":"1","jsonrpc":"2.0","result":{}}`,
	`{"id":1,"jsonrpc":"2.0"}`,
	`{"id":1,"jsonrpc":"2.0","result":{},"error":{"code":1,"message":"x"}}`,
	`{"jsonrpc":"2.0","method":"","params":{}}`,
	`{"jsonrpc":"2.0","id":3,"method":"terminal.get","params":{}}`,
	`[]`, `null`, ``, "\n", `{"jsonrpc":"2.0"}`, `{"id":18446744073709551616,"jsonrpc":"2.0","result":1}`,
}

func FuzzParseFrame(f *testing.F) {
	for _, s := range frameSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		fr, err := parseFrame(line)
		if err != nil {
			var fe *FrameError
			if !errors.As(err, &fe) {
				t.Fatalf("error of type %T: %v", err, err)
			}
			return
		}
		switch fr.kind {
		case frameResponse:
			if (len(fr.result) > 0) == (fr.err != nil) {
				t.Fatalf("response with result %q and error %v", fr.result, fr.err)
			}
			if fr.nullID && fr.err == nil {
				t.Fatal("null id without an error")
			}
			if fr.err != nil {
				e := coreError("m", fr.err)
				if e.Kind == "" || e.Error() == "" {
					t.Fatalf("core error without a kind: %#v", e)
				}
			}
		case frameNotification:
			if fr.method == "" {
				t.Fatal("notification without a method")
			}
		default:
			t.Fatalf("kind %d", fr.kind)
		}
	})
}

// readFrame never returns more than the bound, and returns every line of a
// stream that fits.
func FuzzReadFrame(f *testing.F) {
	f.Add([]byte("a\nbb\n"), uint16(4))
	f.Add(bytes.Repeat([]byte("x"), 300), uint16(16))
	f.Add([]byte("\n\n\n"), uint16(1))
	f.Fuzz(func(t *testing.T, stream []byte, bound uint16) {
		max := int(bound%512) + 1
		r := bufio.NewReaderSize(bytes.NewReader(stream), 16)
		var got []byte
		for {
			line, err := readFrame(r, max)
			if len(line) > max {
				t.Fatalf("line of %d bytes over a bound of %d", len(line), max)
			}
			if err != nil {
				if errors.Is(err, ErrFrameTooLarge) {
					return
				}
				if !errors.Is(err, io.EOF) {
					t.Fatalf("error %v", err)
				}
				break
			}
			got = append(got, line...)
		}
		if i := bytes.LastIndexByte(stream, '\n'); !bytes.Equal(got, stream[:i+1]) {
			t.Fatalf("read %q of %q", got, stream)
		}
	})
}

// Whatever core sends as event data, applying it never panics and leaves the
// cache keyed by address.
func FuzzApplyEvent(f *testing.F) {
	f.Add("t1", EventLabels, []byte(`{"labels":{"k":"v"}}`))
	f.Add("t1", EventCreated, []byte(`{"address":"t1","labels":{},"argv":[],"cwd":"/","state":"live","created_ms":1,"screen":"exact","cols":1,"rows":1}`))
	f.Add("t1", EventFinished, []byte(`{"exit_code":null,"finished_ms":5,"reason":"killed","signal":null}`))
	f.Add("t1", EventStatus, []byte(`{"state":"working"}`))
	f.Add("t1", EventForeground, []byte(`null`))
	f.Add("t2", EventRemoved, []byte(`{}`))
	f.Fuzz(func(t *testing.T, addr, typ string, data []byte) {
		s := &Subscription{filter: Filter{Labels: map[string]string{"k": "v"}}}
		s.install(snapshot{Seq: 1, Terminals: []Terminal{{Address: "t1", Labels: map[string]string{"k": "v"}, State: "live"}}})
		s.apply(Event{Seq: 2, Terminal: addr, Type: typ, Data: json.RawMessage(data)})
		for a, term := range s.cache {
			if a != term.Address {
				t.Fatalf("cache key %q holds %q", a, term.Address)
			}
		}
	})
}
