package env

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/incus"
)

// withSleeps records the rig's sleeps instead of sleeping.
func withSleeps(t *testing.T, r *rig) (sleeps *[]time.Duration) {
	t.Helper()
	sleeps = &[]time.Duration{}
	r.m.Sleep = func(_ context.Context, d time.Duration) error {
		*sleeps = append(*sleeps, d)
		return nil
	}
	return sleeps
}

func TestIncusCreateVMWaitsForTheAgent(t *testing.T) {
	r := newRig(t)
	box := withIncus(t, r)
	sleeps := withSleeps(t, r)
	left := 2
	box.Fail = func(call string) error {
		if call == "box Exec api true" && left > 0 {
			left--
			return errors.New("Error: VM agent isn't currently running")
		}
		return nil
	}
	res, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box", Kind: KindVM, Docker: true, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := r.dir("api")
	want := []string{"box HasStoragePool default", "tofu Init " + dir, "tofu Apply " + dir, "tofu Output " + dir,
		"box Exec api true", "box Exec api true", "box Exec api true",
		"box Exec api sh -c tailscale ip -4 2>/dev/null || true", "box Exec api bash -s"}
	if got := r.j.Entries(); !slices.Equal(got, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(*sleeps) != 2 {
		t.Fatalf("sleeps = %v, want 2", *sleeps)
	}
	md := r.metadata(t, "api")
	if md.Kind != KindVM || md.Status != StatusBootstrapped || res.Kind != KindVM {
		t.Fatalf("metadata = %+v, result = %+v", md, res)
	}
	tf, _ := os.ReadFile(filepath.Join(dir, "main.tf.json"))
	if !strings.Contains(string(tf), `"type": "virtual-machine"`) || strings.Contains(string(tf), "security.") {
		t.Fatalf("main.tf.json:\n%s", tf)
	}
	if stdin := box.ExecStdins(); len(stdin) != 1 || !strings.Contains(stdin[0], "P_DOCKER=1") {
		t.Fatalf("bootstrap payload = %q", stdin)
	}
}

func TestIncusVMAgentTimeout(t *testing.T) {
	r := newRig(t)
	box := withIncus(t, r)
	sleeps := withSleeps(t, r)
	r.m.AgentTimeout = 6 * time.Second // tries at 0, 2, 4 and 6 s
	box.Fail = func(call string) error {
		if call == "box Exec api true" {
			return errors.New("Error: VM agent isn't currently running")
		}
		return nil
	}
	_, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box", Kind: KindVM, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "did not answer within 6s") || !strings.Contains(err.Error(), "the VM and") {
		t.Fatalf("err = %v", err)
	}
	n := 0
	for _, c := range box.Calls() {
		if c == "box Exec api true" {
			n++
		}
		if strings.Contains(c, "bash -s") {
			t.Fatal("bootstrap ran without the agent")
		}
	}
	if n != 4 || len(*sleeps) != 3 {
		t.Fatalf("%d tries, sleeps %v", n, *sleeps)
	}
	if md := r.metadata(t, "api"); md.Status != StatusBootstrapFailed || md.Kind != KindVM {
		t.Fatalf("metadata = %+v", md)
	}
}

func TestIncusContainerDoesNotWaitForAnAgent(t *testing.T) {
	r := newRig(t)
	box := withIncus(t, r)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box", Kind: KindLXC, Apply: true}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(box.Calls(), "box Exec api true") {
		t.Fatalf("calls = %v", box.Calls())
	}
	if md := r.metadata(t, "api"); md.Kind != KindLXC {
		t.Fatalf("kind = %q", md.Kind)
	}
}

func TestVMRefusedOnProxmox(t *testing.T) {
	r := newRig(t)
	_, err := r.m.Create(ctx, CreateOpts{Name: "web", Host: "pve1", Kind: KindVM})
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "cloud-init template with qemu-guest-agent") || !strings.Contains(err.Error(), "planned") {
		t.Fatalf("err = %v", err)
	}
	if r.mutations() != 0 || r.apiCalls() != 0 {
		t.Fatalf("calls: %v", r.j.Entries())
	}
	if _, err := os.Stat(r.dir("web")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("state dir written: %v", err)
	}
}

