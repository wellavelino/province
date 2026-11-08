package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	nameRE   = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	userRE   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	envRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	localeRE = regexp.MustCompile(`^[a-z]{2,3}(_[A-Z]{2})?\.UTF-8$`)
)

// ValidName checks an environment name: a DNS label of lowercase letters,
// digits and hyphens, starting with a letter, at most 63 characters. It is
// used as the guest hostname, the tailnet hostname and a state directory.
func ValidName(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("invalid name %q: use 1-63 lowercase letters, digits or '-', starting with a letter and not ending with '-'", s)
	}
	return nil
}

// Validate checks the loaded config.
func (c *Config) Validate() error {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	if c.StateDir == "" {
		add("state_dir is required")
	}
	if !userRE.MatchString(c.User) || c.User == "root" {
		if c.userFromOS {
			add("user is not set and the OS user %q is not a valid guest user name (lowercase, not root): set user: in the site config", c.User)
		} else {
			add("user %q must be a plain Linux user name (not root)", c.User)
		}
	}
	if c.VMIDStart < 100 || c.VMIDStart > 999999000 {
		add("vmid_start must be between 100 and 999999000, got %d", c.VMIDStart)
	}
	for _, l := range c.GuestLocales {
		if !localeRE.MatchString(l) {
			add("guest_locales: %q is not a UTF-8 locale like en_US.UTF-8", l)
		}
	}
	if len(c.SSHPublicKeys) == 0 {
		add("ssh_public_keys needs at least one public key file")
	}
	if !strings.HasPrefix(c.Tailscale.Tag, "tag:") {
		add("tailscale.tag must look like tag:<name>, got %q", c.Tailscale.Tag)
	}
	if c.Tailscale.Tun != TunUserspace && c.Tailscale.Tun != TunDevice {
		add("tailscale.tun must be %s or %s, got %q", TunUserspace, TunDevice, c.Tailscale.Tun)
	}
	if c.Defaults.Storage == "" || c.Defaults.Bridge == "" || c.Defaults.Template == "" {
		add("defaults.storage, defaults.bridge and defaults.template are required")
	}
	if c.Defaults.CPU < 1 || c.Defaults.RAM < 64 || c.Defaults.Disk < 1 {
		add("defaults.cpu, defaults.ram and defaults.disk must be positive")
	}
	if len(c.Hosts) == 0 {
		add("hosts needs at least one host")
	}
	seen := map[string]bool{}
	for i, h := range c.Hosts {
		where := fmt.Sprintf("hosts[%d]", i)
		if h.Name == "" {
			add("%s: name is required", where)
		} else if seen[h.Name] {
			add("%s: duplicate name %q", where, h.Name)
		}
		seen[h.Name] = true
		if h.Node == "" || h.SSH == "" {
			add("%s: node and ssh are required", where)
		}
		if u, err := url.Parse(h.Endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
			add("%s: endpoint must be an https URL, got %q", where, h.Endpoint)
		}
		if !envRE.MatchString(h.TokenEnv) {
			add("%s: token_env %q is not an environment variable name", where, h.TokenEnv)
		}
		if !strings.HasPrefix(h.DumpDir, "/") {
			add("%s: dump_dir must be an absolute path", where)
		}
	}
	if c.Defaults.Host != "" && !seen[c.Defaults.Host] {
		add("defaults.host %q is not in hosts", c.Defaults.Host)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
