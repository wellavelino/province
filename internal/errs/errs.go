// Package errs classifies errors into the outcomes the CLI maps to exit codes.
package errs

import (
	"errors"
	"fmt"
)

// Kind is the class of a failure, reported as "code" in JSON errors.
type Kind string

const (
	// Refused: the request is invalid or unsafe (usage, missing --yes, name taken).
	Refused Kind = "refused"
	// Preflight: a read-only check against the site failed before any change.
	Preflight Kind = "preflight"
	// Failed: an operation failed, possibly after changes were made.
	Failed Kind = "failed"
)

// Error carries a Kind next to the underlying error.
type Error struct {
	Kind Kind
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Refusedf builds a Refused error.
func Refusedf(format string, a ...any) error {
	return &Error{Kind: Refused, Err: fmt.Errorf(format, a...)}
}

// Preflightf builds a Preflight error.
func Preflightf(format string, a ...any) error {
	return &Error{Kind: Preflight, Err: fmt.Errorf(format, a...)}
}

// KindOf returns the Kind of err; untyped errors are Failed.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return Failed
}

// ExitCode maps err to the process exit code: 0 ok, 2 refused/preflight, 1 otherwise.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	switch KindOf(err) {
	case Refused, Preflight:
		return 2
	}
	return 1
}
