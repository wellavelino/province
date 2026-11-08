package tfgen

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func base() LXC {
	return LXC{
		Name:     "demo",
		VMID:     105,
		Node:     "pve1",
		Endpoint: "https://pve1.example:8006",
		Insecure: true,
		Template: "local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst",
		Storage:  "local-lvm",
		Bridge:   "vmbr0",
		CPU:      2,
		RAM:      2048,
		Disk:     16,
		Keys:     []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk+/9Qx= dev@laptop"},
	}
}

func TestGolden(t *testing.T) {
	docker := base()
	docker.Docker = true
	tun := base()
	tun.TunDevice = true
	for name, l := range map[string]LXC{"default": base(), "docker": docker, "tun-device": tun} {
		got, err := Generate(l)
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
		again, _ := Generate(l)
		if !bytes.Equal(got, again) {
			t.Errorf("%s: output not deterministic", name)
		}
	}
}

func generic(t *testing.T, l LXC) map[string]any {
	t.Helper()
	b, err := Generate(l)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func container0(m map[string]any) map[string]any {
	return m["resource"].(map[string]any)["proxmox_virtual_environment_container"].(map[string]any)["env"].(map[string]any)
}

func TestInvariants(t *testing.T) {
	variants := []LXC{base(), base(), base(), base()}
	variants[1].Docker = true
	variants[2].TunDevice = true
	variants[3].Docker, variants[3].TunDevice = true, true
	for _, l := range variants {
		b, _ := Generate(l)
		s := string(b)
		// device passthrough needs root@pam; it is set over SSH, never here.
		if strings.Contains(s, `"device_passthrough": {`) || strings.Contains(s, `"device_passthrough": [`) {
			t.Error("device_passthrough block generated")
		}
		for _, banned := range []string{"api_token", "password", "TS_AUTHKEY", "tskey"} {
			if strings.Contains(s, banned) {
				t.Errorf("generated config contains %q", banned)
			}
		}
		m := generic(t, l)
		rp := m["terraform"].(map[string]any)["required_providers"].(map[string]any)["proxmox"].(map[string]any)
		if rp["source"] != "bpg/proxmox" || rp["version"] != ProviderVersion {
			t.Errorf("provider pin = %v", rp)
		}
		if v := m["output"].(map[string]any)["vmid"].(map[string]any)["value"]; v != "${proxmox_virtual_environment_container.env.vm_id}" {
			t.Errorf("output.vmid = %v", v)
		}
		c := container0(m)
		if c["vm_id"] != 105.0 {
			t.Errorf("vm_id = %v, want explicit 105", c["vm_id"])
		}
		ipc := c["initialization"].(map[string]any)["ip_config"].([]any)
		if ipc[0].(map[string]any)["ipv4"].(map[string]any)["address"] != "dhcp" {
			t.Errorf("ip_config = %v", ipc)
		}
		feats := c["features"].(map[string]any)
		if feats["nesting"] != true {
			t.Errorf("nesting off: %v", feats)
		}
		if _, has := feats["keyctl"]; has {
			t.Errorf("keyctl generated (an API token may only change nesting): %v", feats)
		}
		var want []any
		if l.Docker {
			want = append(want, "features")
		}
		if l.TunDevice {
			want = append(want, "device_passthrough")
		}
		var got []any
		if lc, ok := c["lifecycle"].(map[string]any); ok {
			got = lc["ignore_changes"].([]any)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("ignore_changes = %v, want %v (docker=%v tun=%v)", got, want, l.Docker, l.TunDevice)
		}
	}
}

func TestOSType(t *testing.T) {
	cases := map[string]string{
		"local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst":     "debian",
		"local:vztmpl/ubuntu-24.04-standard_24.04-2_amd64.tar.zst": "ubuntu",
		"local:vztmpl/alpine-3.20-default_20240908_amd64.tar.xz":   "unmanaged",
		"nas:vztmpl/custom.tar.zst":                                "unmanaged",
	}
	for in, want := range cases {
		if got := OSType(in); got != want {
			t.Errorf("OSType(%q) = %q, want %q", in, got, want)
		}
	}
}
