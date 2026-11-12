package tfgen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func incusBase() Incus {
	return Incus{Name: "demo", Remote: "box", Image: "images:debian/13", Pool: "default", CPU: 2, RAM: 2048, Disk: 16}
}

func TestIncusGolden(t *testing.T) {
	full := incusBase()
	full.Project, full.Network, full.Docker, full.TunDevice = "dev", "incusbr0", true, true
	vm := full
	vm.VM = true
	for name, i := range map[string]Incus{"incus-default": incusBase(), "incus-full": full, "incus-vm": vm} {
		got, err := GenerateIncus(i)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("testdata", name+".golden.json")
		if *update {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: output differs from %s (run go test ./internal/tfgen -update after checking)\n%s", name, path, got)
		}
	}
}

// TestIncusVM: a VM is a virtual-machine instance without the container
// keys Docker and tailscale.tun: device would add.
func TestIncusVM(t *testing.T) {
	i := incusBase()
	i.VM, i.Docker, i.TunDevice = true, true, true
	b, err := GenerateIncus(i)
	if err != nil {
		t.Fatal(err)
	}
	var f incusFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	in := f.Resource["incus_instance"]["env"]
	if in.Type != "virtual-machine" {
		t.Errorf("type = %q", in.Type)
	}
	for k := range in.Config {
		if strings.HasPrefix(string(k), "security.") {
			t.Errorf("VM config has %s", k)
		}
	}
	if len(in.Device) != 1 || in.Device[0].Name != "root" || in.Device[0].Properties["size"] != "16GiB" {
		t.Errorf("devices = %+v", in.Device)
	}
}

func TestIncusInvariants(t *testing.T) {
	for _, docker := range []bool{false, true} {
		i := incusBase()
		i.Docker = docker
		b, err := GenerateIncus(i)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"token", "password", "tskey", "client.key"} {
			if strings.Contains(string(b), banned) {
				t.Errorf("generated config contains %q", banned)
			}
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		rp := m["terraform"].(map[string]any)["required_providers"].(map[string]any)["incus"].(map[string]any)
		if rp["source"] != "lxc/incus" || rp["version"] != IncusProviderVersion {
			t.Errorf("provider pin = %v", rp)
		}
		in := m["resource"].(map[string]any)["incus_instance"].(map[string]any)["env"].(map[string]any)
		if in["remote"] != "box" || in["type"] != "container" || in["name"] != "demo" {
			t.Errorf("instance = %v", in)
		}
		if _, has := in["project"]; has {
			t.Errorf("project set without one configured: %v", in["project"])
		}
		cfg := in["config"].(map[string]any)
		if cfg["limits.memory"] != "2048MiB" || cfg["limits.cpu"] != "2" {
			t.Errorf("limits = %v", cfg)
		}
		if (cfg["security.nesting"] == "true") != docker {
			t.Errorf("docker=%v but nesting = %v", docker, cfg["security.nesting"])
		}
		root := in["device"].([]any)[0].(map[string]any)
		if root["name"] != "root" || root["properties"].(map[string]any)["size"] != "16GiB" {
			t.Errorf("root device = %v", root)
		}
	}
}
