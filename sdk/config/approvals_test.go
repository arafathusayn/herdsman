package config

import (
	"reflect"
	"testing"
)

func TestApprovalsRoundTrip(t *testing.T) {
	want := map[string][]string{
		"host.example/a": {`make "x" {{ task }}`, "second"},
		"other":          {"third"},
	}
	data, err := EncodeApprovals(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeApprovals(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestApprovalsEdges(t *testing.T) {
	if got, err := DecodeApprovals(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for name, text := range map[string]string{
		"broken":      "[[[",
		"no list":     "[approvals.a]\nother = 1\n",
		"scalar list": "[approvals.a]\ntemplates = 1\n",
		"bad item":    "[approvals.a]\ntemplates = [1]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeApprovals([]byte(text)); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestDescriptorSetNamesTheContract(t *testing.T) {
	data, err := DescriptorSet()
	if err != nil || len(data) == 0 {
		t.Fatalf("%d bytes, err %v", len(data), err)
	}
}
