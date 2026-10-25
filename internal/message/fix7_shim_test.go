package message

const queuedMax = 16

func ownedNow(e *Env) bool { return e.ch.MutationHeld }
