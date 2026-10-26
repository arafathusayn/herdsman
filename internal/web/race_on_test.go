//go:build race

package web

// raceSlack is the allocation the race detector's own bookkeeping adds to
// a measured refresh at random: up to about 160 KiB in a run, against a
// flat cost without it. A regression that grows with history costs more
// than a MiB at 10,000 lines, so the bound still finds it.
const raceSlack = 192 << 10
