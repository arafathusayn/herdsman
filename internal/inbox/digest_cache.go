package inbox

import (
	"os"
	"sync"
	"time"
)

// fileStamp identifies one version of a file: the same inode with the same
// size, modification time and change time has the same bytes. The change
// time cannot be set back by a writer, so a rewrite always changes it.
type fileStamp struct {
	dev, ino     uint64
	size         int64
	mtime, ctime time.Time
	digest       string
}

var digestCache = struct {
	sync.Mutex
	m map[string]fileStamp
}{m: map[string]fileStamp{}}

// digestCacheMax bounds the cache: the proof names a few files.
const digestCacheMax = 64

// DigestFileCached is DigestFileFast, remembered per file version, so a
// long-running command that checks the setup proof every pass reads the
// herdsman executable once, not once a pass (A24). A file whose version
// cannot be told (no inode or change time) is read every time.
func DigestFileCached(p string) (string, error) {
	before, ok := stamp(p)
	if !ok {
		return DigestFileFast(p)
	}
	digestCache.Lock()
	c, hit := digestCache.m[p]
	digestCache.Unlock()
	if hit && sameVersion(c, before) {
		return c.digest, nil
	}
	sum, err := DigestFileFast(p)
	if err != nil {
		return "", err
	}
	after, ok := stamp(p)
	if !ok || !sameVersion(before, after) {
		return sum, nil // changed while read: do not remember it
	}
	after.digest = sum
	digestCache.Lock()
	if len(digestCache.m) >= digestCacheMax {
		digestCache.m = map[string]fileStamp{}
	}
	digestCache.m[p] = after
	digestCache.Unlock()
	return sum, nil
}

func sameVersion(a, b fileStamp) bool {
	return a.dev == b.dev && a.ino == b.ino && a.size == b.size && a.mtime.Equal(b.mtime) && a.ctime.Equal(b.ctime)
}

func stamp(p string) (fileStamp, bool) {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return fileStamp{}, false
	}
	dev, ino, ctime, ok := fileIdentity(fi)
	if !ok {
		return fileStamp{}, false
	}
	return fileStamp{dev: dev, ino: ino, size: fi.Size(), mtime: fi.ModTime(), ctime: ctime}, true
}
