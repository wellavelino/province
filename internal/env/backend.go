package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/incus"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/tfgen"
	"github.com/wellavelino/province/internal/vmids"
)

// backend is everything the env lifecycle does differently per host
// platform. The lifecycle itself (create, list, show, destroy, expose,
// provision) is written once against it; this file is the only place in
// package env that knows which platforms exist.
type backend interface {
	// check fails when the host's credential or client is missing. No I/O.
	check() error
	// plan checks, read-only, that the host can take env md, and picks
	// what a new guest needs before tofu runs.
	plan(ctx context.Context, md *Metadata) (planned, error)
	// mainTF renders the env's tofu config.
	mainTF(md *Metadata, keys []string, p planned) ([]byte, error)
	// adopt records the guest tofu created, from the tofu outputs.
	adopt(out map[string]json.RawMessage, md *Metadata) error
	// prepare applies what tofu cannot set, before the bootstrap.
	prepare(ctx context.Context, md *Metadata) error
	// guest returns the env's container for exec and status.
	guest(md *Metadata) (guest, error)
	// status is one created env's live status, or LiveMissing.
	status(ctx context.Context, md *Metadata) (string, error)
	// statuses is the live status of several created envs on this host,
	// in order, with one listing call.
	statuses(ctx context.Context, mds []*Metadata) ([]string, error)
}

// planned is what plan decided for a guest that does not exist yet.
type planned struct {
	VMID     int // Proxmox: the guest's VMID; 0 elsewhere
	Warnings []string
}

// backendFor returns host h's backend.
func (m *Manager) backendFor(h config.Host) backend {
	if h.IsIncus() {
		return incusBackend{m: m, h: h}
	}
	return pveBackend{m: m, h: h}
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

// pveBackend is a standalone Proxmox VE node: the API for reads, tofu
// (bpg/proxmox) for the container, SSH (pct) for the rest.
type pveBackend struct {
	m *Manager
	h config.Host
}

func (b pveBackend) check() error {
	_, err := b.m.Client(b.h)
	return err
}

// plan checks template, bridge and storage through the API. A new env gets
// a VMID free on every Proxmox host, so it can later move to any of them
// without a clash; once created, its VMID is fixed.
func (b pveBackend) plan(ctx context.Context, md *Metadata) (planned, error) {
	c, err := b.m.Client(b.h)
	if err != nil {
		return planned{}, err
	}
	h, d := b.h, b.m.Cfg.Defaults
	ok, err := c.TemplateExists(ctx, h.Node, d.Template)
	if err != nil {
		return planned{}, &errs.Error{Kind: errs.Preflight, Err: err}
	}
	if !ok {
		return planned{}, errs.Preflightf("template %s not found on %s: download it with `pveam download` on the host", d.Template, h.Name)
	}
	bridges, err := c.Bridges(ctx, h.Node)
	if err != nil {
		return planned{}, &errs.Error{Kind: errs.Preflight, Err: err}
	}
	if !slices.Contains(bridges, d.Bridge) {
		return planned{}, errs.Preflightf("bridge %s not found on %s (have %s)", d.Bridge, h.Name, strings.Join(bridges, ", "))
	}
	storages, err := c.Storages(ctx, h.Node)
	if err != nil {
		return planned{}, &errs.Error{Kind: errs.Preflight, Err: err}
	}
	i := slices.IndexFunc(storages, func(s proxmox.Storage) bool { return s.Name == d.Storage })
	if i < 0 {
		return planned{}, errs.Preflightf("storage %s not found on %s", d.Storage, h.Name)
	}
	if s := storages[i]; !s.Active || !s.Has("rootdir") {
		return planned{}, errs.Preflightf("storage %s on %s is inactive or does not hold container volumes (rootdir)", d.Storage, h.Name)
	}
	p := planned{VMID: md.VMID}
	if p.VMID == 0 {
		used, w := vmids.Used(ctx, b.m.Cfg.ProxmoxHosts(), b.m.Client)
		p.Warnings = w
		p.VMID = vmids.LowestFree(used, b.m.Cfg.VMIDStart)
	}
	return p, nil
}

func (b pveBackend) mainTF(md *Metadata, keys []string, p planned) ([]byte, error) {
	storage := md.Storage
	if storage == "" {
		storage = b.m.Cfg.Defaults.Storage
	}
	return tfgen.Generate(tfgen.LXC{
		Name: md.Name, VMID: p.VMID, Node: b.h.Node, Endpoint: b.h.Endpoint, Insecure: b.h.Insecure,
		Template: b.m.Cfg.Defaults.Template, Storage: storage, Bridge: b.m.Cfg.Defaults.Bridge,
		CPU: md.CPU, RAM: md.RAM, Disk: md.Disk, Keys: keys,
		Docker: md.Docker, TunDevice: md.Tun == config.TunDevice,
	})
}

func (b pveBackend) adopt(out map[string]json.RawMessage, md *Metadata) error {
	var vmid int
	if raw, ok := out["vmid"]; !ok || json.Unmarshal(raw, &vmid) != nil || vmid <= 0 {
		return errors.New("no numeric vmid")
	}
	md.VMID = vmid
	return nil
}

// prepare sets keyctl (Docker) and /dev/net/tun over SSH and reboots: an
// API token may not set them, so tofu cannot.
func (b pveBackend) prepare(ctx context.Context, md *Metadata) error {
	var opts []string
	if md.Docker {
		opts = append(opts, "--features", "nesting=1,keyctl=1")
	}
	if md.Tun == config.TunDevice {
		opts = append(opts, "--dev0", "/dev/net/tun")
	}
	if len(opts) == 0 {
		return nil
	}
	host := b.m.Host(b.h)
	if err := host.Set(ctx, proxmox.TypeLXC, md.VMID, opts...); err != nil {
		return err
	}
	return host.Reboot(ctx, proxmox.TypeLXC, md.VMID)
}

func (b pveBackend) guest(md *Metadata) (guest, error) {
	return pveGuest{host: b.m.Host(b.h), name: b.h.Name, vmid: md.VMID}, nil
}

func (b pveBackend) status(ctx context.Context, md *Metadata) (string, error) {
	c, err := b.m.Client(b.h)
	if err != nil {
		return "", err
	}
	return c.GuestStatus(ctx, b.h.Node, proxmox.TypeLXC, md.VMID)
}

func (b pveBackend) statuses(ctx context.Context, mds []*Metadata) ([]string, error) {
	c, err := b.m.Client(b.h)
	if err != nil {
		return nil, err
	}
	guests, err := c.ListGuests(ctx, b.h.Node)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(mds))
	for i, md := range mds {
		out[i] = LiveMissing
		for _, g := range guests {
			if g.VMID == md.VMID && g.Type == proxmox.TypeLXC {
				out[i] = g.Status
			}
		}
	}
	return out, nil
}

