package doctor

// niceOf turns getpriority's result into a nice value: macOS returns it as is.
func niceOf(prio int) int { return prio }
