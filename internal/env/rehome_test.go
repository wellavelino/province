package env

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/errs"
)

func movedEnv(t *testing.T, r *rig) {
	t.Helper()
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true}); err != nil {
		t.Fatal(err)
	}
	r.tofuHosts = nil
}

func TestFindByGuest(t *testing.T) {
	r := newRig(t)
	seed(t, r,
		Metadata{Name: "a", Host: "pve1", VMID: 105, Status: StatusBootstrapped},
		Metadata{Name: "b", Host: "pve2", VMID: 105, Status: StatusBootstrapped},
		Metadata{Name: "c", Host: "pve1", Status: StatusPlanned})
	for _, c := range []struct {
		host string
		vmid int
		want string
	}{{"pve1", 105, "a"}, {"pve2", 105, "b"}, {"pve1", 106, ""}, {"pve1", 0, ""}} {
		if got, err := r.m.FindByGuest(c.host, c.vmid); err != nil || got != c.want {
			t.Errorf("FindByGuest(%s, %d) = %q, %v; want %q", c.host, c.vmid, got, err, c.want)
		}
	}
}

func TestRehomeSuccess(t *testing.T) {
	r := newRig(t)
	movedEnv(t, r)
	before := len(r.j.Entries())
	to, _ := r.m.Cfg.HostByName("pve2")
	to.TokenEnv = "PROVINCE_TOKEN_PVE2"
	res := r.m.Rehome(ctx, "demo", to, 900, "ssd")
	if res.TofuOutOfSync || res.Error != "" {
		t.Fatalf("result = %+v", res)
	}
	dir := r.dir("demo")
	want := []string{
		"tofu Init " + dir,
		"tofu StateRm " + dir + " proxmox_virtual_environment_container.env",
		"tofu Import " + dir + " proxmox_virtual_environment_container.env pve2/900",
	}
	if got := r.j.Entries()[before:]; !slices.Equal(got, want) {
		t.Fatalf("calls = %v", got)
	}
	if !slices.Equal(r.tofuHosts, []string{"pve2"}) {
		t.Fatalf("tofu built for %v, want the target host (its token)", r.tofuHosts)
	}
	md := r.metadata(t, "demo")
	if md.Host != "pve2" || md.Node != "pve2" || md.VMID != 900 || md.Storage != "ssd" || md.TofuOutOfSync || md.TofuFix != nil {
		t.Fatalf("metadata = %+v", md)
	}
	tf, _ := os.ReadFile(filepath.Join(dir, "main.tf.json"))
	for _, s := range []string{`"node_name": "pve2"`, `"vm_id": 900,`, `"endpoint": "https://pve2.example:8006"`, `"datastore_id": "ssd"`} {
		if !strings.Contains(string(tf), s) {
			t.Errorf("main.tf.json lacks %s", s)
		}
	}
	if len(r.hosts["pve1"].Calls())+len(r.hosts["pve2"].Calls()) != 2 { // only create's probe and bootstrap Execs
		t.Fatal("rehome touched a guest")
	}
}

func TestRehomeImportFailureMarksOutOfSyncAndDestroyRefuses(t *testing.T) {
	r := newRig(t)
	movedEnv(t, r)
	r.tofu.Fail = func(c string) error {
		if strings.HasPrefix(c, "Import") {
			return errors.New("tofu import: 401 authentication failure")
		}
		return nil
	}
	to, _ := r.m.Cfg.HostByName("pve2")
	to.TokenEnv = "PROVINCE_TOKEN_PVE2"
	res := r.m.Rehome(ctx, "demo", to, 900, "")
	if !res.TofuOutOfSync || !strings.Contains(res.Error, "401") {
		t.Fatalf("result = %+v", res)
	}
	dir := r.dir("demo")
	wantFix := []string{
		"cd " + dir,
		`export PROXMOX_VE_API_TOKEN="$PROVINCE_TOKEN_PVE2"`,
		"tofu init -input=false",
		"tofu import proxmox_virtual_environment_container.env pve2/900",
		`then remove "tofu_out_of_sync" and "tofu_fix" from ` + filepath.Join(dir, MetadataFile),
	}
	if !slices.Equal(res.Fix, wantFix) { // state rm already done: not repeated
		t.Fatalf("fix = %q", res.Fix)
	}
	md := r.metadata(t, "demo")
	if !md.TofuOutOfSync || !slices.Equal(md.TofuFix, wantFix) || md.Host != "pve2" || md.VMID != 900 {
		t.Fatalf("metadata = %+v", md)
	}
	if !strings.Contains(r.log.String(), "tofu import proxmox_virtual_environment_container.env pve2/900") {
		t.Fatal("fix commands not printed")
	}

	before := len(r.j.Entries())
	_, err := r.m.Destroy(ctx, "demo", true)
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "tofu import proxmox_virtual_environment_container.env pve2/900") {
		t.Fatalf("destroy: %v", err)
	}
	if len(r.j.Entries()) != before {
		t.Fatalf("destroy made calls: %v", r.j.Entries()[before:])
	}
}

func TestRehomeStateRmFailureKeepsBothSteps(t *testing.T) {
	r := newRig(t)
	movedEnv(t, r)
	r.tofu.Fail = func(c string) error {
		if strings.HasPrefix(c, "StateRm") {
			return errors.New("state locked")
		}
		return nil
	}
	to, _ := r.m.Cfg.HostByName("pve2")
	res := r.m.Rehome(ctx, "demo", to, 105, "")
	if !res.TofuOutOfSync || !slices.Contains(res.Fix, "tofu state rm proxmox_virtual_environment_container.env") {
		t.Fatalf("result = %+v", res)
	}
	if slices.ContainsFunc(res.Fix, func(s string) bool { return strings.HasPrefix(s, "export ") }) {
		t.Fatal("export step for a host on the default token variable")
	}
}