// incusBackend is an Incus server reached through the operator's incus
// remote: tofu (lxc/incus) for the container, the incus CLI for the rest.
type incusBackend struct {
	m *Manager
	h config.Host
}

func (b incusBackend) client() (incus.Client, error) {
	if b.m.Incus == nil {
		return nil, errors.New("incus backend is not wired")
	}
	return b.m.Incus(b.h), nil
}

func (b incusBackend) check() error {
	_, err := b.client()
	return err
}

// plan checks that the remote answers and has the pool and the network.
func (b incusBackend) plan(ctx context.Context, _ *Metadata) (planned, error) {
	c, err := b.client()
	if err != nil {
		return planned{}, err
	}
	h := b.h
	ok, err := c.HasStoragePool(ctx, h.StoragePool)
	if err != nil {
		return planned{}, errs.Preflightf("incus remote %s (host %s) did not answer: %v; check `incus remote list` and `incus info %s:`", h.Remote, h.Name, err, h.Remote)
	}
	if !ok {
		return planned{}, errs.Preflightf("storage pool %s not found on incus remote %s (see `incus storage list %s:`)", h.StoragePool, h.Remote, h.Remote)
	}
	if h.Network != "" {
		ok, err := c.HasNetwork(ctx, h.Network)
		if err != nil {
			return planned{}, &errs.Error{Kind: errs.Preflight, Err: err}
		}
		if !ok {
			return planned{}, errs.Preflightf("network %s not found on incus remote %s (see `incus network list %s:`)", h.Network, h.Remote, h.Remote)
		}
	}
	return planned{}, nil
}

// mainTF carries nesting and /dev/net/tun itself, so prepare has nothing to do.
func (b incusBackend) mainTF(md *Metadata, _ []string, _ planned) ([]byte, error) {
	return tfgen.GenerateIncus(tfgen.Incus{
		Name: md.Name, Remote: b.h.Remote, Project: b.h.Project, Image: b.h.Image,
		Pool: b.h.StoragePool, Network: b.h.Network, CPU: md.CPU, RAM: md.RAM, Disk: md.Disk,
		Docker: md.Docker, TunDevice: md.Tun == config.TunDevice,
	})
}

func (b incusBackend) adopt(out map[string]json.RawMessage, md *Metadata) error {
	var got string
	if raw, ok := out["name"]; !ok || json.Unmarshal(raw, &got) != nil || got != md.Name {
		return fmt.Errorf("no instance named %s", md.Name)
	}
	md.Instance = md.Name
	return nil
}

func (incusBackend) prepare(context.Context, *Metadata) error { return nil }

func (b incusBackend) guest(md *Metadata) (guest, error) {
	c, err := b.client()
	if err != nil {
		return nil, err
	}
	return incusGuest{c: c, remote: b.h.Remote, name: md.Name}, nil
}

func (b incusBackend) status(ctx context.Context, md *Metadata) (string, error) {
	st, err := b.statuses(ctx, []*Metadata{md})
	if err != nil {
		return "", err
	}
	return st[0], nil
}

func (b incusBackend) statuses(ctx context.Context, mds []*Metadata) ([]string, error) {
	c, err := b.client()
	if err != nil {
		return nil, err
	}
	list, err := c.Instances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(mds))
	for i, md := range mds {
		out[i] = incusStatus(list, md.Name)
	}
	return out, nil
}

// incusStatus is the status of container name in list, or LiveMissing.
func incusStatus(list []incus.Instance, name string) string {
	for _, in := range list {
		if in.Name == name && in.Type != "virtual-machine" {
			return in.Status
		}
	}
	return LiveMissing
}
