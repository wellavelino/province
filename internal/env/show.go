package env

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/proxmox"
)

// load reads an environment's metadata; an unknown env is a plain failure.
func (m *Manager) load(name string) (*Metadata, error) {
	if err := config.ValidName(name); err != nil {
		return nil, &errs.Error{Kind: errs.Refused, Err: err}
	}
	md, err := readMetadata(m.dir(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("env %s not found in %s", name, m.Cfg.StateDir)
	}
	return md, err
}

// Show returns one environment with its live status.
func (m *Manager) Show(ctx context.Context, name string) (*View, error) {
	md, err := m.load(name)
	if err != nil {
		return nil, err
	}
	v := m.view(md)
	if md.VMID == 0 {
		return &v, nil
	}
	// Like list: a host that cannot be asked (gone from the config, token
	// unset, unreachable) makes the live status unknown, not an error.
	h, err := m.Cfg.HostByName(md.Host)
	var api proxmox.Client
	if err == nil {
		api, err = m.Client(h)
	}
	if err == nil {
		v.LiveStatus, err = api.GuestStatus(ctx, h.Node, proxmox.TypeLXC, md.VMID)
	}
	if err != nil {
		v.LiveStatus, v.Error = LiveUnknown, err.Error()
	}
	return &v, nil
}
