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
	// verify checks, read-only, that the guest at the env's recorded
	// address is the env's own container. found is false when nothing is
	// there; another guest there is a Preflight error naming it.
	verify(ctx context.Context, md *Metadata) (found bool, err error)
	// status is one created env's live status, or LiveMissing.
	status(ctx context.Context, md *Metadata) (live, error)
	// statuses is the live status of several created envs on this host,
	// in order, with one listing call.
	statuses(ctx context.Context, mds []*Metadata) ([]live, error)
}

// live is what a host reports for one env's guest. Err is set (and Status
// is LiveUnknown) when the guest there is not the env's.
type live struct {
	Status string
	Err    error
}

// notOurs is the error for a guest that sits where env md's guest should
// be but is not it.
func notOurs(md *Metadata, h config.Host, found, fix string) error {
	return errs.Preflightf("%s on %s is %s, not env %s's %s: province leaves it alone; %s",
		md.guestLabel(), h.Name, found, md.Name, md.guestNoun(), fix)
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
		// An env whose guest is gone still owns its VMID: its records,
		// destroy included, act on that number.
		recorded, err := b.m.recordedVMIDs()
		if err != nil {
			return planned{}, err
		}
		for id := range recorded {
			used[id] = true
		}
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

// identify finds env md's guest in guests by VMID. g is nil when no guest
// has the VMID; err is set when the guest there is not an LXC named after
// the env (tofu sets the hostname, which PVE reports as the name).
func (b pveBackend) identify(guests []proxmox.Guest, md *Metadata) (g *proxmox.Guest, err error) {
	i := slices.IndexFunc(guests, func(g proxmox.Guest) bool { return g.VMID == md.VMID })
	if i < 0 {
		return nil, nil
	}
	g = &guests[i]
	if g.Type == proxmox.TypeLXC && g.Name == md.Name {
		return g, nil
	}
	return g, notOurs(md, b.h, fmt.Sprintf("%s %q", g.Type, g.Name), fmt.Sprintf(
		"if it is %s's container renamed, rename it back (`pct set %d --hostname %s` on %s); if %s's container is gone, remove %s by hand",
		md.Name, md.VMID, md.Name, b.h.Name, md.Name, b.m.dir(md.Name)))
}

func (b pveBackend) list(ctx context.Context) ([]proxmox.Guest, error) {
	c, err := b.m.Client(b.h)
	if err != nil {
		return nil, err
	}
	return c.ListGuests(ctx, b.h.Node)
}

func (b pveBackend) verify(ctx context.Context, md *Metadata) (bool, error) {
	guests, err := b.list(ctx)
	if err != nil {
		return false, &errs.Error{Kind: errs.Preflight, Err: err}
	}
	g, err := b.identify(guests, md)
	return g != nil, err
}

func (b pveBackend) status(ctx context.Context, md *Metadata) (live, error) {
	st, err := b.statuses(ctx, []*Metadata{md})
	if err != nil {
		return live{}, err
	}
	return st[0], nil
}

func (b pveBackend) statuses(ctx context.Context, mds []*Metadata) ([]live, error) {
	guests, err := b.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]live, len(mds))
	for i, md := range mds {
		switch g, err := b.identify(guests, md); {
		case g == nil, g.Type != proxmox.TypeLXC: // the env's container is gone
			out[i] = live{Status: LiveMissing}
		case err != nil:
			out[i] = live{Status: LiveUnknown, Err: err}
		default:
			out[i] = live{Status: g.Status}
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

// mainTF carries nesting and /dev/net/tun itself, so prepare has nothing
// to set. A VM gets neither: it has its own kernel.
func (b incusBackend) mainTF(md *Metadata, _ []string, _ planned) ([]byte, error) {
	return tfgen.GenerateIncus(tfgen.Incus{
		Name: md.Name, Remote: b.h.Remote, Project: b.h.Project, Image: b.h.Image,
		Pool: b.h.StoragePool, Network: b.h.Network, CPU: md.CPU, RAM: md.RAM, Disk: md.Disk,
		VM: md.vm(), Docker: md.Docker, TunDevice: !md.vm() && md.Tun == config.TunDevice,
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

// prepare waits for a VM's incus-agent, which `incus exec` needs and which
// comes up some seconds after boot. A container needs nothing.
func (b incusBackend) prepare(ctx context.Context, md *Metadata) error {
	if !md.vm() {
		return nil
	}
	g, err := b.guest(md)
	if err != nil {
		return err
	}
	return b.m.waitAgent(ctx, g, md, b.h)
}

func (b incusBackend) guest(md *Metadata) (guest, error) {
	c, err := b.client()
	if err != nil {
		return nil, err
	}
	return incusGuest{c: c, remote: b.h.Remote, name: md.Name, vm: md.vm()}, nil
}

func (b incusBackend) list(ctx context.Context) ([]incus.Instance, error) {
	c, err := b.client()
	if err != nil {
		return nil, err
	}
	return c.Instances(ctx)
}

// identify finds env md's instance in list by name. in is nil when there
// is none; err is set when it is not of the env's recorded kind (container
// or VM) or does not carry the env's name in user.province.env (which
// province's tofu config sets).
func (b incusBackend) identify(list []incus.Instance, md *Metadata) (in *incus.Instance, err error) {
	i := slices.IndexFunc(list, func(in incus.Instance) bool { return in.Name == md.Name })
	if i < 0 {
		return nil, nil
	}
	in = &list[i]
	if in.Type == incusType(md) && in.Env == md.Name {
		return in, nil
	}
	return in, notOurs(md, b.h, fmt.Sprintf("a %s with %s=%q", in.Type, incus.EnvKey, in.Env),
		fmt.Sprintf("if %s's %s is gone, remove %s by hand", md.Name, md.guestNoun(), b.m.dir(md.Name)))
}

// incusType is the Incus instance type of env md's guest.
func incusType(md *Metadata) string {
	if md.vm() {
		return incus.TypeVM
	}
	return incus.TypeContainer
}

func (b incusBackend) verify(ctx context.Context, md *Metadata) (bool, error) {
	list, err := b.list(ctx)
	if err != nil {
		return false, &errs.Error{Kind: errs.Preflight, Err: err}
	}
	in, err := b.identify(list, md)
	return in != nil, err
}

func (b incusBackend) status(ctx context.Context, md *Metadata) (live, error) {
	st, err := b.statuses(ctx, []*Metadata{md})
	if err != nil {
		return live{}, err
	}
	return st[0], nil
}

func (b incusBackend) statuses(ctx context.Context, mds []*Metadata) ([]live, error) {
	list, err := b.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]live, len(mds))
	for i, md := range mds {
		switch in, err := b.identify(list, md); {
		case in == nil, in.Type != incusType(md): // the env's guest is gone
			out[i] = live{Status: LiveMissing}
		case err != nil:
			out[i] = live{Status: LiveUnknown, Err: err}
		default:
			out[i] = live{Status: in.Status}
		}
	}
	return out, nil
}
