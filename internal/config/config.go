// Package config loads the site file (province.yaml), which lives in the
// user's private repo, never in this one.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is the site file.
type Config struct {
	StateDir      string    `yaml:"state_dir"`
	User          string    `yaml:"user"`
	SSHPublicKeys []string  `yaml:"ssh_public_keys"`
	Tailscale     Tailscale `yaml:"tailscale"`
	Defaults      Defaults  `yaml:"defaults"`
	Hosts         []Host    `yaml:"hosts"`
	// GuestLocales are generated in each guest so locales forwarded by
	// ssh clients work. Default [en_US.UTF-8]; [] generates none.
	GuestLocales []string `yaml:"guest_locales"`
	// VMIDStart is the lowest VMID env create picks (default 100).
	VMIDStart int `yaml:"vmid_start"`
	// Recipes say what an env has inside, by name (see Recipe).
	Recipes map[string]Recipe `yaml:"recipes"`

	userFromOS bool // User was not set and came from the OS user
}

// currentUser is user.Current, replaceable in tests.
var currentUser = user.Current

// Tailscale configures how guests join the tailnet.
type Tailscale struct {
	Tag string `yaml:"tag"` // e.g. "tag:dev"
	Tun string `yaml:"tun"` // userspace (default) | device
	// SSH turns on Tailscale SSH in each guest (tailnet policy decides who
	// logs in). Default true; the user's authorized_keys are installed
	// either way as the LAN fallback.
	SSH *bool `yaml:"ssh"`
}

// SSHEnabled reports whether Tailscale SSH is on (the default).
func (t Tailscale) SSHEnabled() bool { return t.SSH == nil || *t.SSH }

// Defaults for env create.
type Defaults struct {
	Host     string `yaml:"host"`
	Storage  string `yaml:"storage"`
	Bridge   string `yaml:"bridge"`
	Template string `yaml:"template"`
	CPU      int    `yaml:"cpu"`
	RAM      int    `yaml:"ram"`  // MiB
	Disk     int    `yaml:"disk"` // GiB
	// Recipe applied by env create when --recipe is not given; empty or
	// "none" applies none.
	Recipe string `yaml:"recipe"`
}

// Recipe is what an env should have inside, applied after bootstrap and
// again by env provision. Every step is safe to repeat.
type Recipe struct {
	// Include names recipes applied first; this recipe's values win.
	Include []string `yaml:"include"`
	// Packages are installed with apt-get.
	Packages []string `yaml:"packages"`
	// Tools maps a mise tool name to its version, e.g. go: "1.24".
	Tools map[string]string `yaml:"tools"`
	// Dotfiles is an https git URL cloned into ~/.dotfiles; its
	// install.sh, if any, runs as the user.
	Dotfiles string `yaml:"dotfiles"`
	// Run holds commands run as root after everything else.
	Run []string `yaml:"run"`
}

// Host is one place envs run: a standalone Proxmox VE node (the default)
// or an Incus server.
type Host struct {
	Name string `yaml:"name"`
	// Backend is proxmox (default) or incus.
	Backend  string `yaml:"backend"`
	Node     string `yaml:"node"`
	Endpoint string `yaml:"endpoint"`
	Insecure bool   `yaml:"insecure"`
	SSH      string `yaml:"ssh"` // ssh destination (an ~/.ssh/config alias works)
	Sudo     bool   `yaml:"sudo"`
	DumpDir  string `yaml:"dump_dir"`
	// TokenEnv names the env var holding this host's API token (standalone
	// hosts each have their own). Default PROXMOX_VE_API_TOKEN.
	TokenEnv string `yaml:"token_env"`

	// Incus hosts. Remote names an Incus remote already set up with
	// `incus remote add`; province, tofu and the incus CLI authenticate
	// through it, so no secret goes through province.
	Remote      string `yaml:"remote"`
	Project     string `yaml:"project"`      // default: the remote's default project
	StoragePool string `yaml:"storage_pool"` // root disk pool (default "default")
	Network     string `yaml:"network"`      // eth0 network; empty keeps the profile's NIC
	Image       string `yaml:"image"`        // default images:debian/13
}

// BackendName is h's backend, proxmox when unset.
func (h Host) BackendName() string {
	if h.Backend == "" {
		return BackendProxmox
	}
	return h.Backend
}

// IsIncus reports whether h is an Incus host.
func (h Host) IsIncus() bool { return h.Backend == BackendIncus }

const (
	BackendProxmox = "proxmox"
	BackendIncus   = "incus"
	// RecipeNone on the command line or in defaults.recipe applies no recipe.
	RecipeNone = "none"
	// DefaultIncusImage and DefaultIncusPool apply to incus hosts.
	DefaultIncusImage = "images:debian/13"
	DefaultIncusPool  = "default"

	TunUserspace   = "userspace"
	TunDevice      = "device"
	DefaultDumpDir = "/var/lib/vz/dump"
	// DefaultTokenEnv is the token variable of hosts without token_env.
	DefaultTokenEnv = "PROXMOX_VE_API_TOKEN"
)

