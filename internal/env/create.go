package env

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wellavelino/province/internal/bootstrap"
	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/tfgen"
	"github.com/wellavelino/province/internal/tofu"
	"github.com/wellavelino/province/internal/vmids"
)

// CreateOpts are the env create flags. Zero sizes fall back to the
// environment's recorded sizes, then to the site defaults.
type CreateOpts struct {
	Name   string
	Host   string
	CPU    int
	RAM    int
	Disk   int
	Docker bool
	Expose int // port published through Tailscale Funnel; 0 keeps the recorded one
	// Recipe to apply after bootstrap; empty keeps the recorded one, else
	// defaults.recipe; "none" applies none.
	Recipe string
	Apply  bool
}

// CreateResult is the outcome of env create.
type CreateResult struct {
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	Status      Status   `json:"status"`
	Backend     string   `json:"backend"`
	VMID        int      `json:"vmid,omitempty"`
	Instance    string   `json:"instance,omitempty"`
	Recipe      *Recipe  `json:"recipe,omitempty"`
	TailscaleIP string   `json:"tailscale_ip,omitempty"`
	ExposePort  int      `json:"expose_port,omitempty"`
	PublicURL   string   `json:"public_url,omitempty"`
	SSH         string   `json:"ssh,omitempty"`
	Changes     bool     `json:"changes"`
	Plan        string   `json:"plan,omitempty"`
	Dir         string   `json:"state_dir"`
	Warnings    []string `json:"warnings"`
}

