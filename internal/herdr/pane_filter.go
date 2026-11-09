package herdr

import "sync/atomic"

var paneFilter atomic.Pointer[func(string) string]

// SetPaneFilter makes every pane read in this process pass through f; nil
// removes it. Filtering where the text enters the process covers consumers
// that store it as well as those that print it.
func SetPaneFilter(f func(string) string) {
	if f == nil {
		paneFilter.Store(nil)
		return
	}
	paneFilter.Store(&f)
}

func filterPane(text string) string {
	if f := paneFilter.Load(); f != nil {
		return (*f)(text)
	}
	return text
}
