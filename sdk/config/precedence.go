package config

import (
	"strings"

	"github.com/arafathusayn/herdsman/sdk/internal/encoding/tree"
)

// replacesLists names the sections whose arrays are one unit: joining two
// argument vectors would run a command nobody wrote.
var replacesLists = map[string]bool{"launch": true}

// merge lays over on top of base, recording who set each value. A scalar
// replaces, tables merge key by key, and arrays join, except a default array,
// which a file replaces, so that a file's list is not padded with defaults.
func merge(base, over *tree.Node, prefix, source string, sources map[string]string) {
	for _, key := range over.Keys() {
		value, _ := over.Get(key)
		path := joinKey(prefix, key)
		held, ok := base.Get(key)
		switch {
		case ok && held.Kind == tree.Table && value.Kind == tree.Table:
			merge(held, value, path, source, sources)
		case ok && held.Kind == tree.Array && value.Kind == tree.Array && joinsLists(path, sources):
			held.Items = append(held.Items, value.Items...)
			sources[path] = source
		default:
			clearSources(sources, path)
			base.Set(key, clone(value))
			recordSources(value, path, source, sources)
		}
	}
}

func joinsLists(path string, sources map[string]string) bool {
	section, _, _ := strings.Cut(path, ".")
	return !replacesLists[section] && sources[path] != SourceDefault
}

// clearSources forgets everything at or under path, whose value is replaced.
func clearSources(sources map[string]string, path string) {
	delete(sources, path)
	for key := range sources {
		if strings.HasPrefix(key, path+".") {
			delete(sources, key)
		}
	}
}

func recordSources(value *tree.Node, path, source string, sources map[string]string) {
	if value.Kind != tree.Table {
		sources[path] = source
		return
	}
	for _, key := range value.Keys() {
		item, _ := value.Get(key)
		recordSources(item, joinKey(path, key), source, sources)
	}
}

// clone copies a tree, so that merging never writes into a layer.
func clone(in *tree.Node) *tree.Node {
	switch in.Kind {
	case tree.Table:
		out := tree.NewTable()
		for _, key := range in.Keys() {
			item, _ := in.Get(key)
			out.Set(key, clone(item))
		}
		return out
	case tree.Array:
		out := tree.NewArray()
		for _, item := range in.Items {
			out.Items = append(out.Items, clone(item))
		}
		return out
	default:
		copied := *in
		return &copied
	}
}
