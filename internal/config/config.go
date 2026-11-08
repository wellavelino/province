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
}

// Host is one standalone Proxmox VE node.
type Host struct {
	Name     string `yaml:"name"`
	Node     string `yaml:"node"`
	Endpoint string `yaml:"endpoint"`
	Insecure bool   `yaml:"insecure"`
	SSH      string `yaml:"ssh"` // ssh destination (an ~/.ssh/config alias works)
	Sudo     bool   `yaml:"sudo"`
	DumpDir  string `yaml:"dump_dir"`
	// TokenEnv names the env var holding this host's API token (standalone
	// hosts each have their own). Default PROXMOX_VE_API_TOKEN.
	TokenEnv string `yaml:"token_env"`
}

const (
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
		if c.Hosts[i].DumpDir == "" {
			c.Hosts[i].DumpDir = DefaultDumpDir
		}
		if c.Hosts[i].TokenEnv == "" {
			c.Hosts[i].TokenEnv = DefaultTokenEnv
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
