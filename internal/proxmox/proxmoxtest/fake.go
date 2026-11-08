// Package proxmoxtest provides an in-memory proxmox.Client for one host.
package proxmoxtest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/wellavelino/province/internal/proxmox"
)

// Fake answers from its fields and records each call as "Method arg...".
// Err, when set, fails every call (an unreachable host).
type Fake struct {
	Guests    []proxmox.Guest
	Configs   map[int]map[string]string
	StorageLs []proxmox.Storage
	BridgeLs  []string
	Templates []string
	DiskUsed  map[int]int64 // status/current "disk"; missing means 0
	Err       error

	mu    sync.Mutex
	calls []string
}

func (f *Fake) record(format string, a ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
	return f.Err
}

// Calls returns the recorded calls.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *Fake) ListGuests(_ context.Context, node string) ([]proxmox.Guest, error) {
	if err := f.record("ListGuests %s", node); err != nil {
		return nil, err
	}
	return slices.Clone(f.Guests), nil
}

func (f *Fake) GuestStatus(_ context.Context, node, typ string, vmid int) (string, error) {
	if err := f.record("GuestStatus %s %s %d", node, typ, vmid); err != nil {
		return "", err
	}
	for _, g := range f.Guests {
		if g.VMID == vmid && g.Type == typ {
			return g.Status, nil
		}
	}
	return "", fmt.Errorf("proxmox API: 500 Configuration file 'nodes/%s/%s/%d.conf' does not exist", node, typ, vmid)
}

func (f *Fake) GuestDiskUsed(_ context.Context, node, typ string, vmid int) (int64, error) {
	if err := f.record("GuestDiskUsed %s %s %d", node, typ, vmid); err != nil {
		return 0, err
	}
	return f.DiskUsed[vmid], nil
}

func (f *Fake) GuestConfig(_ context.Context, node, typ string, vmid int) (map[string]string, error) {
	if err := f.record("GuestConfig %s %s %d", node, typ, vmid); err != nil {
		return nil, err
	}
	c, ok := f.Configs[vmid]
	if !ok {
		return nil, fmt.Errorf("no config for %d", vmid)
	}
	return c, nil
}

func (f *Fake) Storages(_ context.Context, node string) ([]proxmox.Storage, error) {
	if err := f.record("Storages %s", node); err != nil {
		return nil, err
	}
	return slices.Clone(f.StorageLs), nil
}

func (f *Fake) Bridges(_ context.Context, node string) ([]string, error) {
	if err := f.record("Bridges %s", node); err != nil {
		return nil, err
	}
	if len(f.BridgeLs) == 0 {
		return nil, fmt.Errorf("node %s: %w", node, proxmox.ErrNoBridges)
	}
	return slices.Clone(f.BridgeLs), nil
}

func (f *Fake) TemplateExists(_ context.Context, node, volid string) (bool, error) {
	if err := f.record("TemplateExists %s %s", node, volid); err != nil {
		return false, err
	}
	return slices.Contains(f.Templates, volid), nil
}
