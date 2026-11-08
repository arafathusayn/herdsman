package snapshot

// DefaultFileCap is the `[snapshot] file_cap` default, 1 GiB.
const DefaultFileCap int64 = 1 << 30

// overCap reports whether a file of size bytes is too large to capture. A
// file of exactly the cap is captured.
func overCap(size, limit int64) bool {
	return size > limit
}