func pick(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

// Create plans an environment, or with Apply creates and bootstraps it.
func (m *Manager) Create(ctx context.Context, o CreateOpts) (*CreateResult, error) {
	if err := config.ValidName(o.Name); err != nil {
		return nil, &errs.Error{Kind: errs.Refused, Err: err}
	}
	if o.Apply && m.Secrets.AuthKey == "" {
		return nil, errs.Refusedf("TS_AUTHKEY is not set: --apply needs a Tailscale auth key for the new guest")
	}
	if o.CPU < 0 || o.RAM < 0 || o.Disk < 0 {
		return nil, errs.Refusedf("--cpu, --ram and --disk must be positive")
	}
	if o.Expose != 0 {
		if err := validPort(o.Expose); err != nil {
			return nil, err
		}
	}
	dir := m.dir(o.Name)
	md, err := readMetadata(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		md = nil
	case err != nil:
		return nil, err
	case md.Status == StatusBootstrapped:
		return nil, errs.Refusedf("env %s already exists on %s (vmid %d); destroy it first", o.Name, md.Host, md.VMID)
	}

	host, err := m.createHost(o, md)
	if err != nil {
		return nil, err
	}
	if md != nil && md.backend() != host.BackendName() {
		return nil, errs.Refusedf("env %s was planned on %s as a %s guest, but host %s is now configured with backend %s; destroy it first",
			o.Name, md.Host, md.backend(), host.Name, host.BackendName())
	}
	recorded := ""
	if md != nil {
		recorded = md.Recipe
	}
	recipe, err := m.recipe(o.Recipe, recorded)
	if err != nil {
		return nil, err
	}
	if md == nil {
		md = &Metadata{Name: o.Name, Kind: "lxc", Status: StatusPlanned, CreatedAt: m.Now().UTC()}
	}
	md.Backend = host.BackendName()
	if o.Recipe != "" {
		md.Recipe = o.Recipe
	} else if md.Recipe == "" && recipe != nil {
		md.Recipe = recipe.Name
	}
	md.Host, md.Node = host.Name, host.Node
	md.CPU = pick(o.CPU, md.CPU, m.Cfg.Defaults.CPU)
	md.RAM = pick(o.RAM, md.RAM, m.Cfg.Defaults.RAM)
	md.Disk = pick(o.Disk, md.Disk, m.Cfg.Defaults.Disk)
	md.Docker = o.Docker || md.Docker
	md.ExposePort = pick(o.Expose, md.ExposePort)
	md.Tun = m.Cfg.Tailscale.Tun

	var api proxmox.Client
	if !host.IsIncus() {
		if api, err = m.Client(host); err != nil {
			return nil, err
		}
	}
	tf, err := m.Tofu(host)
	if err != nil {
		return nil, err
	}
	keys, err := m.readKeys()
	if err != nil {
		return nil, err
	}
	vmid := md.VMID
	var warnings []string
	if host.IsIncus() {
		if err := m.incusPreflight(ctx, host); err != nil {
			return nil, err
		}
	} else {
		if err := m.createPreflight(ctx, api, host); err != nil {
			return nil, err
		}
		// A new env gets a VMID free on every Proxmox host, so it can later
		// move to any of them without a clash. Once created, its VMID is fixed.
		if vmid == 0 {
			used, w := vmids.Used(ctx, m.Cfg.ProxmoxHosts(), m.Client)
			for _, s := range w {
				m.logf("warning: %s", s)
			}
			warnings = w
			vmid = vmids.LowestFree(used, m.Cfg.VMIDStart)
		}
	}

	if err := mkdirAll(dir); err != nil {
		return nil, err
	}
	if err := m.writeMainTF(dir, md, host, vmid, keys); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, ".gitignore"), []byte(".terraform/\n")); err != nil {
		return nil, err
	}
	if err := m.writeMetadata(dir, md); err != nil {
		return nil, err
	}
	res := &CreateResult{Name: o.Name, Host: host.Name, Backend: host.BackendName(), Dir: dir, VMID: vmid, Recipe: recipe,
		ExposePort: md.ExposePort, Warnings: append([]string{}, warnings...)}

	if err := tf.Init(ctx, dir); err != nil {
		return nil, err
	}
	if !o.Apply {
		text, changes, err := tf.Plan(ctx, dir)
		if err != nil {
			return nil, err
		}
		res.Status, res.Changes, res.Plan = md.Status, changes, text
		return res, nil
	}

	m.logf("applying %s on %s", o.Name, host.Name)
	if err := tf.Apply(ctx, dir); err != nil {
		return nil, fmt.Errorf("%w; fix the cause and re-run `province env create %s --apply`", err, o.Name)
	}
	if host.IsIncus() {
		if err := instanceOutput(ctx, tf, dir, o.Name); err != nil {
			return nil, err
		}
		md.Instance = o.Name
	} else {
		if vmid, err = vmidOutput(ctx, tf, dir); err != nil {
			return nil, err
		}
		md.VMID = vmid
	}
	md.Status, md.TailscaleIP = StatusApplied, ""
	if err := m.writeMetadata(dir, md); err != nil {
		return nil, err
	}

	ip, err := m.bootstrap(ctx, host, md, keys)
	if err != nil {
		md.Status = StatusBootstrapFailed
		if werr := m.writeMetadata(dir, md); werr != nil {
			err = errors.Join(err, werr)
		}
		return nil, fmt.Errorf("bootstrap of %s (%s on %s) failed: %w; the container and %s are kept: fix the cause and re-run `province env create %s --apply`",
			o.Name, md.guestRef(host), host.Name, err, dir, o.Name)
	}
	md.Status, md.TailscaleIP, md.PublicURL = StatusBootstrapped, ip, ""
	if err := m.writeMetadata(dir, md); err != nil {
		return nil, err
	}
	g, err := m.guestFor(host, md)
	if err != nil {
		return nil, err
	}
	if md.ExposePort != 0 {
		// The env is bootstrapped and recorded as such; a Funnel failure
		// only leaves it unpublished. `tailscale funnel --bg` is idempotent.
		m.logf("publishing %s port %d through Tailscale Funnel", o.Name, md.ExposePort)
		url, err := m.funnelOn(ctx, g, md, md.ExposePort)
		if err != nil {
			// The guest exists, so this is a failure after changes, whatever
			// the cause; env expose is the retry.
			return nil, &errs.Error{Kind: errs.Failed, Err: fmt.Errorf("created %s (%s on %s, tailnet IP %s) but did not publish port %d (retry with `province env expose %s --port %d --yes`): %w",
				o.Name, md.guestRef(host), host.Name, ip, md.ExposePort, o.Name, md.ExposePort, err)}
		}
		md.PublicURL = url
		if err := m.writeMetadata(dir, md); err != nil {
			return nil, err
		}
	}
	if recipe != nil {
		if err := m.applyRecipe(ctx, g, md, recipe); err != nil {
			// The env is bootstrapped and usable; env provision is the retry.
			return nil, &errs.Error{Kind: errs.Failed, Err: fmt.Errorf("created %s (%s on %s, tailnet IP %s) but recipe %s failed (fix it, then run `province env provision %s`): %w",
				o.Name, md.guestRef(host), host.Name, ip, recipe.Name, o.Name, err)}
		}
	}
	res.Status, res.VMID, res.Instance, res.TailscaleIP, res.PublicURL, res.Changes = md.Status, md.VMID, md.Instance, ip, md.PublicURL, true
	res.SSH = fmt.Sprintf("ssh %s@%s", m.Cfg.User, o.Name)
	return res, nil
}

