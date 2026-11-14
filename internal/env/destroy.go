package env

import (
	"context"
	"fmt"
	"os"

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
		return nil, errs.Refusedf("env destroy %s deletes its guest (container or VM) and disk: pass --yes", name)
	}
	md, err := m.load(name)
	if err != nil {
		return nil, err
	}
	if err := md.refuseProtected(); err != nil {
		return nil, err
	}
	if err := md.refuseOutOfSync("destroy"); err != nil {
		return nil, err
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
		b := m.backendFor(h)
		// Before any change: tofu destroy and the logout act on whatever
		// guest has the env's VMID (or name), so it must be the env's own.
		found, err := b.verify(ctx, md)
		if err != nil {
			return nil, err
		}
		g, err := b.guest(md)
		if err != nil {
			return nil, err
		}
		// exec fails on a stopped container, so only log out a running one.
		switch st, err := g.Status(ctx); {
		case !found:
			m.logf("info: %s's container is gone from %s; skipping tailscale logout", name, md.Host)
		case err != nil:
			warn("skipped tailscale logout in %s: status check failed: %v", name, err)
		case st != "running":
			m.logf("info: %s is %s; skipping tailscale logout", name, st)
		default:
			if err := g.Exec(ctx, nil, nil, "tailscale", "logout"); err != nil {
				warn("tailscale logout in %s failed: %v", name, err)
			}
		}
		m.revokeNode(ctx, md, warn)
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

// revokeNode deletes env md's tailnet device: by the ID recorded at
// bootstrap, else by name and tag only if the device so named holds the
// recorded address. An env that never joined is skipped: a device of its
// name is someone else's.
func (m *Manager) revokeNode(ctx context.Context, md *Metadata, warn func(string, ...any)) {
	name := md.Name
	if md.Status != StatusBootstrapped && md.TailscaleDeviceID == "" && md.TailscaleIP == "" {
		m.logf("info: %s never joined the tailnet; no tailnet node to delete", name)
		return
	}
	notRevoked := func(why string) {
		warn("tailnet node %s NOT revoked (%s); delete it in the admin console", name, why)
	}
	switch {
	case m.Tailnet == nil:
		notRevoked("TS_API_KEY is not set")
	case md.TailscaleDeviceID != "":
		if err := m.Tailnet.DeleteDevice(ctx, md.TailscaleDeviceID); err != nil {
			notRevoked(fmt.Sprintf("deleting device %s: %v", md.TailscaleDeviceID, err))
		}
	case md.TailscaleIP == "":
		notRevoked("no device ID or tailnet address recorded")
	default:
		deleted, err := m.Tailnet.DeleteDeviceByHostname(ctx, name, md.TailscaleIP)
		switch {
		case err != nil:
			notRevoked(err.Error())
		case !deleted:
			notRevoked(fmt.Sprintf("no device named %s with tag %s at %s", name, m.Cfg.Tailscale.Tag, md.TailscaleIP))
		}
	}
}
