package config

import (
	"os"
	"strings"
	"testing"
)

// TestReadmeSiteConfigLoads keeps the README's example site config valid.
func TestReadmeSiteConfigLoads(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "## Site config")
	if !ok {
		t.Fatal("README has no Site config section")
	}
	_, rest, ok = strings.Cut(rest, "```yaml\n")
	if !ok {
		t.Fatal("Site config section has no yaml block")
	}
	block, _, _ := strings.Cut(rest, "```")
	t.Setenv("HOME", t.TempDir())
	c, err := Load(writeYAML(t, block))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ProxmoxHosts()) != 2 || len(c.Hosts) != 3 {
		t.Errorf("hosts = %+v", c.Hosts)
	}
	if _, err := c.ResolveRecipe(c.Defaults.Recipe); err != nil {
		t.Errorf("defaults.recipe: %v", err)
	}
}