func TestVMRefusesTunDevice(t *testing.T) {
	r := newRig(t)
	withIncus(t, r)
	r.m.Cfg.Tailscale.Tun = config.TunDevice
	_, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box", Kind: KindVM})
	if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "tailscale.tun is device") || !strings.Contains(err.Error(), "own kernel") {
		t.Fatalf("err = %v", err)
	}
	if r.mutations() != 0 {
		t.Fatalf("calls: %v", r.j.Entries())
	}
	// A container on the same site still takes the device.
	if _, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box"}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordedKindIsKeptAndNotChanged(t *testing.T) {
	r := newRig(t)
	withIncus(t, r)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "api", Host: "box", Kind: KindVM}); err != nil {
		t.Fatal(err)
	}
	// A re-run without --kind keeps the VM.
	if _, err := r.m.Create(ctx, CreateOpts{Name: "api"}); err != nil {
		t.Fatal(err)
	}
	if md := r.metadata(t, "api"); md.Kind != KindVM {
		t.Fatalf("kind = %q", md.Kind)
	}
	_, err := r.m.Create(ctx, CreateOpts{Name: "api", Kind: KindLXC})
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "planned as a vm guest, not lxc") {
		t.Fatalf("err = %v", err)
	}
	// A record from before kinds were recorded is a container.
	seed(t, r, Metadata{Name: "old", Backend: "incus", Host: "box", Status: StatusPlanned})
	if _, err := r.m.Create(ctx, CreateOpts{Name: "old", Kind: KindVM}); errs.KindOf(err) != errs.Refused {
		t.Fatalf("old record: %v", err)
	}
	if _, err := r.m.Create(ctx, CreateOpts{Name: "old", Kind: KindLXC}); err != nil {
		t.Fatal(err)
	}
}

// An instance is the env's only if its type matches the recorded kind.
func TestIncusIdentifyByRecordedKind(t *testing.T) {
	r := newRig(t)
	box := withIncus(t, r)
	seed(t, r,
		Metadata{Name: "vm", Kind: KindVM, Backend: "incus", Host: "box", Instance: "vm", Status: StatusBootstrapped},
		Metadata{Name: "ct", Kind: KindLXC, Backend: "incus", Host: "box", Instance: "ct", Status: StatusBootstrapped},
		Metadata{Name: "old", Backend: "incus", Host: "box", Instance: "old", Status: StatusBootstrapped}, // no kind: lxc
		Metadata{Name: "vm2", Kind: KindVM, Backend: "incus", Host: "box", Instance: "vm2", Status: StatusBootstrapped},
	)
	box.List = []incus.Instance{
		{Name: "vm", Type: incus.TypeVM, Status: "running", Env: "vm"},
		{Name: "ct", Type: incus.TypeContainer, Status: "running", Env: "ct"},
		{Name: "old", Type: incus.TypeVM, Status: "running", Env: "old"},
		{Name: "vm2", Type: incus.TypeContainer, Status: "running", Env: "vm2"},
	}
	views, err := r.m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range views {
		got[v.Name] = v.LiveStatus
	}
	if want := map[string]string{"vm": "running", "ct": "running", "old": LiveMissing, "vm2": LiveMissing}; !maps.Equal(got, want) {
		t.Fatalf("live = %v, want %v", got, want)
	}
	b := incusBackend{m: r.m, h: r.m.Cfg.Hosts[2]}
	for name, ours := range map[string]bool{"vm": true, "ct": true, "old": false, "vm2": false} {
		found, err := b.verify(ctx, r.metadata(t, name))
		if !found || (err == nil) != ours {
			t.Errorf("%s: found=%v err=%v", name, found, err)
		}
		if !ours && errs.KindOf(err) != errs.Preflight {
			t.Errorf("%s: kind %v", name, errs.KindOf(err))
		}
	}
	if v, err := r.m.Show(ctx, "vm"); err != nil || v.LiveStatus != "running" || v.Kind != KindVM {
		t.Fatalf("show = %+v, %v", v, err)
	}
	// Provision and expose ask the guest itself.
	for name, want := range map[string]string{"vm": "running", "ct": "running", "old": LiveMissing, "vm2": LiveMissing} {
		g, err := b.guest(r.metadata(t, name))
		if err != nil {
			t.Fatal(err)
		}
		if st, err := g.Status(ctx); err != nil || st != want {
			t.Errorf("%s: guest status = %q, %v; want %q", name, st, err, want)
		}
	}
}
