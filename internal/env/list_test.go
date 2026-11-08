package env

import (
	"errors"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/proxmox"
)

func seed(t *testing.T, r *rig, mds ...Metadata) {
	t.Helper()
	for i := range mds {
		md := mds[i]
		if err := mkdirAll(r.dir(md.Name)); err != nil {
			t.Fatal(err)
		}
		if err := r.m.writeMetadata(r.dir(md.Name), &md); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListOneCallPerHostAndUnreachableIsUnknown(t *testing.T) {
	r := newRig(t)
	seed(t, r,
		Metadata{Name: "a", Host: "pve1", Node: "pve1", VMID: 105, Status: StatusBootstrapped},
		Metadata{Name: "b", Host: "pve1", Node: "pve1", VMID: 106, Status: StatusBootstrapFailed},
		Metadata{Name: "c", Host: "pve1", Node: "pve1", VMID: 107, Status: StatusBootstrapped},
		Metadata{Name: "d", Host: "pve2", Node: "pve2", VMID: 300, Status: StatusBootstrapped},
		Metadata{Name: "e", Host: "pve1", Node: "pve1", Status: StatusPlanned},
	)
	r.clients["pve1"].Guests = []proxmox.Guest{
		{VMID: 105, Status: "running", Type: "lxc"},
		{VMID: 106, Status: "stopped", Type: "lxc"},
		{VMID: 107, Status: "running", Type: "qemu"}, // same id, wrong type: not ours
	}
	r.clients["pve2"].Err = errors.New("dial tcp: i/o timeout")

	views, err := r.m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range views {
		got[v.Name] = v.LiveStatus
	}
	want := map[string]string{"a": "running", "b": "stopped", "c": LiveMissing, "d": LiveUnknown, "e": LiveNotCreated}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: live = %q, want %q", k, got[k], w)
		}
	}
	if views[0].Name != "a" || views[0].SSH != "ssh dev@a" || views[1].SSH != "" {
		t.Errorf("order/ssh: %+v", views[:2])
	}
	if !strings.Contains(views[3].Error, "i/o timeout") {
		t.Errorf("d error = %q", views[3].Error)
	}
	if n := len(r.clients["pve1"].Calls()); n != 1 {
		t.Errorf("pve1 API calls = %d, want 1", n)
	}
	if n := len(r.clients["pve2"].Calls()); n != 1 {
		t.Errorf("pve2 API calls = %d, want 1", n)
	}
}

func TestListEmptyStateDir(t *testing.T) {
	r := newRig(t)
	views, err := r.m.List(ctx)
	if err != nil || views == nil || len(views) != 0 {
		t.Fatalf("views = %v, %v", views, err)
	}
}

func TestShow(t *testing.T) {
	r := newRig(t)
	seed(t, r, Metadata{Name: "demo", Host: "pve1", Node: "pve1", VMID: 105, Status: StatusBootstrapped, TailscaleIP: "100.64.0.7"})
	r.clients["pve1"].Guests = []proxmox.Guest{{VMID: 105, Status: "running", Type: "lxc"}}
	v, err := r.m.Show(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if v.LiveStatus != "running" || v.SSH != "ssh dev@demo" || v.TailscaleIP != "100.64.0.7" {
		t.Fatalf("view = %+v", v)
	}

	r.clients["pve1"].Err = errors.New("connection refused")
	v, err = r.m.Show(ctx, "demo")
	if err != nil || v.LiveStatus != LiveUnknown || v.Error == "" {
		t.Fatalf("unreachable: %+v, %v", v, err)
	}

	r.clients["pve1"].Err = nil
	r.m.Client = func(h config.Host) (proxmox.Client, error) {
		return nil, errs.Refusedf("PROXMOX_VE_API_TOKEN (API token for host %s) is not set", h.Name)
	}
	v, err = r.m.Show(ctx, "demo")
	if err != nil || v.LiveStatus != LiveUnknown || !strings.Contains(v.Error, "PROXMOX_VE_API_TOKEN") {
		t.Fatalf("token unset: %+v, %v", v, err)
	}
	seed(t, r, Metadata{Name: "gone", Host: "pve9", Node: "pve9", VMID: 105, Status: StatusBootstrapped})
	v, err = r.m.Show(ctx, "gone")
	if err != nil || v.LiveStatus != LiveUnknown || !strings.Contains(v.Error, "pve9") {
		t.Fatalf("host not in config: %+v, %v", v, err)
	}

	_, err = r.m.Show(ctx, "nope")
	if err == nil || errs.ExitCode(err) != 1 || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown env: %v", err)
	}
}