// Load reads, defaults and validates the site file at path.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("no site config: pass --config or set PROVINCE_CONFIG")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("site config: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("site config %s: %w", path, err)
	}
	if err := c.applyDefaults(); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("site config %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() error {
	var err error
	if c.StateDir, err = expandHome(c.StateDir); err != nil {
		return err
	}
	for i, k := range c.SSHPublicKeys {
		if c.SSHPublicKeys[i], err = expandHome(k); err != nil {
			return err
		}
	}
	if c.User == "" {
		u, err := currentUser()
		if err != nil {
			return fmt.Errorf("site config: no user set and the OS user is unknown: %w", err)
		}
		c.User, c.userFromOS = u.Username, true
	}
	if c.VMIDStart == 0 {
		c.VMIDStart = 100
	}
	if c.GuestLocales == nil {
		c.GuestLocales = []string{"en_US.UTF-8"}
	}
	if c.Tailscale.Tun == "" {
		c.Tailscale.Tun = TunUserspace
	}
	if c.Defaults.CPU == 0 {
		c.Defaults.CPU = 2
	}
	if c.Defaults.RAM == 0 {
		c.Defaults.RAM = 2048
	}
	if c.Defaults.Disk == 0 {
		c.Defaults.Disk = 16
	}
	for i := range c.Hosts {
		h := &c.Hosts[i]
		if h.Backend == "" {
			h.Backend = BackendProxmox
		}
		if h.Backend != BackendProxmox {
			if h.Backend == BackendIncus {
				if h.Image == "" {
					h.Image = DefaultIncusImage
				}
				if h.StoragePool == "" {
					h.StoragePool = DefaultIncusPool
				}
			}
			continue
		}
		if h.DumpDir == "" {
			h.DumpDir = DefaultDumpDir
		}
		if h.TokenEnv == "" {
			h.TokenEnv = DefaultTokenEnv
		}
	}
	return nil
}

// expandHome replaces a leading "~" or "~/" with the user's home directory.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %s: %w", p, err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// HostByName returns the host called name.
func (c *Config) HostByName(name string) (Host, error) {
	for _, h := range c.Hosts {
		if h.Name == name {
			return h, nil
		}
	}
	names := make([]string, len(c.Hosts))
	for i, h := range c.Hosts {
		names[i] = h.Name
	}
	return Host{}, fmt.Errorf("unknown host %q (configured: %s)", name, strings.Join(names, ", "))
}

// DefaultHost returns defaults.host, or the only host when there is one.
func (c *Config) DefaultHost() (Host, error) {
	if c.Defaults.Host != "" {
		return c.HostByName(c.Defaults.Host)
	}
	if len(c.Hosts) == 1 {
		return c.Hosts[0], nil
	}
	return Host{}, errors.New("several hosts configured and no defaults.host: pass --host")
}

// ProxmoxHosts returns the Proxmox hosts, in config order.
func (c *Config) ProxmoxHosts() []Host {
	var hs []Host
	for _, h := range c.Hosts {
		if !h.IsIncus() {
			hs = append(hs, h)
		}
	}
	return hs
}

// RecipeName picks the recipe for an env: the flag, else the recorded one,
// else defaults.recipe. "none" anywhere first in that order means no recipe
// and returns "".
func (c *Config) RecipeName(flag, recorded string) string {
	for _, n := range []string{flag, recorded, c.Defaults.Recipe} {
		if n == RecipeNone {
			return ""
		}
		if n != "" {
			return n
		}
	}
	return ""
}

// ResolveRecipe returns recipe name with its includes merged in: includes
// first, in order, then the recipe itself. Packages and run commands
// accumulate (a package once), tools and dotfiles from later recipes win.
func (c *Config) ResolveRecipe(name string) (Recipe, error) {
	var out Recipe
	seenPkg := map[string]bool{}
	done := map[string]bool{}
	var walk func(n string, stack []string) error
	walk = func(n string, stack []string) error {
		if slices.Contains(stack, n) {
			return fmt.Errorf("recipe %s includes itself (%s)", n, strings.Join(append(stack, n), " -> "))
		}
		r, ok := c.Recipes[n]
		if !ok {
			if len(stack) == 0 && len(c.Recipes) == 0 {
				return fmt.Errorf("unknown recipe %q: the site config has no recipes", n)
			}
			if len(stack) == 0 {
				return fmt.Errorf("unknown recipe %q (configured: %s)", n, strings.Join(c.recipeNames(), ", "))
			}
			return fmt.Errorf("recipe %s includes unknown recipe %q", stack[len(stack)-1], n)
		}
		if done[n] {
			return nil
		}
		for _, inc := range r.Include {
			if err := walk(inc, append(stack, n)); err != nil {
				return err
			}
		}
		done[n] = true
		for _, p := range r.Packages {
			if !seenPkg[p] {
				seenPkg[p] = true
				out.Packages = append(out.Packages, p)
			}
		}
		for t, v := range r.Tools {
			if out.Tools == nil {
				out.Tools = map[string]string{}
			}
			out.Tools[t] = v
		}
		if r.Dotfiles != "" {
			out.Dotfiles = r.Dotfiles
		}
		out.Run = append(out.Run, r.Run...)
		return nil
	}
	if err := walk(name, nil); err != nil {
		return Recipe{}, err
	}
	return out, nil
}

func (c *Config) recipeNames() []string {
	names := make([]string, 0, len(c.Recipes))
	for n := range c.Recipes {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
