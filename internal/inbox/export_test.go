package inbox

// SetBeforeOpen sets the hook ReadBounded runs just before it opens its
// file, for the external tests; nil removes it.
func SetBeforeOpen(f func(p string)) { beforeOpen = f }
