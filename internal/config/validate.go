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
	// Debian package names, optionally pinned: name or name=version.
	packageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+(=[A-Za-z0-9.+:~-]+)?$`)
	// mise tool names, plain (go) or with a backend (npm:prettier, aqua:cli/cli).
	toolRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(:[A-Za-z0-9@._/-]+)?$`)
	versionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]*$`)
	// Incus remote, project, pool and network names.
	incusNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	recipeRE    = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
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
	if len(c.ProxmoxHosts()) > 0 && (c.Defaults.Storage == "" || c.Defaults.Bridge == "" || c.Defaults.Template == "") {
		add("defaults.storage, defaults.bridge and defaults.template are required for Proxmox hosts")
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
		switch h.Backend {
		case BackendProxmox:
			validateProxmoxHost(h, where, add)
		case BackendIncus:
			validateIncusHost(h, where, add)
		default:
			add("%s: backend must be %s or %s, got %q", where, BackendProxmox, BackendIncus, h.Backend)
		}
	}
	if c.Defaults.Host != "" && !seen[c.Defaults.Host] {
		add("defaults.host %q is not in hosts", c.Defaults.Host)
	}
	c.validateRecipes(add)
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateProxmoxHost(h Host, where string, add func(string, ...any)) {
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
	if h.Remote != "" || h.Project != "" || h.StoragePool != "" || h.Network != "" || h.Image != "" {
		add("%s: remote, project, storage_pool, network and image are incus settings; this host's backend is proxmox", where)
	}
	if h.Isolated {
		add("%s: isolated is not supported on proxmox hosts yet (only on incus hosts)", where)
	}
}

func validateIncusHost(h Host, where string, add func(string, ...any)) {
	if h.Remote == "" {
		add("%s: remote is required for an incus host (an Incus remote set up with `incus remote add`)", where)
	}
	for k, v := range map[string]string{"remote": h.Remote, "project": h.Project, "storage_pool": h.StoragePool, "network": h.Network} {
		if v != "" && !incusNameRE.MatchString(v) {
			add("%s: %s %q is not a valid Incus name", where, k, v)
		}
	}
	if h.Isolated && h.Network == "" {
		add("%s: isolated needs network: the Incus bridge network eth0 is attached to (e.g. incusbr0), so the ACL can be set on that NIC", where)
	}
	if strings.ContainsAny(h.Image, " \t\n") {
		add("%s: image %q must not contain spaces", where, h.Image)
	}
	if h.Node != "" || h.Endpoint != "" || h.SSH != "" || h.Insecure || h.Sudo || h.DumpDir != "" || h.TokenEnv != "" {
		add("%s: node, endpoint, ssh, insecure, sudo, dump_dir and token_env are proxmox settings; this host's backend is incus", where)
	}
}

func (c *Config) validateRecipes(add func(string, ...any)) {
	for _, name := range c.recipeNames() {
		r := c.Recipes[name]
		where := "recipes." + name
		if !recipeRE.MatchString(name) || name == RecipeNone {
			add("%s: recipe names are lowercase letters, digits, '-' or '_', starting with a letter, and not %q", where, RecipeNone)
		}
		for _, p := range r.Packages {
			if !packageRE.MatchString(p) {
				add("%s: package %q is not a Debian package name", where, p)
			}
		}
		for t, v := range r.Tools {
			if !toolRE.MatchString(t) {
				add("%s: tool %q is not a mise tool name", where, t)
			}
			if !versionRE.MatchString(v) {
				add("%s: tool %s has version %q; use a version like 1.24, lts or latest", where, t, v)
			}
		}
		if r.Dotfiles != "" {
			if u, err := url.Parse(r.Dotfiles); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				add("%s: dotfiles must be an https git URL without credentials, got %q", where, r.Dotfiles)
			}
		}
		for i, cmd := range r.Run {
			if strings.TrimSpace(cmd) == "" {
				add("%s: run[%d] is empty", where, i)
			}
		}
		if _, err := c.ResolveRecipe(name); err != nil {
			add("%s: %v", where, err)
		}
	}
	if d := c.Defaults.Recipe; d != "" && d != RecipeNone {
		if _, ok := c.Recipes[d]; !ok {
			add("defaults.recipe %q is not in recipes", d)
		}
	}
}
