package tree

import (
	"math"
	"reflect"
	"testing"
)

func TestTableKeepsInsertionOrder(t *testing.T) {
	table := NewTable()
	table.Set("b", NewInt(1))
	table.Set("a", NewInt(2))
	table.Set("b", NewInt(3))

	if got, want := table.Keys(), []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	if table.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", table.Len())
	}
	if v, ok := table.Get("b"); !ok || v.Int != 3 {
		t.Fatalf("Get(b) = %v, %v; want the replaced value", v, ok)
	}
	if _, ok := table.Get("missing"); ok {
		t.Fatal("Get(missing) reported a value")
	}
}

func TestDeleteKeepsTheOtherKeysFindable(t *testing.T) {
	table := NewTable()
	for i, k := range []string{"a", "b", "c"} {
		table.Set(k, NewInt(int64(i)))
	}
	if !table.Delete("b") {
		t.Fatal("Delete(b) = false")
	}
	if table.Delete("b") {
		t.Fatal("second Delete(b) = true")
	}
	if got, want := table.Keys(), []string{"a", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	if v, ok := table.Get("c"); !ok || v.Int != 2 {
		t.Fatalf("Get(c) after Delete = %v, %v", v, ok)
	}
}

func TestLookupWalksTablesOnly(t *testing.T) {
	inner := NewTable()
	inner.Set("leaf", NewBool(true))
	root := NewTable()
	root.Set("inner", inner)
	root.Set("scalar", NewString("x"))

	if v, ok := root.Lookup("inner", "leaf"); !ok || !v.Bool {
		t.Fatalf("Lookup(inner, leaf) = %v, %v", v, ok)
	}
	if v, ok := root.Lookup(); !ok || v != root {
		t.Fatalf("Lookup() = %v, %v; want the root", v, ok)
	}
	if _, ok := root.Lookup("scalar", "deeper"); ok {
		t.Fatal("Lookup walked through a scalar")
	}
	if _, ok := root.Lookup("nope", "leaf"); ok {
		t.Fatal("Lookup found a missing key")
	}
}

func TestGetOnAScalarFindsNothing(t *testing.T) {
	if _, ok := NewInt(1).Get("a"); ok {
		t.Fatal("Get on a scalar reported a value")
	}
}

func TestEqual(t *testing.T) {
	tableOf := func(pairs ...any) *Node {
		table := NewTable()
		for i := 0; i < len(pairs); i += 2 {
			table.Set(pairs[i].(string), pairs[i+1].(*Node))
		}
		return table
	}
	cases := []struct {
		name string
		a, b *Node
		want bool
	}{
		{"null", NewNull(), NewNull(), true},
		{"string", NewString("a"), NewString("a"), true},
		{"string differs", NewString("a"), NewString("b"), false},
		{"integer", NewInt(1), NewInt(1), true},
		{"integer differs", NewInt(1), NewInt(2), false},
		{"bool", NewBool(true), NewBool(true), true},
		{"bool differs", NewBool(true), NewBool(false), false},
		{"float", NewFloat(1.5), NewFloat(1.5), true},
		{"float differs", NewFloat(1.5), NewFloat(2.5), false},
		{"NaN equals NaN", NewFloat(math.NaN()), NewFloat(math.NaN()), true},
		{"NaN against a number", NewFloat(math.NaN()), NewFloat(1), false},
		{"signed zeros differ", NewFloat(0), NewFloat(math.Copysign(0, -1)), false},
		{"kinds differ", NewString("1"), NewInt(1), false},
		{"datetime", NewDatetime(LocalDate, "2020-01-02"), NewDatetime(LocalDate, "2020-01-02"), true},
		{"datetime differs", NewDatetime(LocalDate, "2020-01-02"), NewDatetime(LocalDate, "2020-01-03"), false},
		{"array", NewArray(NewInt(1), NewInt(2)), NewArray(NewInt(1), NewInt(2)), true},
		{"array order matters", NewArray(NewInt(1), NewInt(2)), NewArray(NewInt(2), NewInt(1)), false},
		{"array length differs", NewArray(NewInt(1)), NewArray(NewInt(1), NewInt(2)), false},
		{"table order ignored", tableOf("a", NewInt(1), "b", NewInt(2)), tableOf("b", NewInt(2), "a", NewInt(1)), true},
		{"table value differs", tableOf("a", NewInt(1)), tableOf("a", NewInt(2)), false},
		{"table key differs", tableOf("a", NewInt(1)), tableOf("b", NewInt(1)), false},
		{"table size differs", tableOf("a", NewInt(1)), tableOf(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Equal(c.a, c.b); got != c.want {
				t.Fatalf("Equal = %v, want %v", got, c.want)
			}
		})
	}
}

func TestKindStringNamesEveryKind(t *testing.T) {
	want := map[Kind]string{
		Null: "null", String: "string", Integer: "integer", Float: "float", Bool: "bool",
		OffsetDateTime: "offset date-time", LocalDateTime: "local date-time",
		LocalDate: "local date", LocalTime: "local time", Array: "array", Table: "table",
		Kind(200): "unknown",
	}
	for kind, name := range want {
		if got := kind.String(); got != name {
			t.Errorf("Kind(%d).String() = %q, want %q", kind, got, name)
		}
	}
}
