package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrCanceled is returned when line input ends before the form is finished.
var ErrCanceled = errors.New("tui: canceled")

// Prompter asks questions one line at a time, with no cursor movement and no
// styling, for screen readers and dumb terminals.
type Prompter struct {
	r *bufio.Reader
	w io.Writer
}

// NewPrompter reads answers from r and writes prompts to w.
func NewPrompter(r io.Reader, w io.Writer) *Prompter {
	return &Prompter{r: bufio.NewReader(r), w: w}
}

// Say writes one line.
func (p *Prompter) Say(text string) error {
	_, err := fmt.Fprintln(p.w, Clean(text))
	return err
}

// Line asks a question and returns the answer without its line ending.
func (p *Prompter) Line(prompt string) (string, error) {
	if _, err := fmt.Fprint(p.w, Clean(prompt)+": "); err != nil {
		return "", err
	}
	return p.read()
}

// Block asks for several lines, ended by a line holding only a period.
func (p *Prompter) Block(prompt string) (string, error) {
	if err := p.Say(prompt + " (end with a line holding only a period)"); err != nil {
		return "", err
	}
	var lines []string
	for {
		line, err := p.read()
		if err != nil {
			return "", err
		}
		if line == "." {
			return strings.Join(lines, "\n"), nil
		}
		lines = append(lines, line)
	}
}

// read returns one line. A last line with no newline still counts; after that,
// the end of input means the person left, so it reads as a cancel.
func (p *Prompter) read() (string, error) {
	line, err := p.r.ReadString('\n')
	if err != nil && line == "" {
		if errors.Is(err, io.EOF) {
			return "", ErrCanceled
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
