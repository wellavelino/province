// Package run executes local processes; every remote action goes through it
// as an ssh invocation.
package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Cmd is one process invocation. A nil Env inherits the parent environment.
type Cmd struct {
	Name   string
	Args   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Argv returns Name followed by Args.
func (c Cmd) Argv() []string { return append([]string{c.Name}, c.Args...) }

// String renders the argv for messages.
func (c Cmd) String() string { return strings.Join(c.Argv(), " ") }

// Runner runs commands.
type Runner interface {
	// Run runs c; a non-zero exit is returned as *ExitError.
	Run(ctx context.Context, c Cmd) error
	// Pipe runs src and dst with src's stdout connected to dst's stdin.
	// It returns an error if either fails.
	Pipe(ctx context.Context, src, dst Cmd) error
}

// ExitError is a process that exited non-zero.
type ExitError struct {
	Cmd    string
	Code   int
	Stderr string // tail of the process's stderr
	// Guest: the command ran inside an env, so Stderr is the guest's text.
	// Error() leaves it out and GuestOutput returns it, so it reaches
	// --json as data (guest_output), never as part of province's message.
	Guest bool
}

// ErrorLines caps the stderr lines an ExitError puts in its message; the
// full output was already streamed to the operator.
const ErrorLines = 15

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s: exit status %d", e.Cmd, e.Code)
	if e.Guest {
		return msg
	}
	if s := e.stderrTail(); s != "" {
		return msg + ": " + s
	}
	return msg
}

// GuestOutput returns the stderr tail of a command run inside an env, or
// "" for any other command (its stderr is in Error()).
func (e *ExitError) GuestOutput() string {
	if !e.Guest {
		return ""
	}
	return e.stderrTail()
}

// stderrTail is the last ErrorLines lines of Stderr, trimmed.
func (e *ExitError) stderrTail() string {
	s := strings.TrimSpace(e.Stderr)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if n := len(lines) - ErrorLines; n > 0 {
		lines = append([]string{fmt.Sprintf("(%d earlier stderr lines omitted)", n)}, lines[n:]...)
	}
	return strings.Join(lines, "\n")
}

// MarkGuest flags the *ExitError in err as a command run inside an env
// and returns err. Call it on the error of an exec in a guest before
// wrapping it, so the guest's stderr stays out of every wrapping message.
func MarkGuest(err error) error {
	var ee *ExitError
	if errors.As(err, &ee) {
		ee.Guest = true
	}
	return err
}
