package snapshot

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	modeRegular = 0o100000
	modeDir     = 0o040000
	modeSymlink = 0o120000
	permMask    = 0o777
)

// safeRel accepts only a clean relative path that stays in its folder and
// out of the restored repository's .git.
func safeRel(name string) error {
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" || path.IsAbs(trimmed) || strings.ContainsRune(trimmed, 0) {
		return fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return fmt.Errorf("%w: %q", ErrUnsafePath, name)
		}
	}
	return nil
}

// recordFor describes a preserved path with its exact permission bits.
func recordFor(rel string, info fs.FileInfo) FileRecord {
	rec := FileRecord{Path: Path(rel), Size: info.Size(), MTimeNS: info.ModTime().UnixNano(), Source: SourcePreserved}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		rec.Mode = modeSymlink
	case info.IsDir():
		rec.Mode, rec.Size = modeDir|uint32(info.Mode().Perm()), 0
	default:
		rec.Mode = modeRegular | uint32(info.Mode().Perm())
	}
	return rec
}

func writePreserved(root, file string, records []FileRecord) (err error) {
	out, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	w := tar.NewWriter(out)
	for _, rec := range records {
		if err := addPreserved(w, root, rec); err != nil {
			return err
		}
	}
	return w.Close()
}

func addPreserved(w *tar.Writer, root string, rec FileRecord) error {
	full := filepath.Join(root, string(rec.Path))
	hdr := &tar.Header{Name: string(rec.Path), Mode: int64(rec.Mode & permMask), Format: tar.FormatGNU}
	switch rec.Mode &^ permMask {
	case modeSymlink:
		target, err := os.Readlink(full)
		if err != nil {
			return err
		}
		hdr.Typeflag, hdr.Linkname, hdr.Mode = tar.TypeSymlink, target, 0o777
		return w.WriteHeader(hdr)
	case modeDir:
		hdr.Typeflag, hdr.Name = tar.TypeDir, hdr.Name+"/"
		return w.WriteHeader(hdr)
	}
	hdr.Typeflag, hdr.Size = tar.TypeReg, rec.Size
	if err := w.WriteHeader(hdr); err != nil {
		return err
	}
	in, err := osOpen(full)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(w, in)
	return err
}

// headerMode is the manifest mode of a tar entry; ok is false for any entry
// type a snapshot never writes, such as a hard link or a device.
func headerMode(hdr *tar.Header) (uint32, bool) {
	switch hdr.Typeflag {
	case tar.TypeReg:
		return modeRegular | uint32(hdr.Mode&permMask), true
	case tar.TypeDir:
		return modeDir | uint32(hdr.Mode&permMask), true
	case tar.TypeSymlink:
		return modeSymlink, true
	}
	return 0, false
}

// checkPreserved reads every header and proves the archive is exactly what
// the manifest lists, with no entry that could leave the restore folder.
func checkPreserved(file string, m *Manifest) error {
	want := map[string]uint32{}
	for _, f := range m.Files {
		if f.Source == SourcePreserved {
			want[string(f.Path)] = f.Mode
		}
	}
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	seen := map[string]bool{}
	r := tar.NewReader(in)
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := checkEntry(hdr, want, seen); err != nil {
			return err
		}
	}
	if len(seen) != len(want) {
		return fmt.Errorf("archive holds %d entries, manifest lists %d", len(seen), len(want))
	}
	return nil
}

func checkEntry(hdr *tar.Header, want map[string]uint32, seen map[string]bool) error {
	if err := safeRel(hdr.Name); err != nil {
		return err
	}
	name := strings.TrimSuffix(hdr.Name, "/")
	mode, known := headerMode(hdr)
	if !known {
		return fmt.Errorf("entry %q has forbidden type %q", name, hdr.Typeflag)
	}
	if seen[name] {
		return fmt.Errorf("entry %q appears twice", name)
	}
	if listed, ok := want[name]; !ok || listed != mode {
		return fmt.Errorf("entry %q (mode %o) does not match the manifest", name, mode)
	}
	seen[name] = true
	return nil
}

// checkLayout proves the tree and the archive can be restored together: no
// path is stored twice, and no path lies beneath another stored path, which
// is either a file, a symlink that would carry the write elsewhere, or a
// folder the restore creates as a leaf. Take never produces either. Its paths
// were already proved safe, so walking up from each one ends at ".".
func checkLayout(m *Manifest, gitlinks []string) error {
	occupied := map[string]bool{}
	claim := func(name string) error {
		if occupied[name] {
			return fmt.Errorf("%w: %q is stored twice", ErrUnsafePath, name)
		}
		occupied[name] = true
		return nil
	}
	for _, f := range m.Files {
		if err := claim(string(f.Path)); err != nil {
			return err
		}
	}
	for _, link := range gitlinks {
		if err := safeRel(link); err != nil {
			return err
		}
		if err := claim(link); err != nil {
			return err
		}
	}
	for name := range occupied {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if occupied[dir] {
				return fmt.Errorf("%w: %q goes through %q", ErrUnsafePath, name, dir)
			}
		}
	}
	return nil
}

// unpackPreserved writes the archive through root. It runs after the tree
// files are on disk, so unpackEntry's check also covers their symlinks.
func unpackPreserved(root *os.Root, file string) error {
	in, err := osOpen(file)
	if err != nil {
		return err
	}
	defer in.Close()
	r := tar.NewReader(in)
	for {
		hdr, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := unpackEntry(root, hdr, r); err != nil {
			return err
		}
	}
}

func unpackEntry(root *os.Root, hdr *tar.Header, r io.Reader) error {
	name := strings.TrimSuffix(hdr.Name, "/")
	if err := prepareParents(root, name); err != nil {
		return err
	}
	perm := fs.FileMode(hdr.Mode & permMask)
	switch hdr.Typeflag {
	case tar.TypeSymlink:
		return root.Symlink(hdr.Linkname, name)
	case tar.TypeDir:
		if err := root.Mkdir(name, 0o700); err != nil {
			return err
		}
		return root.Chmod(name, perm)
	}
	if err := writeNew(root, name, r, 0o600); err != nil {
		return err
	}
	return root.Chmod(name, perm)
}

// prepareParents refuses a path under a symlink on the real filesystem, then
// creates the missing folders. Verification has checked the layout by name;
// this also holds where a case-insensitive filesystem merges two names.
func prepareParents(root *os.Root, name string) error {
	if err := safeRel(name); err != nil {
		return err
	}
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts); i++ {
		parent := strings.Join(parts[:i], "/")
		info, err := root.Lstat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			return root.MkdirAll(path.Dir(name), 0o755)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: %q goes through %q", ErrUnsafePath, name, parent)
		}
	}
	return nil
}

// writeNew creates a file that must not exist yet: nothing is overwritten.
func writeNew(root *os.Root, name string, r io.Reader, perm fs.FileMode) (err error) {
	out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	_, err = io.Copy(out, r)
	return err
}
