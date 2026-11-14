package env

import (
	"context"
	"fmt"
	"io"

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

// incusGuest is an Incus container or VM, reached through the incus CLI.
type incusGuest struct {
	c      incus.Client
	remote string
	name   string
	vm     bool
}

func (g incusGuest) Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, argv ...string) error {
	return g.c.Exec(ctx, g.name, stdin, stdout, argv...)
}

func (g incusGuest) Status(ctx context.Context) (string, error) {
	list, err := g.c.Instances(ctx)
	if err != nil {
		return "", err
	}
	typ := incus.TypeContainer
	if g.vm {
		typ = incus.TypeVM
	}
	return incusStatus(list, g.name, typ), nil
}

func (g incusGuest) StartHint() string { return fmt.Sprintf("`incus start %s:%s`", g.remote, g.name) }

// incusStatus is the status of the instance of type typ named name in
// list, or LiveMissing.
func incusStatus(list []incus.Instance, name, typ string) string {
	for _, in := range list {
		if in.Name == name && in.Type == typ {
			return in.Status
		}
	}
	return LiveMissing
}
