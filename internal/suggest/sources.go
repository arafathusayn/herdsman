package suggest

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// MaxSourceEntries is the most entries one source may offer.
const MaxSourceEntries = 1024

// SourceKind says where an entry came from.
type SourceKind string

// The kinds of source, highest precedence first.
const (
	SourceProject SourceKind = "project"
	SourceUser    SourceKind = "user"
	SourceRemote  SourceKind = "remote"
	SourceBundled SourceKind = "bundled"
)

func (k SourceKind) rank() int {
	switch k {
	case SourceProject:
		return 0
	case SourceUser:
		return 1
	case SourceRemote:
		return 2
	case SourceBundled:
		return 3
	}
	return 4
}

// SourceRef names a source. Every entry carries the one it came from.
type SourceRef struct {
	Kind   SourceKind `json:"kind"`
	Name   string     `json:"name"`
	Commit string     `json:"commit,omitempty"`
}

// Problem is a file or source that could not be used. It never stops the other
// entries from loading, so one bad remote cannot take the catalog down.
type Problem struct {
	Source SourceRef `json:"source"`
	File   string    `json:"file,omitempty"`
	Err    string    `json:"error"`
}

// Shadow records an entry hidden by one of the same id from a stronger source.
type Shadow struct {
	ID     string    `json:"id"`
	Winner SourceRef `json:"winner"`
	Hidden SourceRef `json:"hidden"`
}

// Catalog is the merged view of the sources, with entries sorted by id.
type Catalog struct {
	Entries  []Entry   `json:"entries"`
	Shadowed []Shadow  `json:"shadowed,omitempty"`
	Problems []Problem `json:"problems,omitempty"`
}

type file struct {
	name string
	data []byte
}

// Source is one place entries are read from.
type Source struct {
	Ref  SourceRef
	read func() ([]file, []Problem, error)
}

// DirSource reads the entries of a folder. A folder that does not exist is an
// empty source.
func DirSource(kind SourceKind, name, dir string) Source {
	return FSSource(kind, name, os.DirFS(dir))
}

// FSSource reads the entries at the root of a file system.
func FSSource(kind SourceKind, name string, fsys fs.FS) Source {
	ref := SourceRef{Kind: kind, Name: name}
	return Source{Ref: ref, read: func() ([]file, []Problem, error) { return readFS(ref, fsys) }}
}

func readFS(ref SourceRef, fsys fs.FS) ([]file, []Problem, error) {
	dirents, err := fs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var files []file
	var problems []Problem
	for _, d := range dirents {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			continue
		}
		if len(files) == MaxSourceEntries {
			problems = append(problems, tooMany(ref))
			break
		}
		// Only regular files: a symlink could pull a private file into the
		// catalog and a FIFO could hang the read.
		if !d.Type().IsRegular() {
			problems = append(problems, Problem{Source: ref, File: d.Name(), Err: "not a regular file"})
			continue
		}
		data, err := readLimited(fsys, d.Name())
		if err != nil {
			problems = append(problems, Problem{Source: ref, File: d.Name(), Err: err.Error()})
			continue
		}
		files = append(files, file{d.Name(), data})
	}
	return files, problems, nil
}

func tooMany(ref SourceRef) Problem {
	return Problem{Source: ref, Err: fmt.Sprintf("source has more than %d entries; the rest are ignored", MaxSourceEntries)}
}

// readLimited reads one byte past the limit, so ParseEntry can refuse the file
// without the whole of it reaching memory.
func readLimited(fsys fs.FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, MaxEntryBytes+1))
}

// parsed is a valid entry with the text it was read from.
type parsed struct {
	entry Entry
	text  string
}

func parseFiles(ref SourceRef, files []file) ([]parsed, []Problem) {
	var out []parsed
	var problems []Problem
	for _, f := range files {
		e, err := parseFile(f)
		if err != nil {
			problems = append(problems, Problem{Source: ref, File: f.name, Err: err.Error()})
			continue
		}
		e.Source = ref
		out = append(out, parsed{e, strings.ReplaceAll(string(f.data), "\r\n", "\n")})
	}
	return out, problems
}

func parseFile(f file) (Entry, error) {
	e, err := ParseEntry(f.data)
	if err != nil {
		return Entry{}, err
	}
	if err := Validate(e); err != nil {
		return Entry{}, err
	}
	if f.name != e.ID+".md" {
		return Entry{}, fmt.Errorf("file name must be %s.md", e.ID)
	}
	return e, nil
}

// Load merges the sources. Precedence comes from each source's kind, whatever
// the argument order; sources of one kind keep their order. Load reads local
// data only: it never touches the network.
func Load(sources ...Source) Catalog {
	ordered := slices.Clone(sources)
	slices.SortStableFunc(ordered, func(a, b Source) int { return cmp.Compare(a.Ref.Kind.rank(), b.Ref.Kind.rank()) })

	cat := Catalog{Entries: []Entry{}}
	winners := map[string]SourceRef{}
	for _, s := range ordered {
		files, problems, err := s.read()
		if err != nil {
			problems = append(problems, Problem{Source: s.Ref, Err: err.Error()})
		}
		entries, bad := parseFiles(s.Ref, files)
		cat.Problems = append(append(cat.Problems, problems...), bad...)
		for _, p := range entries {
			if winner, taken := winners[p.entry.ID]; taken {
				cat.Shadowed = append(cat.Shadowed, Shadow{ID: p.entry.ID, Winner: winner, Hidden: s.Ref})
				continue
			}
			winners[p.entry.ID] = s.Ref
			cat.Entries = append(cat.Entries, p.entry)
		}
	}
	slices.SortFunc(cat.Entries, func(a, b Entry) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortStableFunc(cat.Shadowed, func(a, b Shadow) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortStableFunc(cat.Problems, func(a, b Problem) int {
		return cmp.Or(cmp.Compare(a.Source.Kind.rank(), b.Source.Kind.rank()), cmp.Compare(a.File, b.File))
	})
	return cat
}
