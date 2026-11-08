package suggest

import (
	"errors"
	"fmt"
	"io/fs"

	bundledcatalog "github.com/arafathusayn/herdsman/catalog/suggest"
)

// Bundled is the catalog compiled into the binary from catalog/suggest/*.md.
func Bundled() Source { return FSSource(SourceBundled, "bundled", bundledcatalog.Entries) }

// ValidateBundled fails when any bundled entry is invalid. A test runs it, so
// a bad entry fails the build's check rather than a user's `herdsman suggest`.
func ValidateBundled() error { return validateFS(bundledcatalog.Entries) }

func validateFS(fsys fs.FS) error {
	cat := Load(FSSource(SourceBundled, "bundled", fsys))
	var errs []error
	for _, p := range cat.Problems {
		errs = append(errs, fmt.Errorf("%s: %s", p.File, p.Err))
	}
	if len(errs) == 0 && len(cat.Entries) == 0 {
		return errors.New("the bundled catalog has no entries")
	}
	return errors.Join(errs...)
}
