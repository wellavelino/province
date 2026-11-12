package env

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/proxmox"
)

var ctx = context.Background()

func TestCreateInvalidNameTouchesNothing(t *testing.T) {
	r := newRig(t)
	for _, name := range []string{"Demo", "../x", "", "a_b"} {
		_, err := r.m.Create(ctx, CreateOpts{Name: name, Apply: true})
		if errs.KindOf(err) != errs.Refused {
			t.Errorf("%q: err = %v", name, err)
		}
	}
	if r.mutations() != 0 || r.apiCalls() != 0 {
		t.Fatalf("calls made: %v", r.j.Entries())
	}
	if _, err := os.Stat(r.m.Cfg.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state dir created for an invalid name")
	}
}

func TestCreateApplyWithoutAuthKeyRefused(t *testing.T) {
	r := newRig(t)
	r.m.Secrets.AuthKey = ""
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "TS_AUTHKEY") {
		t.Fatalf("err = %v", err)
	}
	if r.mutations() != 0 || r.apiCalls() != 0 {
		t.Fatalf("calls made: %v", r.j.Entries())
	}
}

func TestCreatePreflightFailuresStopBeforeTofu(t *testing.T) {
	cases := map[string]struct {
		mut  func(r *rig)
		want string
	}{
		"template":           {func(r *rig) { r.clients["pve1"].Templates = nil }, "pveam download"},
		"bridge":             {func(r *rig) { r.clients["pve1"].BridgeLs = []string{"vmbr9"} }, "bridge vmbr0"},
		"no bridges visible": {func(r *rig) { r.clients["pve1"].BridgeLs = nil }, "SDN.Audit"},
		"storage":            {func(r *rig) { r.clients["pve1"].StorageLs = nil }, "storage local-lvm"},
		"storage content":    {func(r *rig) { r.clients["pve1"].StorageLs[0].Content = []string{"images"} }, "rootdir"},
		"api down":           {func(r *rig) { r.clients["pve1"].Err = errors.New("dial tcp: connection refused") }, "connection refused"},
		"key file":           {func(r *rig) { r.m.Cfg.SSHPublicKeys = []string{"/nonexistent/id.pub"} }, "ssh public key"},
	}
	for name, c := range cases {
		r := newRig(t)
		c.mut(r)
		_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
		if errs.KindOf(err) != errs.Preflight || errs.ExitCode(err) != 2 || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v (kind %s)", name, err, errs.KindOf(err))
		}
		if r.mutations() != 0 {
			t.Errorf("%s: mutations after failed preflight: %v", name, r.j.Entries())
		}
	}
}

