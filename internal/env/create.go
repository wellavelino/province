package env

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/wellavelino/province/internal/bootstrap"
	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
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
	case o.Apply && md.TofuOutOfSync:
		// The recorded guest moved but tofu still tracks the old one.
		return nil, md.refuseOutOfSync("tofu apply")
	case md.Status == StatusBootstrapped:
		return nil, errs.Refusedf("env %s already exists on %s (%s); destroy it first", o.Name, md.Host, md.guestLabel())
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

	b := m.backendFor(host)
	if err := b.check(); err != nil {
		return nil, err
	}
	tf, err := m.Tofu(host)
	if err != nil {
		return nil, err
	}
	keys, err := m.readKeys()
	if err != nil {
		return nil, err
	}
	if md.created() && md.Status != StatusPlanned {
		// tofu apply and the bootstrap act on the recorded guest.
		if _, err := b.verify(ctx, md); err != nil {
			return nil, err
		}
	}
	p, err := b.plan(ctx, md)
	if err != nil {
		return nil, err
	}
	for _, s := range p.Warnings {
		m.logf("warning: %s", s)
	}

	if err := mkdirAll(dir); err != nil {
		return nil, err
	}
	if err := m.writeMainTF(dir, md, host, p.VMID, keys); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, ".gitignore"), []byte(".terraform/\n")); err != nil {
		return nil, err
	}
	if err := m.writeMetadata(dir, md); err != nil {
		return nil, err
	}
	res := &CreateResult{Name: o.Name, Host: host.Name, Backend: host.BackendName(), Dir: dir, VMID: p.VMID, Recipe: recipe,
		ExposePort: md.ExposePort, Warnings: append([]string{}, p.Warnings...)}

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
	out, err := tf.Output(ctx, dir)
	if err != nil {
		return nil, err
	}
	if err := b.adopt(out, md); err != nil {
		return nil, fmt.Errorf("tofu output in %s has %w", dir, err)
	}
	md.Status, md.TailscaleIP, md.TailscaleDeviceID = StatusApplied, "", ""
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
	if w := m.recordDevice(ctx, md); w != "" {
		m.logf("warning: %s", w)
		res.Warnings = append(res.Warnings, w)
	}
	if err := m.writeMetadata(dir, md); err != nil {
		return nil, err
	}
	g, err := b.guest(md)
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

// recordDevice looks the env's node up in the Tailscale API (name, tag and
// the address bootstrap reported must agree) and records its ID, so destroy
// deletes that device and no namesake. It returns a warning when it could
// not; without TS_API_KEY it does nothing.
func (m *Manager) recordDevice(ctx context.Context, md *Metadata) string {
	if m.Tailnet == nil {
		return ""
	}
	id, err := m.Tailnet.FindDevice(ctx, md.Name, md.TailscaleIP)
	switch {
	case err != nil:
		return fmt.Sprintf("tailnet device ID of %s not recorded: %v; destroy will delete its node only if the device named %s holds %s", md.Name, err, md.Name, md.TailscaleIP)
	case id == "":
		return fmt.Sprintf("no tailnet device named %s with tag %s holds %s, so its ID is not recorded; destroy will delete its node only if one does then", md.Name, m.Cfg.Tailscale.Tag, md.TailscaleIP)
	}
	md.TailscaleDeviceID = id
	return ""
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

// bootstrap applies what tofu could not set, then runs the bootstrap
// script in the container. The auth key travels only on the script's
// stdin, and only to a guest that has not joined the tailnet yet: a re-run
// on a joined guest (one whose bootstrap failed after `tailscale up`)
// sends none.
func (m *Manager) bootstrap(ctx context.Context, h config.Host, md *Metadata, keys []string) (string, error) {
	b := m.backendFor(h)
	if err := b.prepare(ctx, md); err != nil {
		return "", err
	}
	g, err := b.guest(md)
	if err != nil {
		return "", err
	}
	// No address (no tailscale yet, not logged in) or a failed exec, which
	// the bootstrap itself will then report, means not joined: send the key.
	var probe strings.Builder
	joined := g.Exec(ctx, nil, &probe, bootstrap.ProbeArgv()...) == nil && bootstrap.Joined(probe.String())
	authKey := m.Secrets.AuthKey
	if joined {
		m.logf("%s is already on the tailnet; not sending the auth key again", md.Name)
		authKey = ""
	}
	payload, err := bootstrap.Payload(bootstrap.Params{
		User: m.Cfg.User, Keys: keys, Hostname: md.Name, Tag: m.Cfg.Tailscale.Tag,
		Tun: md.Tun, Docker: md.Docker, TSSSH: m.Cfg.Tailscale.SSHEnabled(), Locales: m.Cfg.GuestLocales,
		AuthKey: authKey, Joined: joined,
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

// writeMainTF renders the env's tofu config for host and, on Proxmox, vmid.
func (m *Manager) writeMainTF(dir string, md *Metadata, host config.Host, vmid int, keys []string) error {
	mainTF, err := m.backendFor(host).mainTF(md, keys, planned{VMID: vmid})
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "main.tf.json"), mainTF)
}
