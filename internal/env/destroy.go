package env

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/wellavelino/province/internal/errs"
)

// DestroyResult is the outcome of env destroy.
type DestroyResult struct {
	Name      string   `json:"name"`
	Host      string   `json:"host"`
	VMID      int      `json:"vmid,omitempty"`
	Instance  string   `json:"instance,omitempty"`
	Destroyed bool     `json:"destroyed"`
	Warnings  []string `json:"warnings"`
}

// Destroy removes the environment's tailnet node (best effort), its guest
// (tofu destroy) and finally its state directory. Without yes it refuses
// before touching anything.
func (m *Manager) Destroy(ctx context.Context, name string, yes bool) (*DestroyResult, error) {
	if !yes {
		return nil, errs.Refusedf("env destroy %s deletes the container and its disk: pass --yes", name)
	}
	md, err := m.load(name)
	if err != nil {
		return nil, err
	}
	if md.TofuOutOfSync {
		return nil, errs.Refusedf("env %s was moved but its tofu state still points at the old guest, so destroy could hit the wrong one; finish the move first:\n  %s",
			name, strings.Join(md.TofuFix, "\n  "))
	}
	res := &DestroyResult{Name: name, Host: md.Host, VMID: md.VMID, Instance: md.Instance, Warnings: []string{}}
	warn := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		res.Warnings = append(res.Warnings, msg)
		m.logf("warning: %s", msg)
	}

	h, err := m.hostOf(md)
	if err != nil {
		return nil, err
	}
	tf, err := m.Tofu(h) // before any change: the host's token must be set
	if err != nil {
		return nil, err
	}
	if md.Status != StatusPlanned && md.created() {
		g, err := m.backendFor(h).guest(md)
		if err != nil {
			return nil, err
		}
		// exec fails on a stopped container, so only log out a running one.
		switch st, err := g.Status(ctx); {
		case err != nil:
			warn("skipped tailscale logout in %s: status check failed: %v", name, err)
		case st != "running":
			m.logf("info: %s is %s; skipping tailscale logout", name, st)
		default:
			if err := g.Exec(ctx, nil, nil, "tailscale", "logout"); err != nil {
				warn("tailscale logout in %s failed: %v", name, err)
			}
		}
		switch {
		case m.Tailnet == nil:
			warn("TS_API_KEY is not set: the tailnet node %s stays listed (offline) in the admin console; delete it there", name)
		default:
			deleted, err := m.Tailnet.DeleteDeviceByHostname(ctx, name)
			switch {
			case err != nil:
				warn("tailnet node %s not deleted: %v", name, err)
			case !deleted:
				warn("no tailnet node named %s with tag %s found", name, m.Cfg.Tailscale.Tag)
			}
		}
	}

	dir := m.dir(name)
	if err := tf.Init(ctx, dir); err != nil {
		return nil, fmt.Errorf("%w; %s is kept, re-run `province env destroy %s --yes`", err, dir, name)
	}
	if err := tf.Destroy(ctx, dir); err != nil {
		return nil, fmt.Errorf("%w; %s is kept, re-run `province env destroy %s --yes`", err, dir, name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("guest destroyed but %s could not be removed: %w", dir, err)
	}
	res.Destroyed = true
	return res, nil
}
