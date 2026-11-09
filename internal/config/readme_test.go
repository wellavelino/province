package config

import (
	"os"
	"strings"
	"testing"
)

// readmeYAML returns the first yaml block after heading in the README.
func readmeYAML(t *testing.T, heading string) string {
	t.Helper()
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "\n"+heading+"\n")
	if !ok {
		t.Fatalf("README has no %q heading", heading)
	}
	_, rest, ok = strings.Cut(rest, "```yaml\n")
	if !ok {
		t.Fatalf("no yaml block after %q", heading)
	}
	block, _, _ := strings.Cut(rest, "```")
	return block
}

// TestReadmeSiteConfigsLoad keeps the README's example site configs valid.
func TestReadmeSiteConfigsLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, err := Load(writeYAML(t, readmeYAML(t, "## Site config reference")))
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	if len(c.ProxmoxHosts()) != 2 || len(c.Hosts) != 3 {
		t.Errorf("reference hosts = %+v", c.Hosts)
	}
	if _, err := c.ResolveRecipe(c.Defaults.Recipe); err != nil {
		t.Errorf("reference defaults.recipe: %v", err)
	}

	start := readmeYAML(t, "### 4. Write a site config")
	c, err = Load(writeYAML(t, start))
	if err != nil {
		t.Fatalf("getting started: %v", err)
	}
	if h, err := c.DefaultHost(); err != nil || h.Name != "pve1" {
		t.Errorf("getting started default host = %+v, %v", h, err)
	}
	// The commented Incus alternative works too, without the Proxmox defaults.
	incus := strings.Replace(start, "  - { name: pve1", "  # - { name: pve1", 1)
	incus = strings.Replace(incus, "  # - { name: box", "  - { name: box", 1)
	for _, k := range []string{"  storage: local-lvm\n", "  bridge: vmbr0\n", "  template: local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst\n"} {
		incus = strings.Replace(incus, k, "", 1)
	}
	c, err = Load(writeYAML(t, incus))
	if err != nil {
		t.Fatalf("getting started, incus variant: %v", err)
	}
	if h, _ := c.DefaultHost(); !h.IsIncus() {
		t.Errorf("incus variant default host = %+v", h)
	}
}
