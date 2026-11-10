package env

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/incus"
	"github.com/wellavelino/province/internal/proxmox"
)

// A VMID recorded by an env stays taken even when its guest is gone.
func TestCreateSkipsVMIDsRecordedInTheStateDir(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)             // demo records 105
	r.clients["pve1"].Guests = nil // ...but no host lists it
	r.m.Cfg.Recipes = nil
	r.m.Cfg.VMIDStart = 105
	res, err := r.m.Create(ctx, CreateOpts{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if res.VMID == 105 {
		t.Fatalf("new env other planned at vmid 105, which env demo records")
	}
}

// The guest at an env's VMID is someone else's: province must not act on it.
func TestEnvIsBoundToItsGuestNotJustItsVMID(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)
	r.clients["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "prod-db", Status: "running", Type: proxmox.TypeLXC}}
	r.hosts["pve1"].ExecOuts = map[string]string{statusCmd: statusFunnelOK}
	r.m.Tailnet = &fakeTailnet{j: r.j, deleted: true}

	v, err := r.m.Show(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if v.LiveStatus != LiveUnknown || !strings.Contains(v.Error, "prod-db") {
		t.Fatalf("show: live_status=%q error=%q", v.LiveStatus, v.Error)
	}
	vs, err := r.m.List(ctx)
	if err != nil || len(vs) != 1 {
		t.Fatalf("list: %v %v", err, vs)
	}
	if vs[0].LiveStatus != LiveUnknown || !strings.Contains(vs[0].Error, "prod-db") {
		t.Fatalf("list: live_status=%q error=%q", vs[0].LiveStatus, vs[0].Error)
	}

	before := len(r.j.Entries())
	if _, err := r.m.Expose(ctx, "demo", 3000, true); errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "prod-db") {
		t.Fatalf("expose: err = %v", err)
	}
	if _, err := r.m.Provision(ctx, "demo", ""); errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "prod-db") {
		t.Fatalf("provision: err = %v", err)
	}
	if _, err := r.m.Destroy(ctx, "demo", true); errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "prod-db") {
		t.Fatalf("destroy: err = %v", err)
	}
	for _, e := range r.j.Entries()[before:] {
		if strings.Contains(e, "Exec") || strings.HasPrefix(e, "tofu Destroy") || strings.HasPrefix(e, "tailnet") {
			t.Fatalf("acted on prod-db: %v", r.j.Entries()[before:])
		}
	}
	if _, err := os.Stat(r.dir("demo")); err != nil {
		t.Fatal("state dir removed")
	}
	if md := r.metadata(t, "demo"); md.PublicURL != "" {
		t.Fatalf("public_url recorded: %q", md.PublicURL)
	}
}

// A VM that took the VMID is not the env's container either.
func TestEnvDestroyRefusesAVMAtItsVMID(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)
	r.clients["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: proxmox.TypeQEMU}}
	r.m.Tailnet = &fakeTailnet{j: r.j, deleted: true}
	before := len(r.j.Entries())
	if _, err := r.m.Destroy(ctx, "demo", true); errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "qemu") {
		t.Fatalf("destroy: err = %v", err)
	}
	if len(r.j.Entries()) != before {
		t.Fatalf("calls: %v", r.j.Entries()[before:])
	}
}

// The env's own container (an LXC named after it) is acted on as before.
func TestEnvGuestOfTheRightNameIsUsed(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)
	r.clients["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: proxmox.TypeLXC}}
	r.hosts["pve1"].ExecOuts = map[string]string{statusCmd: statusFunnelOK}
	if _, err := r.m.Expose(ctx, "demo", 3000, true); err != nil {
		t.Fatal(err)
	}
	r.m.Tailnet = &fakeTailnet{j: r.j, deleted: true}
	if _, err := r.m.Destroy(ctx, "demo", true); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(r.j.Entries(), func(e string) bool { return strings.HasPrefix(e, "tofu Destroy") }) {
		t.Fatalf("no tofu destroy: %v", r.j.Entries())
	}
}

// Incus: an instance of the env's name without province's marker is not the env's.
func TestIncusEnvIsBoundByItsMarker(t *testing.T) {
	r := newRig(t)
	box := withIncus(t, r)
	seed(t, r, Metadata{Name: "api", Backend: "incus", Host: "box", Instance: "api", Status: StatusBootstrapped})
	box.List = []incus.Instance{{Name: "api", Type: "container", Status: "running", Env: "someone-else"}}
	r.m.Tailnet = &fakeTailnet{j: r.j, deleted: true}

	v, err := r.m.Show(ctx, "api")
	if err != nil {
		t.Fatal(err)
	}
	if v.LiveStatus != LiveUnknown || !strings.Contains(v.Error, "user.province.env") {
		t.Fatalf("show: live_status=%q error=%q", v.LiveStatus, v.Error)
	}
	before := len(r.j.Entries())
	if _, err := r.m.Expose(ctx, "api", 3000, true); errs.KindOf(err) != errs.Preflight {
		t.Fatalf("expose: err = %v", err)
	}
	if _, err := r.m.Destroy(ctx, "api", true); errs.KindOf(err) != errs.Preflight {
		t.Fatalf("destroy: err = %v", err)
	}
	for _, e := range r.j.Entries()[before:] {
		if strings.Contains(e, "Exec") || strings.HasPrefix(e, "tofu Destroy") {
			t.Fatalf("acted on another instance: %v", r.j.Entries()[before:])
		}
	}
}
