package move

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/proxmox"
)

// pruneRig: pve1 holds the stopped, onboot=0 copy 105 "demo"; pve2 holds
// the moved copy as 106.
func pruneRig(t *testing.T) *rig {
	r := newRig(t)
	r.api["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "stopped", Type: "lxc"}}
	r.api["pve1"].Configs[105] = map[string]string{"rootfs": "local-lvm:vm-105-disk-0,size=8G", "onboot": "0"}
	r.api["pve2"].Guests = []proxmox.Guest{{VMID: 106, Name: "demo", Status: "running", Type: "lxc"}}
	return r
}

func pruneOpts() PruneOpts {
	return PruneOpts{VMID: 105, Host: "pve1", Yes: true, EnvAt: func(string, int) (string, error) { return "", nil }}
}

func TestPruneHappyPath(t *testing.T) {
	r := pruneRig(t)
	res, err := r.m.Prune(ctx, pruneOpts())
	if err != nil {
		t.Fatal(err)
	}
	assertJournal(t, r, []string{"pve1 Destroy lxc 105"})
	if !res.Destroyed || res.Type != "lxc" || !slices.Equal(res.CopiesOn, []string{"pve2"}) || res.Forced {
		t.Fatalf("result = %+v", res)
	}

	// A VM is destroyed with qm (argv itself is covered in hostops).
	r = pruneRig(t)
	r.api["pve1"].Guests = []proxmox.Guest{{VMID: 200, Name: "win", Status: "stopped", Type: "qemu"}}
	r.api["pve1"].Configs[200] = map[string]string{"onboot": "0"}
	r.api["pve2"].Guests = []proxmox.Guest{{VMID: 200, Name: "win", Status: "running", Type: "qemu"}}
	o := pruneOpts()
	o.VMID = 200
	if _, err := r.m.Prune(ctx, o); err != nil {
		t.Fatal(err)
	}
	assertJournal(t, r, []string{"pve1 Destroy qemu 200"})
}

func TestPruneRefusals(t *testing.T) {
	cases := map[string]struct {
		mut  func(r *rig, o *PruneOpts)
		kind errs.Kind
		want string
	}{
		"no --yes":        {func(r *rig, o *PruneOpts) { o.Yes = false }, errs.Refused, "--yes"},
		"unknown host":    {func(r *rig, o *PruneOpts) { o.Host = "pve9" }, errs.Refused, "pve9"},
		"province env":    {func(r *rig, o *PruneOpts) { o.EnvAt = func(string, int) (string, error) { return "demo", nil } }, errs.Refused, "env destroy demo"},
		"not found":       {func(r *rig, o *PruneOpts) { o.VMID = 999 }, errs.Preflight, "not found"},
		"running":         {func(r *rig, o *PruneOpts) { r.api["pve1"].Guests[0].Status = "running" }, errs.Preflight, "is running"},
		"onboot=1":        {func(r *rig, o *PruneOpts) { r.api["pve1"].Configs[105]["onboot"] = "1" }, errs.Preflight, "onboot=0"},
		"onboot unset":    {func(r *rig, o *PruneOpts) { delete(r.api["pve1"].Configs[105], "onboot") }, errs.Preflight, "onboot=0"},
		"protected":       {func(r *rig, o *PruneOpts) { r.api["pve1"].Configs[105]["protection"] = "1" }, errs.Preflight, "protection=1"},
		"locked":          {func(r *rig, o *PruneOpts) { r.api["pve1"].Configs[105]["lock"] = "backup" }, errs.Preflight, "locked"},
		"no copy":         {func(r *rig, o *PruneOpts) { r.api["pve2"].Guests = nil }, errs.Preflight, "moved-copy check"},
		"copy unknown":    {func(r *rig, o *PruneOpts) { r.api["pve2"].Err = errors.New("dial tcp: timeout") }, errs.Preflight, "--force"},
		"nameless":        {func(r *rig, o *PruneOpts) { r.api["pve1"].Guests[0].Name = ""; r.api["pve2"].Guests[0].Name = "" }, errs.Preflight, "moved-copy check"},
		"source api down": {func(r *rig, o *PruneOpts) { r.api["pve1"].Err = errors.New("connection refused") }, errs.Preflight, "connection refused"},
	}
	for name, c := range cases {
		r := pruneRig(t)
		o := pruneOpts()
		c.mut(r, &o)
		_, err := r.m.Prune(ctx, o)
		if errs.KindOf(err) != c.kind || errs.ExitCode(err) != 2 || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v (kind %s)", name, err, errs.KindOf(err))
		}
		if e := r.j.Entries(); len(e) != 0 {
			t.Errorf("%s: mutations: %v", name, e)
		}
	}
}

func TestPruneNoYesMakesNoCallsAtAll(t *testing.T) {
	r := pruneRig(t)
	o := pruneOpts()
	o.Yes = false
	_, _ = r.m.Prune(ctx, o)
	if len(r.api["pve1"].Calls())+len(r.api["pve2"].Calls()) != 0 {
		t.Fatal("API called without --yes")
	}
}

func TestPruneForceSkipsOnlyTheCopyCheck(t *testing.T) {
	r := pruneRig(t)
	r.api["pve2"].Guests = nil
	o := pruneOpts()
	o.Force = true
	res, err := r.m.Prune(ctx, o)
	if err != nil || !res.Forced || len(res.CopiesOn) != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	assertJournal(t, r, []string{"pve1 Destroy lxc 105"})

	// --force does not override the other checks.
	r = pruneRig(t)
	r.api["pve1"].Guests[0].Status = "running"
	if _, err := r.m.Prune(ctx, o); errs.KindOf(err) != errs.Preflight {
		t.Fatalf("running with --force: %v", err)
	}
}

func TestPruneSkipsHostsWithoutTokenInCopyCheck(t *testing.T) {
	r := pruneRig(t)
	r.m.Cfg.Hosts = append(r.m.Cfg.Hosts, config.Host{Name: "pve3", Node: "pve3"})
	client := r.m.Client
	r.m.Client = func(h config.Host) (proxmox.Client, error) {
		if h.Name == "pve3" {
			return nil, errs.Refusedf("PROVINCE_TOKEN_PVE3 (API token for host pve3) is not set")
		}
		return client(h)
	}
	res, err := r.m.Prune(ctx, pruneOpts())
	if err != nil || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "pve3") {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestMoveOutputPointsAtPrune(t *testing.T) {
	r := newRig(t)
	res, err := r.m.Move(ctx, opts())
	if err != nil || !strings.Contains(res.Source, "province guest prune 105 --host pve1 --yes") {
		t.Fatalf("source = %q, %v", res.Source, err)
	}
}
