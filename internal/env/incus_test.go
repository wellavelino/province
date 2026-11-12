package env

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/incus"
	"github.com/wellavelino/province/internal/incus/incustest"
)

// withIncus adds Incus host "box" (remote "box") to the rig.
func withIncus(t *testing.T, r *rig) *incustest.Fake {
	t.Helper()
	r.m.Cfg.Hosts = append(r.m.Cfg.Hosts, config.Host{Name: "box", Backend: config.BackendIncus, Remote: "box",
		Image: config.DefaultIncusImage, StoragePool: "default"})
	f := &incustest.Fake{Name: "box", Journal: r.j, Pools: []string{"default"}, Networks: []string{"incusbr0"},
		ExecOut: "apt noise\nPROVINCE_TS_IP=100.64.0.9\n"}
	r.m.Incus = func(h config.Host) incus.Client {
		if h.Name != "box" {
			t.Errorf("incus client asked for %s", h.Name)
		}
		return f
	}
	r.tofu.Outputs = map[string]json.RawMessage{"name": json.RawMessage(`"api"`)}
	return f
}

func TestIncusCreateApply(t *testing.T) {
	r := newRig(t)
	box := withIncus(t, r)
	res, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box", Docker: true, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := r.dir("api")
	want := []string{"box HasStoragePool default", "tofu Init " + dir, "tofu Apply " + dir, "tofu Output " + dir, "box Exec api sh -c tailscale ip -4 2>/dev/null || true", "box Exec api bash -s"}
	if got := r.j.Entries(); !slices.Equal(got, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if r.apiCalls() != 0 {
		t.Fatal("a Proxmox API was asked about an Incus env")
	}
	md := r.metadata(t, "api")
	if md.Backend != config.BackendIncus || md.Instance != "api" || md.VMID != 0 || md.Status != StatusBootstrapped || md.TailscaleIP != "100.64.0.9" {
		t.Fatalf("metadata = %+v", md)
	}
	if res.Instance != "api" || res.Backend != "incus" || res.SSH != "ssh dev@api" {
		t.Fatalf("result = %+v", res)
	}
	tf, _ := os.ReadFile(filepath.Join(dir, "main.tf.json"))
	if !strings.Contains(string(tf), `"incus_instance"`) || !strings.Contains(string(tf), `"security.nesting": "true"`) {
		t.Fatalf("main.tf.json:\n%s", tf)
	}
	if stdin := box.ExecStdins(); len(stdin) != 1 || !strings.Contains(stdin[0], "TS_AUTHKEY="+authKey) {
		t.Fatal("bootstrap payload did not reach incus exec")
	}
}

func TestIncusPreflight(t *testing.T) {
	r := newRig(t)
	withIncus(t, r)
	r.m.Cfg.Hosts[2].StoragePool = "fast"
	_, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box"})
	if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "storage pool fast") {
		t.Fatalf("pool: %v", err)
	}
	r.m.Cfg.Hosts[2].StoragePool, r.m.Cfg.Hosts[2].Network = "default", "lan"
	_, err = r.m.Create(ctx, CreateOpts{Name: "api", Host: "box"})
	if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "network lan") {
		t.Fatalf("network: %v", err)
	}
}

func TestIncusListShowAndProvisionRefusalWhenStopped(t *testing.T) {
	r := newRig(t)
	box := withIncus(t, r)
	seed(t, r,
		Metadata{Name: "api", Backend: "incus", Host: "box", Instance: "api", Status: StatusBootstrapped},
		Metadata{Name: "gone", Backend: "incus", Host: "box", Instance: "gone", Status: StatusBootstrapped},
		Metadata{Name: "web", Host: "pve1", Node: "pve1", VMID: 105, Status: StatusBootstrapped},
	)
	box.List = []incus.Instance{{Name: "api", Type: "container", Status: "stopped", Env: "api"}}
	r.clients["pve1"].Guests = nil
	views, err := r.m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range views {
		got[v.Name] = v.LiveStatus
	}
	if got["api"] != "stopped" || got["gone"] != LiveMissing || got["web"] != LiveMissing {
		t.Fatalf("live = %v", got)
	}
	v, err := r.m.Show(ctx, "api")
	if err != nil || v.LiveStatus != "stopped" {
		t.Fatalf("show = %+v, %v", v, err)
	}
	_, err = r.m.Provision(ctx, "api", "")
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "incus start box:api") {
		t.Fatalf("provision stopped: %v", err)
	}
}

func TestIncusDestroy(t *testing.T) {
	r := newRig(t)
	box := withIncus(t, r)
	seed(t, r, Metadata{Name: "api", Backend: "incus", Host: "box", Instance: "api", Status: StatusBootstrapped, TailscaleIP: "100.64.0.9"})
	box.List = []incus.Instance{{Name: "api", Type: "container", Status: "running", Env: "api"}}
	r.m.Tailnet = &fakeTailnet{j: r.j, deleted: true}
	res, err := r.m.Destroy(ctx, "api", true)
	if err != nil {
		t.Fatal(err)
	}
	dir := r.dir("api")
	want := []string{"box Instances", "box Instances", "box Exec api tailscale logout", "tailnet delete api", "tofu Init " + dir, "tofu Destroy " + dir}
	if got := r.j.Entries(); !slices.Equal(got, want) {
		t.Fatalf("calls:\n%s", strings.Join(got, "\n"))
	}
	if !res.Destroyed || res.Instance != "api" || r.tofuHosts[0] != "box" {
		t.Fatalf("res = %+v, tofu hosts %v", res, r.tofuHosts)
	}
}

