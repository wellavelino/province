package env

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/hostops"
	"github.com/wellavelino/province/internal/incus"
	"github.com/wellavelino/province/internal/proxmox"
)

// guest is an env's container on either backend: what province runs in it
// and asks about it after it exists.
type guest interface {
	// Exec runs argv in the guest as root.
	Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, argv ...string) error
	// Status is running, stopped, ... or LiveMissing.
	Status(ctx context.Context) (string, error)
	// StartHint says how to start the guest by hand.
	StartHint() string
}

// pveGuest is a Proxmox LXC container, reached over SSH (pct).
type pveGuest struct {
	host hostops.Host
	name string // host name
	vmid int
}

func (g pveGuest) Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, argv ...string) error {
	return g.host.Exec(ctx, g.vmid, stdin, stdout, argv...)
}

func (g pveGuest) Status(ctx context.Context) (string, error) {
	return g.host.Status(ctx, proxmox.TypeLXC, g.vmid)
}

func (g pveGuest) StartHint() string { return fmt.Sprintf("`pct start %d` on %s", g.vmid, g.name) }

// incusGuest is an Incus container, reached through the incus CLI.
type incusGuest struct {
	c      incus.Client
	remote string
	name   string
}

func (g incusGuest) Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, argv ...string) error {
	return g.c.Exec(ctx, g.name, stdin, stdout, argv...)
}

func (g incusGuest) Status(ctx context.Context) (string, error) {
	list, err := g.c.Instances(ctx)
	if err != nil {
		return "", err
	}
	return incusStatus(list, g.name), nil
}

func (g incusGuest) StartHint() string { return fmt.Sprintf("`incus start %s:%s`", g.remote, g.name) }

// incusStatus is the status of container name in list, or LiveMissing.
func incusStatus(list []incus.Instance, name string) string {
	for _, in := range list {
		if in.Name == name && in.Type != "virtual-machine" {
			return in.Status
		}
	}
	return LiveMissing
}

func (m *Manager) incusClient(h config.Host) (incus.Client, error) {
	if m.Incus == nil {
		return nil, errors.New("incus backend is not wired")
	}
	return m.Incus(h), nil
}

// guestFor returns the guest of env md on host h.
func (m *Manager) guestFor(h config.Host, md *Metadata) (guest, error) {
	if h.IsIncus() {
		c, err := m.incusClient(h)
		if err != nil {
			return nil, err
		}
		return incusGuest{c: c, remote: h.Remote, name: md.Name}, nil
	}
	return pveGuest{host: m.Host(h), name: h.Name, vmid: md.VMID}, nil
}

// hostOf returns the configured host env md is recorded on, refusing when
// the host is gone or now has another backend.
func (m *Manager) hostOf(md *Metadata) (config.Host, error) {
	h, err := m.Cfg.HostByName(md.Host)
	if err != nil {
		return config.Host{}, &errs.Error{Kind: errs.Refused, Err: err}
	}
	if h.BackendName() != md.backend() {
		return config.Host{}, errs.Refusedf("env %s was created on %s as a %s guest, but host %s is now configured with backend %s",
			md.Name, md.Host, md.backend(), h.Name, h.BackendName())
	}
	return h, nil
}
