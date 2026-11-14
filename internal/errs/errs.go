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

// guestError is an error whose message is province's own, with text a
// guest produced kept beside it.
type guestError struct {
	err    error
	output string
}

func (e *guestError) Error() string       { return e.err.Error() }
func (e *guestError) Unwrap() error       { return e.err }
func (e *guestError) GuestOutput() string { return e.output }

// WithGuestOutput returns err carrying out, text a guest produced, apart
// from its message: --json reports it as guest_output, which is data.
func WithGuestOutput(err error, out string) error {
	return &guestError{err: err, output: out}
}

// GuestOutput returns the first non-empty guest-produced text in err's
// chain (see WithGuestOutput and run.ExitError), or "".
func GuestOutput(err error) string {
	if err == nil {
		return ""
	}
	if g, ok := err.(interface{ GuestOutput() string }); ok {
		if s := g.GuestOutput(); s != "" {
			return s
		}
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return GuestOutput(u.Unwrap())
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if s := GuestOutput(e); s != "" {
				return s
			}
		}
	}
	return ""
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