func TestEnvOnHostThatChangedBackendIsRefused(t *testing.T) {
	r := newRig(t)
	withIncus(t, r)
	seed(t, r, Metadata{Name: "api", Host: "box", VMID: 105, Status: StatusBootstrapped}) // recorded as proxmox
	if _, err := r.m.Destroy(ctx, "api", true); errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "backend incus") {
		t.Fatalf("destroy: %v", err)
	}
	if _, err := r.m.Create(ctx, CreateOpts{Name: "api"}); err == nil {
		t.Fatal("create on a changed backend accepted")
	}
	if r.mutations() != 0 {
		t.Fatalf("calls: %v", r.j.Entries())
	}
}

func TestIncusExistingEnvRefusalNamesTheInstance(t *testing.T) {
	r := newRig(t)
	withIncus(t, r)
	seed(t, r, Metadata{Name: "api", Backend: "incus", Host: "box", Instance: "api", Status: StatusBootstrapped})
	_, err := r.m.Create(ctx, CreateOpts{Name: "api", Apply: true})
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "already exists on box (instance api)") || strings.Contains(err.Error(), "vmid") {
		t.Fatalf("err = %v", err)
	}
}

// isolate makes host box isolated on incusbr0, on a server that can
// enforce it.
func isolate(r *rig, f *incustest.Fake) {
	h := &r.m.Cfg.Hosts[2]
	h.Network, h.Isolated = "incusbr0", true
	f.ServerInfo = incus.Server{Extensions: []string{"network_acl", incus.ExtBridgeACLDevices}, Firewall: "nftables"}
	f.NetworkInfo = map[string]incus.Network{"incusbr0": {Name: "incusbr0", Type: "bridge", Managed: true,
		Config: map[string]string{"ipv4.address": "10.148.12.1/24", "ipv6.address": "fd42:4c6f:2f3a:1b2c::1/64"}}}
}

func TestIncusIsolatedCreate(t *testing.T) {
	r := newRig(t)
	box := withIncus(t, r)
	isolate(r, box)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"box HasStoragePool default", "box HasNetwork incusbr0", "box Server", "box Network incusbr0"}
	if got := box.Calls(); !slices.Equal(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if md := r.metadata(t, "api"); !md.Isolated {
		t.Fatalf("metadata = %+v", md)
	}
	tf, _ := os.ReadFile(filepath.Join(r.dir("api"), "main.tf.json"))
	for _, s := range []string{`"incus_network_acl"`, `"security.acls": "province-api"`, `"destination": "10.148.12.1,fd42:4c6f:2f3a:1b2c::1"`} {
		if !strings.Contains(string(tf), s) {
			t.Errorf("main.tf.json lacks %s:\n%s", s, tf)
		}
	}
}

func TestIncusIsolatedPreflight(t *testing.T) {
	cases := map[string]struct {
		change func(f *incustest.Fake)
		name   string
		want   string
	}{
		"old incus":    {func(f *incustest.Fake) { f.ServerInfo.Extensions = []string{"network_acl"} }, "api", "lacks API extension network_bridge_acl_devices"},
		"xtables":      {func(f *incustest.Fake) { f.ServerInfo.Firewall = "xtables" }, "api", `"xtables" firewall driver`},
		"not a bridge": {func(f *incustest.Fake) { f.NetworkInfo["incusbr0"] = incus.Network{Type: "macvlan", Managed: true} }, "api", "a macvlan network"},
		"unmanaged":    {func(f *incustest.Fake) { f.NetworkInfo["incusbr0"] = incus.Network{Type: "bridge"} }, "api", "managed bridge"},
		"no addresses": {func(f *incustest.Fake) {
			n := f.NetworkInfo["incusbr0"]
			n.Config = map[string]string{"ipv4.address": "none"}
			f.NetworkInfo["incusbr0"] = n
		}, "api", "no ipv4.address or ipv6.address"},
		"bad address": {func(f *incustest.Fake) {
			n := f.NetworkInfo["incusbr0"]
			n.Config = map[string]string{"ipv4.address": "auto"}
			f.NetworkInfo["incusbr0"] = n
		}, "api", `ipv4.address "auto"`},
		"long env name": {func(*incustest.Fake) {}, strings.Repeat("a", 55), "at most 54 characters"},
	}
	for name, c := range cases {
		r := newRig(t)
		box := withIncus(t, r)
		isolate(r, box)
		c.change(box)
		_, err := r.m.Create(ctx, CreateOpts{Name: c.name, Host: "box"})
		if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want a preflight error with %q", name, err, c.want)
		}
		if got := r.j.Entries(); slices.ContainsFunc(got, func(e string) bool { return strings.HasPrefix(e, "tofu") }) {
			t.Errorf("%s: tofu ran after a failed preflight: %q", name, got)
		}
	}
	// IPv6 only is enough.
	r := newRig(t)
	box := withIncus(t, r)
	isolate(r, box)
	n := box.NetworkInfo["incusbr0"]
	n.Config = map[string]string{"ipv4.address": "none", "ipv6.address": "fd42:4c6f:2f3a:1b2c::1/64"}
	box.NetworkInfo["incusbr0"] = n
	if _, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box"}); err != nil {
		t.Fatal(err)
	}
}
