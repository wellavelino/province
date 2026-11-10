package move

import (
	"context"
	"fmt"
)

// Result is the end state of a move.
type Result struct {
	VMID        int      `json:"vmid"`
	TargetVMID  int      `json:"target_vmid"`
	Note        string   `json:"note,omitempty"`
	Type        string   `json:"type"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Storage     string   `json:"storage"`
	Source      string   `json:"source"` // what was left on --from
	Target      string   `json:"target"` // running | stopped
	DumpRemoved bool     `json:"dump_removed"`
	Dump        string   `json:"dump,omitempty"` // left on --to when not removed
	Warnings    []string `json:"warnings"`
}

// Move runs preflight, then: shutdown source (if running) -> stream dump ->
// verify archive -> restore on target -> onboot=0 on source -> remove dump
// -> optionally start target. Both copies never run at once: the target is
// started only after the source is stopped and pinned to onboot=0.
//
// If only the final start fails, the guest is restored on the target and
// the source is pinned, so Move returns the Result (Target "stopped") with
// the error: callers must treat the guest as moved.
func (m *Mover) Move(ctx context.Context, o Opts) (*Result, error) {
	p, err := m.preflight(ctx, o)
	if err != nil {
		return nil, err
	}
	res := &Result{VMID: o.VMID, TargetVMID: p.targetID, Type: p.typ, From: p.from.Name, To: p.to.Name, Storage: p.storage,
		Target: "stopped", Warnings: append([]string{}, p.warnings...)}
	for _, w := range p.warnings {
		m.logf("warning: %s", w)
	}

	if p.running {
		m.logf("shutting down %d on %s", o.VMID, p.from.Name)
		if err := p.src.Shutdown(ctx, p.typ, o.VMID, ShutdownTimeout); err != nil {
			return nil, fmt.Errorf("shutdown failed: %w; guest %d may still be running on %s, nothing was copied", err, o.VMID, p.from.Name)
		}
	}
	// From here on the source is stopped. Cleanup must run even if the user
	// interrupts, so it does not use ctx.
	cleanup := context.WithoutCancel(ctx)
	stopped := fmt.Sprintf("guest %d is stopped on %s: %s brings it back; nothing is running on %s", o.VMID, p.from.Name, p.startCmd, p.to.Name)
	removeDump := func() {
		if err := p.dst.Remove(cleanup, p.dumpPath); err != nil {
			m.logf("warning: could not remove %s on %s: %v", p.dumpPath, p.to.Name, err)
		}
	}

	m.logf("streaming dump of %d from %s to %s:%s", o.VMID, p.from.Name, p.to.Name, p.dumpPath)
	if err := m.Runner.Pipe(ctx, p.src.DumpCmd(p.typ, o.VMID), p.dst.ReceiveCmd(p.dumpPath)); err != nil {
		removeDump()
		return nil, fmt.Errorf("dump transfer failed: %w; %s", err, stopped)
	}
	if err := p.dst.ZstdTest(ctx, p.dumpPath); err != nil {
		removeDump()
		return nil, fmt.Errorf("dump archive is corrupt: %w; %s", err, stopped)
	}
	m.logf("restoring %d on %s as %d (storage %s)", o.VMID, p.to.Name, p.targetID, p.storage)
	if err := p.dst.Restore(ctx, p.typ, p.targetID, p.dumpPath, p.storage); err != nil {
		// The verified dump is kept, so a fixed restore needs no second transfer.
		return nil, fmt.Errorf("restore failed: %w; %s; the verified dump is kept at %s on %s (restore it by hand with %s, or delete it); a partial guest %d may exist on %s, check %s",
			err, stopped, p.dumpPath, p.to.Name, p.restoreCmd, p.targetID, p.to.Name, p.targetList)
	}
	if err := p.src.Set(cleanup, p.typ, o.VMID, "--onboot", "0"); err != nil {
		return nil, fmt.Errorf("guest %d was restored (stopped) on %s as %d, but setting onboot=0 on the source failed: %w; the target was not started and the dump %s is kept; set it by hand on %s before starting the target, do not re-run the move",
			o.VMID, p.to.Name, p.targetID, err, p.dumpPath, p.from.Name)
	}
	res.Source = fmt.Sprintf("stopped, onboot=0 (kept as rollback; once you are happy with the copy on %s, remove it with `province guest prune %d --host %s --yes`)",
		p.to.Name, o.VMID, p.from.Name)
	if p.targetID != o.VMID {
		res.Note = fmt.Sprintf("restored as vmid %d; the MAC address in the archive's config is kept, so DHCP gives it the same IP", p.targetID)
	}
	if err := p.dst.Remove(cleanup, p.dumpPath); err != nil {
		res.Dump = p.dumpPath
		res.Warnings = append(res.Warnings, fmt.Sprintf("dump %s on %s not removed: %v", p.dumpPath, p.to.Name, err))
	} else {
		res.DumpRemoved = true
	}
	if o.Start {
		m.logf("starting %d on %s", p.targetID, p.to.Name)
		if err := p.dst.Start(ctx, p.typ, p.targetID); err != nil {
			return res, fmt.Errorf("guest %d was moved to %s as %d but did not start: %w; start it with %s", o.VMID, p.to.Name, p.targetID, err, p.targetStart)
		}
		res.Target = "running"
	}
	return res, nil
}
