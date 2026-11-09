package main

import (
	"context"
	"strings"
	"testing"

	pluginv1 "github.com/arafathusayn/herdsman/sdk/plugin/v1"
)

func TestDecodeReadsNestedNodes(t *testing.T) {
	source := "name \"a\"\ncount 2\non true\noff false\n\nchild {\n  key \"v\"\n}\n"
	got, err := codec{}.Decode(context.Background(), &pluginv1.DecodeRequest{Source: []byte(source)})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"child":{"key":"v"},"count":2,"name":"a","off":false,"on":true}`
	if string(got.GetJson()) != want {
		t.Fatalf("%s", got.GetJson())
	}
}

func TestDecodeRefusesBadInput(t *testing.T) {
	for _, source := range []string{"}", "9bad 1", "key nope", "child {\n", "child {\n}\n}", "child {\n9bad 1\n}"} {
		if _, err := (codec{}).Decode(context.Background(), &pluginv1.DecodeRequest{Source: []byte(source)}); err == nil {
			t.Errorf("accepted %q", source)
		}
	}
}

func TestEncodeWritesNestedNodes(t *testing.T) {
	got, err := codec{}.Encode(context.Background(), &pluginv1.EncodeRequest{Json: []byte(`{"b":{"c":1.5},"a":"x","d":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	want := "a \"x\"\nb {\n  c 1.5\n}\nd true\n"
	if string(got.GetSource()) != want {
		t.Fatalf("%q", got.GetSource())
	}
}

func TestEncodeRefusesWhatItCannotWrite(t *testing.T) {
	for _, tree := range []string{`[1]`, `{"a":[1]}`, `{"a":{"b":null}}`} {
		if _, err := (codec{}).Encode(context.Background(), &pluginv1.EncodeRequest{Json: []byte(tree)}); err == nil || !strings.Contains(err.Error(), "invalid_argument") {
			t.Errorf("%s: %v", tree, err)
		}
	}
}

func TestSetIsNotOffered(t *testing.T) {
	if _, err := (codec{}).Set(context.Background(), &pluginv1.SetRequest{}); err == nil {
		t.Fatal("no error")
	}
}
