package cow

import (
	"errors"
	"time"

	"github.com/arafathusayn/herdsman/internal/sys"
)

// platformClone uses one clonefile call per regular file.
func platformClone(src, dst string) error {
	err := sys.CloneFile(src, dst)
	if errors.Is(err, sys.ErrCloneUnsupported) {
		return errNoClone
	}
	return err
}

func setLinkTime(path string, mtime time.Time) error {
	return sys.SetLinkTime(path, mtime)
}
