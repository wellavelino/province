// Package run executes local processes; every remote action goes through it
// as an ssh invocation.
package run

import (
	"context"
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
}

// ErrorLines caps the stderr lines an ExitError puts in its message; the
// full output was already streamed to the operator.
const ErrorLines = 15

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s: exit status %d", e.Cmd, e.Code)
	s := strings.TrimSpace(e.Stderr)
	if s == "" {
		return msg
	}
	lines := strings.Split(s, "\n")
	if n := len(lines) - ErrorLines; n > 0 {
		lines = append([]string{fmt.Sprintf("(%d earlier stderr lines omitted)", n)}, lines[n:]...)
	}
	return msg + ": " + strings.Join(lines, "\n")
}
