// Package incustest provides a recording fake incus.Client.
package incustest

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/wellavelino/province/internal/hostops/hostopstest"
	"github.com/wellavelino/province/internal/incus"
)

// Fake records each call as "<Name> <Method> <args>" in Calls and, when
// set, in Journal. Fail decides per call whether it errors.
type Fake struct {
	Name     string
	Journal  *hostopstest.Journal
	Fail     func(call string) error
	List     []incus.Instance
	Pools    []string
	Networks []string
	ExecOut  string
	// ExecOuts, keyed by the space-joined argv, overrides ExecOut per command.
	ExecOuts map[string]string

	mu        sync.Mutex
	calls     []string
	execStdin []string
}

func (f *Fake) record(parts ...string) error {
	call := strings.Join(append([]string{f.Name}, parts...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.Journal != nil {
		f.Journal.Add(call)
	}
	if f.Fail != nil {
		return f.Fail(call)
	}
	return nil
}

// Calls returns the recorded calls.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// ExecStdins returns the stdin of each Exec call that had one.
func (f *Fake) ExecStdins() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.execStdin)
}

// Exec implements incus.Client.
func (f *Fake) Exec(_ context.Context, name string, stdin io.Reader, stdout io.Writer, argv ...string) error {
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.mu.Lock()
		f.execStdin = append(f.execStdin, string(b))
		f.mu.Unlock()
	}
	if err := f.record(append([]string{"Exec", name}, argv...)...); err != nil {
		return err
	}
	if stdout != nil {
		out := f.ExecOut
		if o, ok := f.ExecOuts[strings.Join(argv, " ")]; ok {
			out = o
		}
		io.WriteString(stdout, out)
	}
	return nil
}

// Instances implements incus.Client.
func (f *Fake) Instances(context.Context) ([]incus.Instance, error) {
	if err := f.record("Instances"); err != nil {
		return nil, err
	}
	return slices.Clone(f.List), nil
}

// HasStoragePool implements incus.Client.
func (f *Fake) HasStoragePool(_ context.Context, pool string) (bool, error) {
	if err := f.record("HasStoragePool", pool); err != nil {
		return false, err
	}
	return slices.Contains(f.Pools, pool), nil
}

// HasNetwork implements incus.Client.
func (f *Fake) HasNetwork(_ context.Context, network string) (bool, error) {
	if err := f.record("HasNetwork", network); err != nil {
		return false, err
	}
	return slices.Contains(f.Networks, network), nil
}
