// Package tofutest provides a recording fake tofu.Tofu.
package tofutest

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
)

// Fake records calls as "<Method> <dir>". Fail decides per call whether it
// errors; OnCall sees each call (e.g. to feed a shared journal).
type Fake struct {
	PlanText string
	Changes  bool
	Outputs  map[string]json.RawMessage
	Fail     func(call string) error
	OnCall   func(call string)

	mu    sync.Mutex
	calls []string
}

func (f *Fake) record(method, dir string) error {
	call := method + " " + dir
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.OnCall != nil {
		f.OnCall(call)
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

func (f *Fake) Init(_ context.Context, dir string) error { return f.record("Init", dir) }

func (f *Fake) Plan(_ context.Context, dir string) (string, bool, error) {
	if err := f.record("Plan", dir); err != nil {
		return "", false, err
	}
	return f.PlanText, f.Changes, nil
}

func (f *Fake) Apply(_ context.Context, dir string) error   { return f.record("Apply", dir) }
func (f *Fake) Destroy(_ context.Context, dir string) error { return f.record("Destroy", dir) }

func (f *Fake) Output(_ context.Context, dir string) (map[string]json.RawMessage, error) {
	if err := f.record("Output", dir); err != nil {
		return nil, err
	}
	return f.Outputs, nil
}

func (f *Fake) StateRm(_ context.Context, dir, addr string) error {
	return f.record("StateRm", dir+" "+addr)
}

func (f *Fake) Import(_ context.Context, dir, addr, id string) error {
	return f.record("Import", dir+" "+addr+" "+id)
}
