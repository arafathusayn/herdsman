//go:build !race

package web

// raceSlack is zero without the race detector: allocation counts are exact.
const raceSlack = 0
