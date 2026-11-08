package commitmsg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	// DiffBudget is the most of a diff a prompt carries, in bytes.
	DiffBudget = 100 * 1024
	statBudget = 8 * 1024

	defaultTimeout   = 60 * time.Second
	defaultMaxOutput = 64 * 1024
	attempts         = 2
	killGrace        = 2 * time.Second
)

// lockFiles are dropped from prompts: they are long and say nothing about why.
var lockFiles = []string{
	"bun.lock", "bun.lockb", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml",
	"go.sum", "Cargo.lock", "Gemfile.lock", "poetry.lock", "composer.lock", "uv.lock", "flake.lock",
}

// harnesses are tried in order when the command is "auto". Each reads the
// prompt on stdin and prints its answer.
var harnesses = []struct{ program, command string }{
	{"claude", "claude -p"},
	{"codex", "codex exec"},
	{"opencode", "opencode run"},
}

// ErrNoGenerator means that no command is configured and no harness is installed.
var ErrNoGenerator = errors.New("no commit message generator is installed")

// UnparsableError means that the generator answered twice with text that is
// not a valid message.
type UnparsableError struct {
	Last error
}

func (e *UnparsableError) Error() string {
	return fmt.Sprintf("the generator's answer was refused after one retry: %v", e.Last)
}

func (e *UnparsableError) Unwrap() error { return e.Last }

// IsLockFile reports whether a path names a dependency lock file.
func IsLockFile(file string) bool {
	return slices.Contains(lockFiles, path.Base(file))
}

// Input is what the prompt is built from.
type Input struct {
	Branch   string
	Target   string // The branch being squashed onto; may be empty.
	Diff     string // Without lock files.
	Stat     string
	Subjects []string
	Append   string // The project's own instruction.
	Pin      Pin
}

func capText(text string, limit int, note string) string {
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	if index := strings.LastIndexByte(text, '\n'); index >= 0 {
		text = text[:index+1]
	}
	return text + note + "\n"
}

// Prompt builds the text sent to the generator.
func Prompt(in Input) string {
	var out strings.Builder
	out.WriteString("Write the git commit message for the change below. Reply with the message only.\n\n")
	out.WriteString("Format: a Conventional Commits header; after it a blank line and a paragraph giving the reason for the change.\n")
	out.WriteString("Allowed types: " + strings.Join(Types, ", ") + ". The scope is optional.\n")
	fmt.Fprintf(&out, "Start the subject with a lowercase verb in the command form, end it without a period, and keep the header within %d characters.\n", MaxHeader)
	if in.Pin.Type != "" {
		out.WriteString("Use the type " + in.Pin.Type + ".\n")
	}
	if in.Pin.Scope != "" {
		out.WriteString("Use the scope " + in.Pin.Scope + ".\n")
	}
	if in.Append != "" {
		out.WriteString(in.Append + "\n")
	}
	out.WriteString("\nBranch: " + in.Branch + "\n")
	if in.Target != "" {
		out.WriteString("All of the branch's commits are being squashed onto: " + in.Target + "\n")
	}
	if len(in.Subjects) > 0 {
		out.WriteString("\nRecent subjects, for style:\n" + strings.Join(in.Subjects, "\n") + "\n")
	}
	out.WriteString("\nDiffstat:\n" + capText(in.Stat, statBudget, "[diffstat cut here]"))
	out.WriteString("\nDiff:\n" + capText(in.Diff, DiffBudget, "[diff cut here]"))
	return out.String()
}

// Generator runs a command that reads a prompt on stdin and prints a message.
type Generator struct {
	Command   string // "" or "auto" picks the first installed harness.
	Dir       string
	Timeout   time.Duration // Per attempt; zero means one minute.
	MaxOutput int           // Bytes per attempt; zero means 64 KB.
}

// Resolve returns the command that would run.
func (g *Generator) Resolve() (string, error) {
	if g.Command != "" && g.Command != "auto" {
		return g.Command, nil
	}
	for _, harness := range harnesses {
		if _, err := exec.LookPath(harness.program); err == nil {
			return harness.command, nil
		}
	}
	return "", ErrNoGenerator
}

// Generate asks for a message, with one retry. It returns ErrNoGenerator, an
// *UnparsableError when the second answer also fails to parse, or the last
// failure to run; the caller picks the fallback for the failures to run.
func (g *Generator) Generate(ctx context.Context, prompt string, pin Pin) (Message, error) {
	command, err := g.Resolve()
	if err != nil {
		return Message{}, err
	}
	request := prompt
	var last error
	for range attempts {
		out, runErr := g.run(ctx, command, request)
		if runErr != nil {
			last = runErr
			if ctx.Err() != nil {
				break
			}
			continue
		}
		message, parseErr := parseReply(out, pin)
		if parseErr == nil {
			return message, nil
		}
		last = &UnparsableError{Last: parseErr}
		request = prompt + "\nYour previous answer was refused: " + parseErr.Error() + "\n"
	}
	return Message{}, last
}

// parseReply removes the wrapping models tend to add, then applies the pin.
func parseReply(out string, pin Pin) (Message, error) {
	text := strings.TrimSpace(out)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		_, inner, _ := strings.Cut(text, "\n")
		text = strings.TrimSpace(strings.TrimSuffix(inner, "```"))
	}
	message, err := Parse(text)
	if err != nil {
		return Message{}, err
	}
	message = pin.apply(message)
	return Parse(message.String())
}

// boundedBuffer keeps what fits and remembers that more arrived.
type boundedBuffer struct {
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.data.Len(); len(p) > room {
		b.overflow = true
		b.data.Write(p[:room])
		return len(p), nil
	}
	return b.data.Write(p)
}

func (g *Generator) run(ctx context.Context, command, prompt string) (string, error) {
	timeout, limit := g.Timeout, g.MaxOutput
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if limit <= 0 {
		limit = defaultMaxOutput
	}
	limited, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(limited, "/bin/sh", "-c", command)
	output := &boundedBuffer{limit: limit}
	cmd.Dir, cmd.Stdin, cmd.Stdout = g.Dir, strings.NewReader(prompt), output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = killGrace
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	err := cmd.Run()
	switch {
	case limited.Err() != nil:
		return "", fmt.Errorf("the generator did not finish within %s", timeout)
	case err != nil:
		return "", fmt.Errorf("the generator failed: %w", err)
	case output.overflow:
		return "", fmt.Errorf("the generator printed more than %d bytes", limit)
	}
	return output.data.String(), nil
}
