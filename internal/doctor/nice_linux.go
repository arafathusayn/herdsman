package doctor

// niceOf turns the raw getpriority result on Linux (20 - nice) into a nice value.
func niceOf(prio int) int { return 20 - prio }
