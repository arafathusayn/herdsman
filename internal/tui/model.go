// Package tui is herdsman's terminal layer: a model-update-view loop, a key
// parser, and a renderer that repaints only the lines that changed.
//
// A model is a value. Update returns the next model and an optional command,
// and View returns the frame. Nothing here depends on a third-party library.
package tui

// Msg is anything a model reacts to: a Key, a Paste, a Resize, or the value a
// command returned.
type Msg any

// Cmd is work done away from the update loop. Its result is the next message.
type Cmd func() Msg

// Model is the state of one interactive screen.
type Model interface {
	Update(Msg) (Model, Cmd)
	View() string
}

// Resize reports the terminal's size in cells. A model receives one before its
// first frame and one after each change.
type Resize struct {
	Cols, Rows int
}

type quitMsg struct{}

// Quit is a command that ends the program after the current frame.
func Quit() Msg { return quitMsg{} }

type batchMsg []Cmd

// Batch runs several commands at once. Nil commands are dropped, and with none
// left the result is nil.
func Batch(cmds ...Cmd) Cmd {
	kept := make(batchMsg, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd != nil {
			kept = append(kept, cmd)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return func() Msg { return kept }
}
