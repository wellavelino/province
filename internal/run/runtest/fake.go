// Package runtest provides a scripted fake run.Runner.
package runtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/wellavelino/province/internal/run"
)

// Response is what a scripted command does.
type Response struct {
	Stdout string
	Stderr string
	Code   int   // non-zero -> *run.ExitError
	Err    error // returned as is (e.g. a start failure)
}

// Call is one recorded command.
type Call struct {
	Argv  []string
	Env   []string
	Dir   string
	Stdin string
}

// String joins the argv with spaces.
func (c Call) String() string { return strings.Join(c.Argv, " ") }

type rule struct {
	prefix []string
	resp   Response
	once   bool
	used   bool
}

// Fake is a run.Runner that answers commands by argv prefix, records every
// call in order and fails the test on a command nobody scripted.
type Fake struct {
	T      testing.TB
	OnCall func(Call) // optional; sees each call as it happens (e.g. a shared journal)

	mu    sync.Mutex
	rules []*rule
	calls []Call
}

// On answers every command starting with prefix.
func (f *Fake) On(prefix []string, r Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, &rule{prefix: prefix, resp: r})
}

// Once answers the next command starting with prefix, once. Rules are tried
// in the order they were added.
func (f *Fake) Once(prefix []string, r Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, &rule{prefix: prefix, resp: r, once: true})
}

// Calls returns the recorded calls.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Commands returns the recorded argvs joined with spaces.
func (f *Fake) Commands() []string {
	var out []string
	for _, c := range f.Calls() {
		out = append(out, c.String())
	}
	return out
}

// ErrUnexpected is returned for a command no rule matches.
var ErrUnexpected = errors.New("runtest: unexpected command")

func (f *Fake) exec(c run.Cmd, stdin io.Reader, stdout io.Writer) error {
	call := Call{Argv: c.Argv(), Env: slices.Clone(c.Env), Dir: c.Dir}
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		call.Stdin = string(b)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	var matched *rule
	for _, r := range f.rules {
		if (r.once && r.used) || len(r.prefix) > len(call.Argv) || !slices.Equal(r.prefix, call.Argv[:len(r.prefix)]) {
			continue
		}
		matched = r
		r.used = true
		break
	}
	onCall := f.OnCall
	f.mu.Unlock()
	if onCall != nil {
		onCall(call)
	}
	if matched == nil {
		if f.T != nil {
			f.T.Errorf("unexpected command: %q", call.Argv)
		}
		return fmt.Errorf("%w: %s", ErrUnexpected, call)
	}
	r := matched.resp
	if stdout != nil {
		io.WriteString(stdout, r.Stdout)
	}
	if c.Stderr != nil {
		io.WriteString(c.Stderr, r.Stderr)
	}
	if r.Err != nil {
		return r.Err
	}
	if r.Code != 0 {
		return &run.ExitError{Cmd: c.String(), Code: r.Code, Stderr: r.Stderr}
	}
	return nil
}

// Run implements run.Runner.
func (f *Fake) Run(_ context.Context, c run.Cmd) error {
	return f.exec(c, c.Stdin, c.Stdout)
}

// Pipe implements run.Runner: src's scripted stdout becomes dst's stdin.
func (f *Fake) Pipe(_ context.Context, src, dst run.Cmd) error {
	var buf bytes.Buffer
	srcErr := f.exec(src, src.Stdin, &buf)
	dstErr := f.exec(dst, &buf, dst.Stdout)
	return errors.Join(srcErr, dstErr)
}