func TestCreatePlanOnly(t *testing.T) {
	r := newRig(t)
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo", CPU: 4})
	if err != nil {
		t.Fatal(err)
	}
	dir := r.dir("demo")
	if want := []string{"tofu Init " + dir, "tofu Plan " + dir}; !slices.Equal(r.j.Entries(), want) {
		t.Fatalf("calls = %v", r.j.Entries())
	}
	if res.Status != StatusPlanned || res.Plan != "Plan: 1 to add" || !res.Changes {
		t.Fatalf("result = %+v", res)
	}
	for _, f := range []string{"main.tf.json", ".gitignore", MetadataFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(gi) != ".terraform/\n" {
		t.Errorf(".gitignore = %q", gi)
	}
	md := r.metadata(t, "demo")
	if md.Status != StatusPlanned || md.Host != "pve1" || md.CPU != 4 || md.VMID != 0 {
		t.Fatalf("metadata = %+v", md)
	}
	// The planned sizes carry over to a later --apply without flags.
	tf, _ := os.ReadFile(filepath.Join(dir, "main.tf.json"))
	if !strings.Contains(string(tf), `"cores": 4`) {
		t.Fatal("main.tf.json lacks the requested cores")
	}
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	if md := r.metadata(t, "demo"); md.CPU != 4 {
		t.Fatalf("re-plan lost cpu: %+v", md)
	}
}

func TestCreateApplyOrderAndStatuses(t *testing.T) {
	r := newRig(t)
	dir := r.dir("demo")
	var atExec *Metadata
	r.hosts["pve1"].Fail = func(call string) error {
		if strings.HasPrefix(call, "pve1 Exec") {
			atExec, _ = readMetadata(dir)
		}
		return nil
	}
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tofu Init " + dir, "tofu Apply " + dir, "tofu Output " + dir, "pve1 Exec 105 bash -s"}
	if got := r.j.Entries(); !slices.Equal(got, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if atExec == nil || atExec.Status != StatusApplied || atExec.VMID != 105 {
		t.Fatalf("metadata during bootstrap = %+v", atExec)
	}
	md := r.metadata(t, "demo")
	if md.Status != StatusBootstrapped || md.TailscaleIP != "100.64.0.7" || md.VMID != 105 {
		t.Fatalf("metadata = %+v", md)
	}
	if res.SSH != "ssh dev@demo" || res.TailscaleIP != "100.64.0.7" || res.Status != StatusBootstrapped {
		t.Fatalf("result = %+v", res)
	}
}

func TestCreateAuthKeyOnlyInBootstrapStdin(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true}); err != nil {
		t.Fatal(err)
	}
	stdins := r.hosts["pve1"].ExecStdins()
	if len(stdins) != 1 || !strings.Contains(stdins[0], "TS_AUTHKEY="+authKey+"\n") {
		t.Fatalf("auth key not on bootstrap stdin")
	}
	for _, e := range r.j.Entries() {
		if strings.Contains(e, authKey) {
			t.Fatalf("auth key in a call's arguments: %s", e)
		}
	}
	err := filepath.Walk(r.m.Cfg.StateDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), authKey) || strings.Contains(string(b), apiToken) {
			t.Errorf("secret written to %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.log.String(), authKey) {
		t.Fatal("auth key logged")
	}
}

func TestCreateBootstrapFailureKeepsStateAndRerunRecovers(t *testing.T) {
	r := newRig(t)
	r.hosts["pve1"].Fail = func(call string) error {
		if strings.HasPrefix(call, "pve1 Exec") {
			return errors.New("apt-get: network unreachable")
		}
		return nil
	}
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if err == nil || errs.ExitCode(err) != 1 || !strings.Contains(err.Error(), "province env create demo --apply") {
		t.Fatalf("err = %v", err)
	}
	if md := r.metadata(t, "demo"); md.Status != StatusBootstrapFailed || md.VMID != 105 {
		t.Fatalf("metadata = %+v", md)
	}

	// Re-run: apply again (no-op for tofu if nothing changed) and bootstrap again.
	r.hosts["pve1"].Fail = nil
	before := len(r.j.Entries())
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true}); err != nil {
		t.Fatal(err)
	}
	dir := r.dir("demo")
	want := []string{"tofu Init " + dir, "tofu Apply " + dir, "tofu Output " + dir, "pve1 Exec 105 bash -s"}
	if got := r.j.Entries()[before:]; !slices.Equal(got, want) {
		t.Fatalf("re-run calls = %v", got)
	}
	if md := r.metadata(t, "demo"); md.Status != StatusBootstrapped {
		t.Fatalf("metadata = %+v", md)
	}
}

func TestCreateMissingResultLineIsBootstrapFailure(t *testing.T) {
	r := newRig(t)
	r.hosts["pve1"].ExecOut = "done?\n"
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true}); err == nil {
		t.Fatal("no error")
	}
	if md := r.metadata(t, "demo"); md.Status != StatusBootstrapFailed {
		t.Fatalf("metadata = %+v", md)
	}
}

func TestCreateExistingBootstrappedRefused(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true}); err != nil {
		t.Fatal(err)
	}
	before := len(r.j.Entries())
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	if len(r.j.Entries()) != before {
		t.Fatalf("calls after refusal: %v", r.j.Entries()[before:])
	}
}

