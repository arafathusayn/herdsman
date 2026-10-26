package message

// child is a nested run on the same channel, as the scripts' subshells: its
// own entries in the parent's owner (its releaseAll frees only them), so
// the parent's signal cleanup frees every lock it holds (review 3, 4).
func (e *Env) child() *Env {
	return &Env{Getenv: e.Getenv, Stdout: e.Stdout, Stderr: e.Stderr, Client: e.Client, Now: e.Now, Sleep: e.Sleep,
		Events: e.Events, Executable: e.Executable, Ctx: e.Ctx, ch: e.ch, own: e.owner()}
}