func (m *Manager) createHost(o CreateOpts, md *Metadata) (config.Host, error) {
	name := o.Host
	if md != nil {
		if name != "" && name != md.Host {
			return config.Host{}, errs.Refusedf("env %s is recorded on host %s, not %s", o.Name, md.Host, name)
		}
		name = md.Host
	}
	var h config.Host
	var err error
	if name != "" {
		h, err = m.Cfg.HostByName(name)
	} else {
		h, err = m.Cfg.DefaultHost()
	}
	if err != nil {
		return config.Host{}, &errs.Error{Kind: errs.Refused, Err: err}
	}
	return h, nil
}

// readKeys returns the public key lines of every configured key file.
func (m *Manager) readKeys() ([]string, error) {
	var keys []string
	for _, p := range m.Cfg.SSHPublicKeys {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, errs.Preflightf("ssh public key: %v", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				keys = append(keys, line)
			}
		}
	}
	if len(keys) == 0 {
		return nil, errs.Preflightf("ssh_public_keys files hold no key")
	}
	return keys, nil
}

// createPreflight checks template, bridge and storage through the API.
func (m *Manager) createPreflight(ctx context.Context, c proxmox.Client, h config.Host) error {
	d := m.Cfg.Defaults
	ok, err := c.TemplateExists(ctx, h.Node, d.Template)
	if err != nil {
		return &errs.Error{Kind: errs.Preflight, Err: err}
	}
	if !ok {
		return errs.Preflightf("template %s not found on %s: download it with `pveam download` on the host", d.Template, h.Name)
	}
	bridges, err := c.Bridges(ctx, h.Node)
	if err != nil {
		return &errs.Error{Kind: errs.Preflight, Err: err}
	}
	if !slices.Contains(bridges, d.Bridge) {
		return errs.Preflightf("bridge %s not found on %s (have %s)", d.Bridge, h.Name, strings.Join(bridges, ", "))
	}
	storages, err := c.Storages(ctx, h.Node)
	if err != nil {
		return &errs.Error{Kind: errs.Preflight, Err: err}
	}
	for _, s := range storages {
		if s.Name == d.Storage {
			if !s.Active || !s.Has("rootdir") {
				return errs.Preflightf("storage %s on %s is inactive or does not hold container volumes (rootdir)", d.Storage, h.Name)
			}
			return nil
		}
	}
	return errs.Preflightf("storage %s not found on %s", d.Storage, h.Name)
}

// incusPreflight checks the remote answers and has the pool and network.
func (m *Manager) incusPreflight(ctx context.Context, h config.Host) error {
	c, err := m.incusClient(h)
	if err != nil {
		return err
	}
	ok, err := c.HasStoragePool(ctx, h.StoragePool)
	if err != nil {
		return errs.Preflightf("incus remote %s (host %s) did not answer: %v; check `incus remote list` and `incus info %s:`", h.Remote, h.Name, err, h.Remote)
	}
	if !ok {
		return errs.Preflightf("storage pool %s not found on incus remote %s (see `incus storage list %s:`)", h.StoragePool, h.Remote, h.Remote)
	}
	if h.Network != "" {
		ok, err := c.HasNetwork(ctx, h.Network)
		if err != nil {
			return &errs.Error{Kind: errs.Preflight, Err: err}
		}
		if !ok {
			return errs.Preflightf("network %s not found on incus remote %s (see `incus network list %s:`)", h.Network, h.Remote, h.Remote)
		}
	}
	return nil
}