// A moved env whose tofu state still tracks the old guest: tofu apply would
// act on the wrong one, so create --apply refuses before any call.
func TestCreateApplyRefusesTofuOutOfSync(t *testing.T) {
	r := newRig(t)
	r.hosts["pve1"].ExecOut = "done?\n" // bootstrap fails: a re-run would apply again
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true}); err == nil {
		t.Fatal("no error")
	}
	md := r.metadata(t, "demo")
	fix := []string{"cd " + r.dir("demo"), "tofu import proxmox_virtual_environment_container.env pve2/900"}
	md.Host, md.Node, md.VMID, md.TofuOutOfSync, md.TofuFix = "pve2", "pve2", 900, true, fix
	if err := r.m.writeMetadata(r.dir("demo"), md); err != nil {
		t.Fatal(err)
	}
	r.hosts["pve1"].ExecOut = "PROVINCE_TS_IP=100.64.0.7\n"
	before, api, tofus := r.mutations(), r.apiCalls(), len(r.tofuHosts)
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if errs.KindOf(err) != errs.Refused || errs.ExitCode(err) != 2 ||
		!strings.Contains(err.Error(), "tofu state still points at the old guest") || !strings.Contains(err.Error(), "\n  "+fix[1]) {
		t.Fatalf("err = %v", err)
	}
	if r.mutations() != before || r.apiCalls() != api || len(r.tofuHosts) != tofus {
		t.Fatalf("calls after refusal: %v", r.j.Entries()[before:])
	}
	if got := r.metadata(t, "demo"); !got.TofuOutOfSync || got.VMID != 900 {
		t.Fatalf("metadata changed: %+v", got)
	}
}

func TestCreateOtherHostForExistingEnvRefused(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Host: "pve2", Apply: true})
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "pve1") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateHostPreparationBeforeBootstrap(t *testing.T) {
	cases := map[string]struct {
		tun    string
		docker bool
		want   []string // host calls before Exec
	}{
		"plain":      {config.TunUserspace, false, nil},
		"tun device": {config.TunDevice, false, []string{"pve1 Set lxc 105 --dev0 /dev/net/tun", "pve1 Reboot lxc 105"}},
		"docker":     {config.TunUserspace, true, []string{"pve1 Set lxc 105 --features nesting=1,keyctl=1", "pve1 Reboot lxc 105"}},
		"both":       {config.TunDevice, true, []string{"pve1 Set lxc 105 --features nesting=1,keyctl=1 --dev0 /dev/net/tun", "pve1 Reboot lxc 105"}},
	}
	for name, c := range cases {
		r := newRig(t)
		r.m.Cfg.Tailscale.Tun = c.tun
		if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true, Docker: c.docker}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := append(c.want, "pve1 Exec 105 bash -s")
		if got := r.hosts["pve1"].Calls(); !slices.Equal(got, want) {
			t.Errorf("%s: host calls = %v, want %v", name, got, want)
		}
		stdin := r.hosts["pve1"].ExecStdins()[0]
		if !strings.Contains(stdin, "P_TUN="+c.tun+"\n") {
			t.Errorf("%s: payload tun wrong", name)
		}
		if !strings.Contains(stdin, "P_TS_SSH=1\n") {
			t.Errorf("%s: Tailscale SSH not on by default", name)
		}
	}
}

func TestCreateApplyFailureKeepsPlannedStatus(t *testing.T) {
	r := newRig(t)
	r.tofu.Fail = func(call string) error {
		if strings.HasPrefix(call, "Apply") {
			return errors.New("tofu apply: 403 Permission check failed")
		}
		return nil
	}
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if err == nil || errs.ExitCode(err) != 1 || !strings.Contains(err.Error(), "--apply") {
		t.Fatalf("err = %v", err)
	}
	if md := r.metadata(t, "demo"); md.Status != StatusPlanned {
		t.Fatalf("metadata = %+v", md)
	}
	if len(r.hosts["pve1"].Calls()) != 0 {
		t.Fatal("bootstrap ran after a failed apply")
	}
}

