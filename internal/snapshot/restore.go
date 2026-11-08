package snapshot

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Restore verifies the snapshot, rebuilds the worktree in a temporary folder
// beside target and renames it into place, so target either appears whole or
// not at all. Target must be absent or an empty folder.
func Restore(ctx context.Context, dir, target string) error {
	if err := absPaths(&dir, &target); err != nil {
		return err
	}
	m, err := Verify(ctx, dir)
	if err != nil {
		return err
	}
	if err := targetFree(target); err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".herdsman-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := populate(ctx, dir, tmp, m); err != nil {
		return err
	}
	if err := os.Chmod(tmp, fs.FileMode(m.WorktreeMode&permMask)); err != nil {
		return err
	}
	// Rmdir, not Remove: an empty folder may be replaced, a file never. Some
	// systems refuse to rename over even an empty folder.
	if err := syscall.Rmdir(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(tmp, target)
}

func targetFree(target string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a folder", ErrTargetOccupied, target)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %s is not empty", ErrTargetOccupied, target)
	}
	return nil
}

func populate(ctx context.Context, dir, tmp string, m *Manifest) error {
	if _, err := runGit(ctx, tmp, nil, nil, "init", "-q", "--template=", "--object-format="+m.ObjectFormat); err != nil {
		return err
	}
	if err := fetchBundle(ctx, tmp, filepath.Join(dir, BundleName)); err != nil {
		return err
	}
	if err := setHead(ctx, tmp, m); err != nil {
		return err
	}
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := writeWorkingTree(ctx, root, tmp, m.WorkingTree); err != nil {
		return err
	}
	if err := unpackPreserved(root, filepath.Join(dir, PreservedName)); err != nil {
		return err
	}
	if _, err := runGit(ctx, tmp, nil, nil, "read-tree", m.StagedTree); err != nil {
		return err
	}
	return applyMetadata(root, m)
}

func setHead(ctx context.Context, repo string, m *Manifest) error {
	if m.Branch == "" {
		_, err := runGit(ctx, repo, nil, nil, "update-ref", "--no-deref", "HEAD", m.Head)
		return err
	}
	if _, err := runGit(ctx, repo, nil, nil, "symbolic-ref", "HEAD", m.Branch); err != nil {
		return err
	}
	if m.Head == "" {
		return nil
	}
	_, err := runGit(ctx, repo, nil, nil, "update-ref", m.Branch, m.Head)
	return err
}

// writeWorkingTree writes blobs itself, because `git checkout` would run
// smudge filters and line-ending conversion over the exact bytes captured.
func writeWorkingTree(ctx context.Context, root *os.Root, repo, tree string) error {
	rows, err := listTree(ctx, repo, "-r", "-z", "--full-tree", tree)
	if err != nil {
		return err
	}
	var blobs []treeRow
	var request strings.Builder
	for _, row := range rows {
		if row.kind == "commit" {
			if err := makeSubmoduleFolder(root, row.path); err != nil {
				return err
			}
			continue
		}
		blobs = append(blobs, row)
		request.WriteString(row.oid + "\n")
	}
	cmd := gitCommand(ctx, repo, nil, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(request.String())
	reader, writer := io.Pipe()
	defer reader.Close()
	cmd.Stdout = writer
	go func() { writer.CloseWithError(cmd.Run()) }()
	stream := bufio.NewReader(reader)
	for _, row := range blobs {
		if err := writeBlob(root, stream, row); err != nil {
			return fmt.Errorf("restoring %q: %w", row.path, err)
		}
	}
	return nil
}

func makeSubmoduleFolder(root *os.Root, name string) error {
	if err := prepareParents(root, name); err != nil {
		return err
	}
	return root.Mkdir(name, 0o755)
}

// writeBlob consumes one `cat-file --batch` reply: a header, the content and
// a newline.
func writeBlob(root *os.Root, stream *bufio.Reader, row treeRow) error {
	header, err := stream.ReadString('\n')
	if err != nil {
		return err
	}
	var oid, kind string
	var size int64
	if _, err := fmt.Sscanf(header, "%s %s %d", &oid, &kind, &size); err != nil || kind != "blob" {
		return fmt.Errorf("unexpected object header %q", strings.TrimSpace(header))
	}
	if err := prepareParents(root, row.path); err != nil {
		return err
	}
	content := io.LimitReader(stream, size)
	if row.mode == "120000" {
		target, err := io.ReadAll(content)
		if err != nil {
			return err
		}
		if err := root.Symlink(string(target), row.path); err != nil {
			return err
		}
	} else if err := writeNew(root, row.path, content, 0o644); err != nil {
		return err
	}
	_, err = stream.Discard(1)
	return err
}

// applyMetadata runs last, since writing a file would reset its mtime. A
// symlink has neither: os.Root has no call that leaves the link unfollowed.
func applyMetadata(root *os.Root, m *Manifest) error {
	for _, f := range m.Files {
		if f.Mode == modeSymlink {
			continue
		}
		name := string(f.Path)
		if f.Perm != 0 {
			if err := root.Chmod(name, fs.FileMode(f.Perm&permMask)); err != nil {
				return err
			}
		}
		when := time.Unix(0, f.MTimeNS)
		if err := root.Chtimes(name, when, when); err != nil {
			return err
		}
	}
	return nil
}