// instanceOutput checks that tofu created the Incus instance it was asked for.
func instanceOutput(ctx context.Context, tf tofu.Tofu, dir, name string) error {
	out, err := tf.Output(ctx, dir)
	if err != nil {
		return err
	}
	var got string
	if raw, ok := out["name"]; !ok || json.Unmarshal(raw, &got) != nil || got != name {
		return fmt.Errorf("tofu output in %s does not name instance %s", dir, name)
	}
	return nil
}

func vmidOutput(ctx context.Context, tf tofu.Tofu, dir string) (int, error) {
	out, err := tf.Output(ctx, dir)
	if err != nil {
		return 0, err
	}
	var vmid int
	if raw, ok := out["vmid"]; !ok || json.Unmarshal(raw, &vmid) != nil || vmid <= 0 {
		return 0, fmt.Errorf("tofu output in %s has no numeric vmid", dir)
	}
	return vmid, nil
}

// bootstrap prepares the container (keyctl, /dev/net/tun) and runs the
// bootstrap script in it. The auth key travels only on the script's stdin.
// On Incus, nesting and /dev/net/tun are part of the tofu config already.
func (m *Manager) bootstrap(ctx context.Context, h config.Host, md *Metadata, keys []string) (string, error) {
	if !h.IsIncus() {
		host := m.Host(h)
		var opts []string
		if md.Docker {
			opts = append(opts, "--features", "nesting=1,keyctl=1")
		}
		if md.Tun == config.TunDevice {
			opts = append(opts, "--dev0", "/dev/net/tun")
		}
		if len(opts) > 0 {
			if err := host.Set(ctx, proxmox.TypeLXC, md.VMID, opts...); err != nil {
				return "", err
			}
			if err := host.Reboot(ctx, proxmox.TypeLXC, md.VMID); err != nil {
				return "", err
			}
		}
	}
	g, err := m.guestFor(h, md)
	if err != nil {
		return "", err
	}
	payload, err := bootstrap.Payload(bootstrap.Params{
		User: m.Cfg.User, Keys: keys, Hostname: md.Name, Tag: m.Cfg.Tailscale.Tag,
		Tun: md.Tun, Docker: md.Docker, TSSSH: m.Cfg.Tailscale.SSHEnabled(), Locales: m.Cfg.GuestLocales, AuthKey: m.Secrets.AuthKey,
	})
	if err != nil {
		return "", err
	}
	m.logf("bootstrapping %s (%s); this installs packages and takes a few minutes", md.Name, md.guestRef(h))
	var out bytes.Buffer
	if err := g.Exec(ctx, bytes.NewReader(payload), &out, "bash", "-s"); err != nil {
		return "", err
	}
	return bootstrap.ParseResult(out.String())
}

// writeMainTF renders the env's tofu config for host and vmid.
func (m *Manager) writeMainTF(dir string, md *Metadata, host config.Host, vmid int, keys []string) error {
	if host.IsIncus() {
		mainTF, err := tfgen.GenerateIncus(tfgen.Incus{
			Name: md.Name, Remote: host.Remote, Project: host.Project, Image: host.Image,
			Pool: host.StoragePool, Network: host.Network, CPU: md.CPU, RAM: md.RAM, Disk: md.Disk,
			Docker: md.Docker, TunDevice: md.Tun == config.TunDevice,
		})
		if err != nil {
			return err
		}
		return writeFileAtomic(filepath.Join(dir, "main.tf.json"), mainTF)
	}
	storage := md.Storage
	if storage == "" {
		storage = m.Cfg.Defaults.Storage
	}
	mainTF, err := tfgen.Generate(tfgen.LXC{
		Name: md.Name, VMID: vmid, Node: host.Node, Endpoint: host.Endpoint, Insecure: host.Insecure,
		Template: m.Cfg.Defaults.Template, Storage: storage, Bridge: m.Cfg.Defaults.Bridge,
		CPU: md.CPU, RAM: md.RAM, Disk: md.Disk, Keys: keys,
		Docker: md.Docker, TunDevice: md.Tun == config.TunDevice,
	})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "main.tf.json"), mainTF)
}
