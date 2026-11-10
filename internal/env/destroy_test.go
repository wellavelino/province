package env

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/proxmox"
)

// fakeTailnet journals each call. found is FindDevice's answer; deleted
// and err are DeleteDeviceByHostname's.
type fakeTailnet struct {
	j       interface{ Add(string) }
	found   string
	deleted bool
	err     error
}

func (f *fakeTailnet) FindDevice(_ context.Context, h, ip string) (string, error) {
	f.j.Add("tailnet find " + h + " " + ip)
	return f.found, nil
}

func (f *fakeTailnet) DeleteDevice(_ context.Context, id string) error {
	f.j.Add("tailnet delete id " + id)
	return nil
}

func (f *fakeTailnet) DeleteDeviceByHostname(_ context.Context, h, _ string) (bool, error) {
	f.j.Add("tailnet delete " + h)
	return f.deleted, f.err
}

func bootstrapped(t *testing.T, r *rig) {
	t.Helper()
	seed(t, r, Metadata{Name: "demo", Host: "pve1", Node: "pve1", VMID: 105, Status: StatusBootstrapped, TailscaleIP: "100.64.0.7"})
	r.hosts["pve1"].State = "running"
	r.clients["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: proxmox.TypeLXC}}
}

func TestDestroyWithoutYesTouchesNothing(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)
	_, err := r.m.Destroy(ctx, "demo", false)
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
	if r.mutations() != 0 || r.apiCalls() != 0 {
		t.Fatalf("calls: %v", r.j.Entries())
	}
	if _, err := os.Stat(r.dir("demo")); err != nil {
		t.Fatal("state removed")
	}
}

func TestDestroyOrder(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)
	r.m.Tailnet = &fakeTailnet{j: r.j, deleted: true}
	res, err := r.m.Destroy(ctx, "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	dir := r.dir("demo")
	want := []string{"pve1 Status lxc 105", "pve1 Exec 105 tailscale logout", "tailnet delete demo", "tofu Init " + dir, "tofu Destroy " + dir}
	if got := r.j.Entries(); !slices.Equal(got, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state dir not removed")
	}
	if !res.Destroyed || len(res.Warnings) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestDestroyTailscaleStepsAreBestEffort(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)
	r.hosts["pve1"].Fail = func(c string) error {
		if strings.Contains(c, "Exec") {
			return errors.New("container not running")
		}
		return nil
	}
	// No TS_API_KEY: no API call, a warning instead.
	res, err := r.m.Destroy(ctx, "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 2 || !strings.Contains(res.Warnings[0], "logout") || !strings.Contains(res.Warnings[1], "TS_API_KEY") {
		t.Fatalf("warnings = %q", res.Warnings)
	}

	r = newRig(t)
	bootstrapped(t, r)
	r.m.Tailnet = &fakeTailnet{j: r.j, deleted: false}
	res, err = r.m.Destroy(ctx, "demo", true)
	if err != nil || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "NOT revoked") {
		t.Fatalf("not found: %+v, %v", res, err)
	}
}

func TestDestroyFailureKeepsState(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)
	r.tofu.Fail = func(call string) error {
		if strings.HasPrefix(call, "Destroy") {
			return errors.New("tofu destroy: 500 unable to delete")
		}
		return nil
	}
	_, err := r.m.Destroy(ctx, "demo", true)
	if err == nil || errs.ExitCode(err) != 1 || !strings.Contains(err.Error(), "is kept") {
		t.Fatalf("err = %v", err)
	}
	if md := r.metadata(t, "demo"); md.Status != StatusBootstrapped {
		t.Fatalf("metadata = %+v", md)
	}
}

func TestDestroyPlannedSkipsTailscale(t *testing.T) {
	r := newRig(t)
	seed(t, r, Metadata{Name: "demo", Host: "pve1", Node: "pve1", Status: StatusPlanned})
	r.m.Tailnet = &fakeTailnet{j: r.j, deleted: true}
	if _, err := r.m.Destroy(ctx, "demo", true); err != nil {
		t.Fatal(err)
	}
	dir := r.dir("demo")
	if got := r.j.Entries(); !slices.Equal(got, []string{"tofu Init " + dir, "tofu Destroy " + dir}) {
		t.Fatalf("calls = %v", got)
	}
}

func TestDestroyUnknownEnv(t *testing.T) {
	r := newRig(t)
	_, err := r.m.Destroy(ctx, "ghost", true)
	if err == nil || errs.ExitCode(err) != 1 || r.mutations() != 0 {
		t.Fatalf("err = %v, calls = %v", err, r.j.Entries())
	}
}

func TestDestroyStoppedGuestSkipsLogoutButDeletesNode(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)
	r.hosts["pve1"].State = "stopped"
	r.m.Tailnet = &fakeTailnet{j: r.j, deleted: true}
	res, err := r.m.Destroy(ctx, "demo", true)
	if err != nil {
		t.Fatal(err)
	}
	dir := r.dir("demo")
	want := []string{"pve1 Status lxc 105", "tailnet delete demo", "tofu Init " + dir, "tofu Destroy " + dir}
	if got := r.j.Entries(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v", got)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("a stopped guest is not a warning: %q", res.Warnings)
	}
	if !strings.Contains(r.log.String(), "info: demo is stopped; skipping tailscale logout") {
		t.Fatalf("log = %q", r.log.String())
	}
}
