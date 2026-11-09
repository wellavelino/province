package config

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

const incusSite = `state_dir: /srv/province
user: dev
ssh_public_keys: [/etc/keys/a.pub]
tailscale: { tag: "tag:dev" }
hosts:
  - { name: box, backend: incus, remote: box }
`

const recipeSite = incusSite + `defaults: { recipe: base }
recipes:
  base:
    packages: [git, tmux]
    dotfiles: https://github.com/you/dotfiles
    tools: { go: "1.23" }
  go:
    include: [base]
    packages: [git, make]
    tools: { go: "1.24", golangci-lint: latest }
    run: ["echo go"]
  web:
    include: [base, go]
    tools: { node: "22" }
    run: ["echo web"]
`

func TestIncusOnlySiteNeedsNoProxmoxDefaults(t *testing.T) {
	c, err := Load(writeYAML(t, incusSite))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := c.HostByName("box")
	if !h.IsIncus() || h.Image != DefaultIncusImage || h.StoragePool != DefaultIncusPool || h.TokenEnv != "" || h.DumpDir != "" {
		t.Fatalf("box = %+v", h)
	}
	if len(c.ProxmoxHosts()) != 0 {
		t.Fatalf("proxmox hosts = %v", c.ProxmoxHosts())
	}
}

func TestMixedSiteDefaultsBackendToProxmox(t *testing.T) {
	body := strings.Replace(incusSite, "hosts:\n", `defaults: { storage: local-lvm, bridge: vmbr0, template: "local:vztmpl/debian.tar.zst" }
hosts:
  - { name: pve1, node: pve1, endpoint: "https://pve1.example:8006", ssh: pve1 }
`, 1)
	c, err := Load(writeYAML(t, body))
	if err != nil {
		t.Fatal(err)
	}
	pve := c.ProxmoxHosts()
	if len(pve) != 1 || pve[0].Backend != BackendProxmox || pve[0].TokenEnv != DefaultTokenEnv {
		t.Fatalf("proxmox hosts = %+v", pve)
	}
}

func TestBackendRejects(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"unknown backend":      {strings.Replace(incusSite, "backend: incus", "backend: docker", 1), "backend must be"},
		"incus without remote": {strings.Replace(incusSite, ", remote: box", "", 1), "remote is required"},
		"incus with token":     {strings.Replace(incusSite, "remote: box", "remote: box, token_env: T", 1), "proxmox settings"},
		"incus with ssh":       {strings.Replace(incusSite, "remote: box", "remote: box, ssh: box", 1), "proxmox settings"},
		"bad remote name":      {strings.Replace(incusSite, "remote: box", `remote: "box; rm"`, 1), "not a valid Incus name"},
		"proxmox needs defaults": {strings.Replace(incusSite, "{ name: box, backend: incus, remote: box }",
			`{ name: pve1, node: pve1, endpoint: "https://pve1.example:8006", ssh: pve1 }`, 1), "defaults.storage"},
		"proxmox with incus key": {strings.Replace(incusSite, "hosts:\n  - { name: box, backend: incus, remote: box }",
			"defaults: { storage: s, bridge: b, template: t }\nhosts:\n  - { name: pve1, node: pve1, endpoint: \"https://pve1.example:8006\", ssh: pve1, image: x }", 1), "incus settings"},
	}
	for name, c := range cases {
		_, err := Load(writeYAML(t, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want mention of %q", name, err, c.want)
		}
	}
}

func TestResolveRecipeMergesIncludes(t *testing.T) {
	c, err := Load(writeYAML(t, recipeSite))
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.ResolveRecipe("web")
	if err != nil {
		t.Fatal(err)
	}
	// base once (included twice), then go, then web.
	if !slices.Equal(r.Packages, []string{"git", "tmux", "make"}) {
		t.Errorf("packages = %q", r.Packages)
	}
	if want := map[string]string{"go": "1.24", "golangci-lint": "latest", "node": "22"}; !maps.Equal(r.Tools, want) {
		t.Errorf("tools = %v", r.Tools)
	}
	if !slices.Equal(r.Run, []string{"echo go", "echo web"}) || r.Dotfiles != "https://github.com/you/dotfiles" {
		t.Errorf("run = %q dotfiles = %q", r.Run, r.Dotfiles)
	}
	if _, err := c.ResolveRecipe("nope"); err == nil || !strings.Contains(err.Error(), "base, go, web") {
		t.Errorf("unknown recipe: %v", err)
	}
}

func TestRecipeName(t *testing.T) {
	c := &Config{Defaults: Defaults{Recipe: "base"}}
	for _, tc := range []struct{ flag, recorded, want string }{
		{"", "", "base"},
		{"go", "", "go"},
		{"", "web", "web"},
		{"go", "web", "go"},
		{"none", "web", ""},
		{"", "none", ""},
	} {
		if got := c.RecipeName(tc.flag, tc.recorded); got != tc.want {
			t.Errorf("RecipeName(%q, %q) = %q, want %q", tc.flag, tc.recorded, got, tc.want)
		}
	}
	if got := (&Config{Defaults: Defaults{Recipe: "none"}}).RecipeName("", ""); got != "" {
		t.Errorf("defaults none = %q", got)
	}
}

func TestRecipeRejects(t *testing.T) {
	cases := map[string]struct{ recipes, want string }{
		"cycle":           {"a: { include: [b] }\n  b: { include: [a] }", "includes itself"},
		"unknown include": {"a: { include: [zzz] }", `unknown recipe "zzz"`},
		"bad package":     {`a: { packages: ["git; rm -rf /"] }`, "not a Debian package"},
		"bad tool":        {`a: { tools: { "go go": "1" } }`, "not a mise tool"},
		"bad version":     {`a: { tools: { go: "1.24 && rm" } }`, "version"},
		"http dotfiles":   {"a: { dotfiles: http://example.com/d }", "https git URL"},
		"dotfiles creds":  {"a: { dotfiles: \"https://user:pw@example.com/d\" }", "without credentials"},
		"empty run":       {`a: { run: [" "] }`, "run[0] is empty"},
		"reserved name":   {"none: { packages: [git] }", "not \"none\""},
		"unknown key":     {"a: { pkgs: [git] }", "pkgs"},
	}
	for name, c := range cases {
		_, err := Load(writeYAML(t, incusSite+"recipes:\n  "+c.recipes+"\n"))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want mention of %q", name, err, c.want)
		}
	}
	_, err := Load(writeYAML(t, incusSite+"defaults: { recipe: ghost }\n"))
	if err == nil || !strings.Contains(err.Error(), "defaults.recipe") {
		t.Errorf("unknown defaults.recipe: %v", err)
	}
	if _, err := Load(writeYAML(t, incusSite+"defaults: { recipe: none }\n")); err != nil {
		t.Errorf("defaults.recipe none: %v", err)
	}
}

func TestToolNamesWithBackends(t *testing.T) {
	body := incusSite + "recipes:\n  a: { tools: { \"npm:prettier\": latest, \"aqua:cli/cli\": \"2.60.0\", python: \"3.12\" } }\n"
	if _, err := Load(writeYAML(t, body)); err != nil {
		t.Fatal(err)
	}
}
