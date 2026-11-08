package cow

import (
	"errors"

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
