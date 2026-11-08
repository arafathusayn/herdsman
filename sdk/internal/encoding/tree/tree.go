// Package tree is the generic value tree that the configuration decoders
// produce, so that one binding layer can read every text format.
package tree

import (
	"math"
	"slices"
)

// Kind says which field of a Node holds its value.
type Kind uint8

// The kinds of node. A date or time keeps its text as written in Str, so that
// no precision is lost and a format without that type can carry it as text.
const (
	Null Kind = iota
	String
	Integer
	Float
	Bool
	OffsetDateTime
	LocalDateTime
	LocalDate
	LocalTime
	Array
	Table
)

var kindNames = [...]string{
	Null: "null", String: "string", Integer: "integer", Float: "float", Bool: "bool",
	OffsetDateTime: "offset date-time", LocalDateTime: "local date-time",
	LocalDate: "local date", LocalTime: "local time", Array: "array", Table: "table",
}

// String returns the kind's name for error messages.
func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// Node is one value. Items holds an array's elements; a table's entries go
// through Get, Set, Delete and Keys, which keep insertion order.
type Node struct {
	Kind  Kind
	Str   string
	Int   int64
	Flt   float64
	Bool  bool
	Items []*Node

	keys   []string
	values map[string]*Node
}

// NewNull returns a null node.
func NewNull() *Node { return &Node{Kind: Null} }

// NewString returns a string node.
func NewString(s string) *Node { return &Node{Kind: String, Str: s} }

// NewInt returns an integer node.
func NewInt(i int64) *Node { return &Node{Kind: Integer, Int: i} }

// NewFloat returns a float node.
func NewFloat(f float64) *Node { return &Node{Kind: Float, Flt: f} }

// NewBool returns a boolean node.
func NewBool(b bool) *Node { return &Node{Kind: Bool, Bool: b} }

// NewDatetime returns a date or time node of the given kind with its text.
func NewDatetime(kind Kind, text string) *Node { return &Node{Kind: kind, Str: text} }

// NewArray returns an array node holding items.
func NewArray(items ...*Node) *Node { return &Node{Kind: Array, Items: items} }

// NewTable returns an empty table node.
func NewTable() *Node { return &Node{Kind: Table, values: map[string]*Node{}} }

// Get returns the value under key. A node that is not a table has no keys.
func (n *Node) Get(key string) (*Node, bool) {
	v, ok := n.values[key]
	return v, ok
}

// Set stores value under key. A new key goes last; an existing key keeps its
// place.
func (n *Node) Set(key string, value *Node) {
	if _, ok := n.values[key]; !ok {
		n.keys = append(n.keys, key)
	}
	n.values[key] = value
}

// Delete removes key and reports whether it was present.
func (n *Node) Delete(key string) bool {
	if _, ok := n.values[key]; !ok {
		return false
	}
	delete(n.values, key)
	n.keys = slices.Delete(n.keys, slices.Index(n.keys, key), slices.Index(n.keys, key)+1)
	return true
}

// Keys returns the table's keys in insertion order. The caller must not
// change the slice.
func (n *Node) Keys() []string { return n.keys }

// Len returns the number of keys in the table.
func (n *Node) Len() int { return len(n.keys) }

// Lookup walks path through nested tables. An empty path returns n.
func (n *Node) Lookup(path ...string) (*Node, bool) {
	for _, key := range path {
		next, ok := n.Get(key)
		if !ok {
			return nil, false
		}
		n = next
	}
	return n, true
}

// Equal reports whether two trees hold the same values. Array order matters;
// table order does not, because no supported format gives it a meaning. Two
// NaNs are equal so that a value compares equal to its own round trip, and
// the two zeros differ for the same reason.
func Equal(a, b *Node) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case Null:
		return true
	case Integer:
		return a.Int == b.Int
	case Float:
		if math.IsNaN(a.Flt) || math.IsNaN(b.Flt) {
			return math.IsNaN(a.Flt) && math.IsNaN(b.Flt)
		}
		return math.Float64bits(a.Flt) == math.Float64bits(b.Flt)
	case Bool:
		return a.Bool == b.Bool
	case Array:
		return slices.EqualFunc(a.Items, b.Items, Equal)
	case Table:
		if a.Len() != b.Len() {
			return false
		}
		for _, key := range a.keys {
			other, ok := b.Get(key)
			if !ok || !Equal(a.values[key], other) {
				return false
			}
		}
		return true
	default:
		return a.Str == b.Str
	}
}
