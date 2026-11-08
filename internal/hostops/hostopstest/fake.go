// Package hostopstest provides a recording fake hostops.Host.
package hostopstest

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/wellavelino/province/internal/run"
)

// Journal is an ordered log shared by several fakes, so a test can assert
// the order of operations across hosts.
type Journal struct {
	mu      sync.Mutex
	entries []string
}

// Add appends an entry.
func (j *Journal) Add(s string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, s)
}

// Entries returns the log.
func (j *Journal) Entries() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.entries)
}

// Fake records each call as "<Name> <Method> <args>" in Calls and, when
// set, in Journal. Fail decides per call whether it errors. Like a real
// runner, a call made with a cancelled context fails; it is recorded with
// a " [cancelled]" suffix.
type Fake struct {
	Name    string
	Journal *Journal
	Fail    func(call string) error
	State   string           // returned by Status; Shutdown and Stop set "stopped", Start "running"
	Avail   map[string]int64 // DfAvail by path
	ExecOut string           // written to Exec's stdout
	// ExecOuts, keyed by the space-joined argv, overrides ExecOut per command.
	ExecOuts map[string]string

	mu        sync.Mutex
	calls     []string
	execStdin []string
}

func (f *Fake) record(ctx context.Context, method string, args ...any) error {
	parts := []string{f.Name, method}
	for _, a := range args {
		parts = append(parts, fmt.Sprint(a))
	}
	call := strings.Join(parts, " ")
	if ctx.Err() != nil {
		call += " [cancelled]"
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.Journal != nil {
		f.Journal.Add(call)
	}
	if err := ctx.Err(); err != nil {
		return err
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

// ExecStdins returns the stdin of each Exec call.
func (f *Fake) ExecStdins() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.execStdin)
}

func (f *Fake) Exec(ctx context.Context, vmid int, stdin io.Reader, stdout io.Writer, argv ...string) error {
	in := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	f.mu.Lock()
	f.execStdin = append(f.execStdin, in)
	f.mu.Unlock()
	if err := f.record(ctx, "Exec", append([]any{vmid}, toAny(argv)...)...); err != nil {
		return err
	}
	if stdout != nil {
		out, ok := f.ExecOuts[strings.Join(argv, " ")]
		if !ok {
			out = f.ExecOut
		}
		io.WriteString(stdout, out)
	}
	return nil
}

func (f *Fake) Set(ctx context.Context, typ string, vmid int, opts ...string) error {
	return f.record(ctx, "Set", append([]any{typ, vmid}, toAny(opts)...)...)
}

func (f *Fake) setState(s string) {
	f.mu.Lock()
	f.State = s
	f.mu.Unlock()
}

func (f *Fake) Shutdown(ctx context.Context, typ string, vmid int, timeoutSec int) error {
	if err := f.record(ctx, "Shutdown", typ, vmid, timeoutSec); err != nil {
		return err
	}
	f.setState("stopped")
	return nil
}

func (f *Fake) Stop(ctx context.Context, typ string, vmid int) error {
	if err := f.record(ctx, "Stop", typ, vmid); err != nil {
		return err
	}
	f.setState("stopped")
	return nil
}

func (f *Fake) Start(ctx context.Context, typ string, vmid int) error {
	if err := f.record(ctx, "Start", typ, vmid); err != nil {
		return err
	}
	f.setState("running")
	return nil
}

func (f *Fake) Reboot(ctx context.Context, typ string, vmid int) error {
	return f.record(ctx, "Reboot", typ, vmid)
}

func (f *Fake) Status(ctx context.Context, typ string, vmid int) (string, error) {
	if err := f.record(ctx, "Status", typ, vmid); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.State, nil
}

// DumpCmd returns a placeholder command "dump <Name> <typ> <vmid>".
func (f *Fake) DumpCmd(typ string, vmid int) run.Cmd {
	return run.Cmd{Name: "dump", Args: []string{f.Name, typ, strconv.Itoa(vmid)}}
}

// ReceiveCmd returns a placeholder command "receive <Name> <path>".
func (f *Fake) ReceiveCmd(path string) run.Cmd {
	return run.Cmd{Name: "receive", Args: []string{f.Name, path}}
}

func (f *Fake) ZstdTest(ctx context.Context, path string) error {
	return f.record(ctx, "ZstdTest", path)
}

func (f *Fake) DfAvail(ctx context.Context, path string) (int64, error) {
	if err := f.record(ctx, "DfAvail", path); err != nil {
		return 0, err
	}
	return f.Avail[path], nil
}

func (f *Fake) Remove(ctx context.Context, path string) error { return f.record(ctx, "Remove", path) }

func (f *Fake) Restore(ctx context.Context, typ string, vmid int, archive, storage string) error {
	return f.record(ctx, "Restore", typ, vmid, archive, storage)
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func (f *Fake) Destroy(ctx context.Context, typ string, vmid int) error {
	return f.record(ctx, "Destroy", typ, vmid)
}
