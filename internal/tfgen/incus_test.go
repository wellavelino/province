package tfgen

import (
	"bytes"
	"encoding/json"
	"net/netip"
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
	isolated := incusBase()
	isolated.Network, isolated.Isolated, isolated.Gateways = "incusbr0", true, []string{"10.148.12.1", "fd42:4c6f:2f3a:1b2c::1"}
	for name, i := range map[string]Incus{"incus-default": incusBase(), "incus-full": full, "incus-vm": vm, "incus-isolated": isolated} {
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
	in := f.Resource.Instance["env"]
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

func TestIncusIsolated(t *testing.T) {
	i := incusBase()
	i.Project, i.Network, i.Isolated, i.Gateways = "dev", "incusbr0", true, []string{"10.148.12.1"}
	i.TunDevice = true
	b, err := GenerateIncus(i)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Resource struct {
			Instance map[string]struct {
				Device []struct {
					Name       string            `json:"name"`
					Properties map[string]string `json:"properties"`
				} `json:"device"`
				DependsOn []string `json:"depends_on"`
			} `json:"incus_instance"`
			ACL map[string]struct {
				Name    string `json:"name"`
				Remote  string `json:"remote"`
				Project string `json:"project"`
				Egress  []struct {
					Action, Protocol, Destination string
					Port                          string `json:"destination_port"`
				} `json:"egress"`
				Ingress any `json:"ingress"`
			} `json:"incus_network_acl"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	acl, ok := m.Resource.ACL["isolated"]
	if !ok || acl.Name != "province-demo" || acl.Remote != "box" || acl.Project != "dev" || acl.Ingress != nil {
		t.Fatalf("acl = %+v", acl)
	}
	env := m.Resource.Instance["env"]
	if len(env.DependsOn) != 1 || env.DependsOn[0] != IncusACLAddress {
		t.Errorf("depends_on = %v", env.DependsOn)
	}
	nic := env.Device[1]
	want := map[string]string{"name": "eth0", "network": "incusbr0", "security.acls": "province-demo",
		"security.acls.default.egress.action": "drop", "security.acls.default.ingress.action": "allow"}
	if nic.Name != "eth0" || len(nic.Properties) != len(want) {
		t.Fatalf("nic = %+v", nic)
	}
	for k, v := range want {
		if nic.Properties[k] != v {
			t.Errorf("nic %s = %q, want %q", k, nic.Properties[k], v)
		}
	}
	// Only allow rules: Incus applies drop rules before allow rules, so one
	// would override the gateway's DNS.
	var dns []string
	for _, r := range acl.Egress {
		if r.Action != "allow" {
			t.Errorf("rule %+v is not an allow rule", r)
		}
		if r.Protocol != "" {
			dns = append(dns, r.Protocol+" "+r.Destination+" "+r.Port)
		}
	}
	if strings.Join(dns, "; ") != "udp 10.148.12.1 53,67,547; tcp 10.148.12.1 53" {
		t.Errorf("gateway rules = %q", dns)
	}

	for _, bad := range []Incus{{Name: "demo", Isolated: true, Gateways: []string{"10.0.0.1"}}, {Name: "demo", Network: "incusbr0", Isolated: true}} {
		if _, err := GenerateIncus(bad); err == nil {
			t.Errorf("isolated without network or gateways generated: %+v", bad)
		}
	}
	if b, _ := GenerateIncus(incusBase()); strings.Contains(string(b), "incus_network_acl") || strings.Contains(string(b), "depends_on") {
		t.Error("ACL generated for an env that is not isolated")
	}
}

// TestIsolatedAllowedIsComplement checks that isolatedAllowed and
// IsolatedLAN together cover every address exactly once.
func TestIsolatedAllowedIsComplement(t *testing.T) {
	type span struct{ lo, hi netip.Addr }
	var spans []span
	for _, r := range isolatedAllowed {
		lo, hi, _ := strings.Cut(r, "-")
		spans = append(spans, span{netip.MustParseAddr(lo), netip.MustParseAddr(hi)})
	}
	for _, c := range IsolatedLAN {
		p := netip.MustParsePrefix(c)
		spans = append(spans, span{p.Addr(), lastAddr(p)})
	}
	for _, fam := range []struct{ first, last netip.Addr }{
		{netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("255.255.255.255")},
		{netip.MustParseAddr("::"), netip.MustParseAddr("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")},
	} {
		var in []span
		for _, s := range spans {
			if s.lo.Is4() == fam.first.Is4() {
				in = append(in, s)
			}
		}
		next := fam.first
		for range in {
			found := false
			for _, s := range in {
				if s.lo == next {
					if s.hi.Less(s.lo) {
						t.Fatalf("range %v-%v is backwards", s.lo, s.hi)
					}
					found, next = true, s.hi.Next()
					break
				}
			}
			if !found {
				t.Fatalf("no range starts at %v (gap or overlap)", next)
			}
		}
		if next.IsValid() {
			t.Fatalf("ranges end before %v", next)
		}
	}
	for _, keep := range []string{"fe80::1", "ff02::2", "100.100.100.100", "8.8.8.8"} {
		a := netip.MustParseAddr(keep)
		for _, c := range IsolatedLAN {
			if netip.MustParsePrefix(c).Contains(a) {
				t.Errorf("%s is in the LAN ranges (%s)", keep, c)
			}
		}
	}
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 1 << (7 - i%8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}
