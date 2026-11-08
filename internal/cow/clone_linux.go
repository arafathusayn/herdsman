package cow

import (
	"errors"

	"github.com/arafathusayn/herdsman/internal/sys"
)

// platformClone uses the FICLONE ioctl, which btrfs and XFS implement.
func platformClone(src, dst string) error {
	err := sys.CloneFile(src, dst)
	if errors.Is(err, sys.ErrCloneUnsupported) {
		return errNoClone
	}
	return err
}
