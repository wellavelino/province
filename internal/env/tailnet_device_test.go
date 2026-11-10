package env

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/tailscale"
)

// tailnetServer serves devices on the device list and records each request.
func tailnetServer(t *testing.T, devices string) (*tailscale.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var reqs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		reqs = append(reqs, q.Method+" "+q.URL.Path)
		mu.Unlock()
		if q.Method == http.MethodGet {
			w.Write([]byte(devices))
		}
	}))
	t.Cleanup(srv.Close)
	c := tailscale.New("tskey-api-dummy", "-", "tag:dev")
	c.BaseURL = srv.URL
	return c, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(reqs) }
}

func destroyWithDevices(t *testing.T, md Metadata, devices string) (*DestroyResult, []string) {
	t.Helper()
	r := newRig(t)
	seed(t, r, md)
	r.hosts["pve1"].State = "running"
	r.clients["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: proxmox.TypeLXC}}
	c, reqs := tailnetServer(t, devices)
	r.m.Tailnet = c
	res, err := r.m.Destroy(ctx, "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	return res, reqs()
}

const (
	devOwn      = `{"id":"ENV-DEVICE","hostname":"demo","name":"demo.tail0000.ts.net","addresses":["100.64.0.7"],"tags":["tag:dev"]}`
	devRenamed  = `{"id":"ENV-DEVICE","hostname":"renamed","name":"renamed.tail0000.ts.net","addresses":["100.64.0.7"],"tags":["tag:dev"]}`
	devNamesake = `{"id":"OTHER-DEVICE","hostname":"demo","name":"demo.tail0000.ts.net","addresses":["100.64.0.99"],"tags":["tag:dev"]}`
	devUntagged = `{"id":"UNTAGGED","hostname":"demo","name":"demo.tail0000.ts.net","addresses":["100.64.0.50"],"tags":[]}`
)

func devicesJSON(ds ...string) string { return `{"devices":[` + strings.Join(ds, ",") + `]}` }

func deletes(reqs []string) []string {
	var out []string
	for _, r := range reqs {
		if id, ok := strings.CutPrefix(r, "DELETE /api/v2/device/"); ok {
			out = append(out, id)
		}
	}
	return out
}

func notRevoked(res *DestroyResult) bool {
	return slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "NOT revoked") })
}

// Destroy deletes only the env's own tailnet device, never a namesake.
func TestDestroyDeletesOnlyTheEnvsOwnDevice(t *testing.T) {
	joined := Metadata{Name: "demo", Host: "pve1", Node: "pve1", VMID: 105, Status: StatusBootstrapped, TailscaleIP: "100.64.0.7"}
	failed := Metadata{Name: "demo", Host: "pve1", Node: "pve1", VMID: 105, Status: StatusBootstrapFailed}

	t.Run("own_node", func(t *testing.T) {
		res, reqs := destroyWithDevices(t, joined, devicesJSON(devOwn))
		if d := deletes(reqs); !slices.Equal(d, []string{"ENV-DEVICE"}) || len(res.Warnings) != 0 {
			t.Fatalf("deletes=%v warnings=%q", d, res.Warnings)
		}
	})
	t.Run("renamed_plus_namesake", func(t *testing.T) {
		res, reqs := destroyWithDevices(t, joined, devicesJSON(devRenamed, devNamesake))
		if d := deletes(reqs); len(d) != 0 || !notRevoked(res) {
			t.Fatalf("deletes=%v warnings=%q", d, res.Warnings)
		}
	})
	t.Run("renamed_only", func(t *testing.T) {
		res, reqs := destroyWithDevices(t, joined, devicesJSON(devRenamed))
		if d := deletes(reqs); len(d) != 0 || !notRevoked(res) {
			t.Fatalf("deletes=%v warnings=%q", d, res.Warnings)
		}
	})
	t.Run("never_joined_namesake", func(t *testing.T) {
		_, reqs := destroyWithDevices(t, failed, devicesJSON(devNamesake))
		if d := deletes(reqs); len(d) != 0 {
			t.Fatalf("deletes=%v", d)
		}
	})
	t.Run("own_plus_namesake_refused", func(t *testing.T) {
		res, reqs := destroyWithDevices(t, joined, devicesJSON(devOwn, devNamesake))
		if d := deletes(reqs); len(d) != 0 || len(res.Warnings) == 0 {
			t.Fatalf("deletes=%v warnings=%q", d, res.Warnings)
		}
	})
	t.Run("untagged_namesake_kept", func(t *testing.T) {
		_, reqs := destroyWithDevices(t, failed, devicesJSON(devUntagged))
		if d := deletes(reqs); len(d) != 0 {
			t.Fatalf("deletes=%v", d)
		}
	})
}

// With a device ID recorded at bootstrap, destroy deletes that device
// whatever its name now is, and no namesake.
func TestDestroyDeletesTheRecordedDeviceID(t *testing.T) {
	md := Metadata{Name: "demo", Host: "pve1", Node: "pve1", VMID: 105, Status: StatusBootstrapped,
		TailscaleIP: "100.64.0.7", TailscaleDeviceID: "ENV-DEVICE"}
	res, reqs := destroyWithDevices(t, md, devicesJSON(devRenamed, devNamesake))
	if !slices.Equal(reqs, []string{"DELETE /api/v2/device/ENV-DEVICE"}) || len(res.Warnings) != 0 {
		t.Fatalf("requests=%v warnings=%q", reqs, res.Warnings)
	}
}

// Create looks the node up from the operator side after bootstrap and
// records its ID; a namesake at another address is not taken for it.
func TestCreateRecordsTheTailnetDeviceID(t *testing.T) {
	r := newRig(t)
	r.m.Cfg.Recipes = nil
	c, reqs := tailnetServer(t, devicesJSON(devNamesake, devOwn))
	r.m.Tailnet = c
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if md := r.metadata(t, "demo"); md.TailscaleDeviceID != "ENV-DEVICE" || len(res.Warnings) != 0 {
		t.Fatalf("device id = %q, warnings %q", md.TailscaleDeviceID, res.Warnings)
	}
	if d := deletes(reqs()); len(d) != 0 {
		t.Fatalf("create deleted %v", d)
	}
}

func TestCreateWarnsWhenTheDeviceIsNotFound(t *testing.T) {
	r := newRig(t)
	r.m.Cfg.Recipes = nil
	c, _ := tailnetServer(t, devicesJSON(devNamesake))
	r.m.Tailnet = c
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	md := r.metadata(t, "demo")
	if md.Status != StatusBootstrapped || md.TailscaleDeviceID != "" || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "not recorded") {
		t.Fatalf("status %s, device id %q, warnings %q", md.Status, md.TailscaleDeviceID, res.Warnings)
	}
}

// Without TS_API_KEY there is no lookup and no warning.
func TestCreateWithoutAPIKeySkipsTheLookup(t *testing.T) {
	r := newRig(t)
	r.m.Cfg.Recipes = nil
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if err != nil || len(res.Warnings) != 0 || r.metadata(t, "demo").TailscaleDeviceID != "" {
		t.Fatalf("%+v %v", res, err)
	}
}