func TestCreateUsesTheChosenHostsTofuAndToken(t *testing.T) {
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Host: "pve2"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.tofuHosts, []string{"pve2"}) {
		t.Fatalf("tofu built for %v", r.tofuHosts)
	}
	// pve1 is only asked for its guest list (the cross-host VMID check).
	if c := r.clients["pve1"].Calls(); !slices.Equal(c, []string{"ListGuests pve1"}) || len(r.clients["pve2"].Calls()) < 4 {
		t.Fatalf("preflight asked the wrong host: pve1 %v, pve2 %v", c, r.clients["pve2"].Calls())
	}
}

func TestCreateMissingHostTokenRefusesBeforeAnyCall(t *testing.T) {
	r := newRig(t)
	r.m.Client = func(h config.Host) (proxmox.Client, error) {
		return nil, errs.Refusedf("PROVINCE_TOKEN_PVE1 (API token for host %s) is not set", h.Name)
	}
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if errs.KindOf(err) != errs.Refused || r.mutations() != 0 || len(r.tofuHosts) != 0 {
		t.Fatalf("err = %v, calls = %v", err, r.j.Entries())
	}
}

func TestCreateTailscaleSSHOffReachesPayload(t *testing.T) {
	r := newRig(t)
	off := false
	r.m.Cfg.Tailscale.SSH = &off
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true}); err != nil {
		t.Fatal(err)
	}
	if stdin := r.hosts["pve1"].ExecStdins()[0]; !strings.Contains(stdin, "P_TS_SSH=0\n") {
		t.Fatal("payload does not turn Tailscale SSH off")
	}
}

func TestCreatePicksVMIDFreeOnEveryHost(t *testing.T) {
	r := newRig(t)
	r.clients["pve1"].Guests = []proxmox.Guest{{VMID: 100, Type: "lxc"}, {VMID: 102, Type: "lxc"}}
	r.clients["pve2"].Guests = []proxmox.Guest{{VMID: 101, Type: "qemu"}, {VMID: 103, Type: "lxc"}}
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.VMID != 104 {
		t.Fatalf("vmid = %d, want 104 (100-103 used on pve1 or pve2)", res.VMID)
	}
	tf, _ := os.ReadFile(filepath.Join(r.dir("demo"), "main.tf.json"))
	if !strings.Contains(string(tf), `"vm_id": 104,`) {
		t.Fatalf("main.tf.json lacks explicit vm_id:\n%s", tf)
	}

	r = newRig(t)
	r.m.Cfg.VMIDStart = 500
	r.clients["pve2"].Guests = []proxmox.Guest{{VMID: 500, Type: "qemu"}}
	if res, err := r.m.Create(ctx, CreateOpts{Name: "demo"}); err != nil || res.VMID != 501 {
		t.Fatalf("vmid_start: %+v, %v", res, err)
	}
}

func TestCreateVMIDCheckSkipsHostsWithoutToken(t *testing.T) {
	r := newRig(t)
	r.clients["pve2"].Guests = []proxmox.Guest{{VMID: 100, Type: "qemu"}}
	client := r.m.Client
	r.m.Client = func(h config.Host) (proxmox.Client, error) {
		if h.Name == "pve2" {
			return nil, errs.Refusedf("PROVINCE_TOKEN_PVE2 (API token for host pve2) is not set")
		}
		return client(h)
	}
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.VMID != 100 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "pve2") {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(r.log.String(), "skipped in the VMID check") {
		t.Fatal("warning not logged")
	}
}

func TestCreateKeepsTheVMIDOfACreatedEnv(t *testing.T) {
	r := newRig(t)
	r.hosts["pve1"].Fail = func(c string) error {
		if strings.Contains(c, "Exec") {
			return errors.New("boom")
		}
		return nil
	}
	_, _ = r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true}) // bootstrap_failed, vmid 105 from tofu
	r.hosts["pve1"].Fail = nil
	r.clients["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Type: "lxc"}}
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if err != nil || res.VMID != 105 {
		t.Fatalf("re-run: %+v, %v", res, err)
	}
	tf, _ := os.ReadFile(filepath.Join(r.dir("demo"), "main.tf.json"))
	if !strings.Contains(string(tf), `"vm_id": 105,`) {
		t.Fatal("re-run changed the vm_id of an existing container")
	}
}
