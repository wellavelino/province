package env

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/proxmox"
)

// clock makes r's Manager read *now.
func clock(r *rig) *time.Time {
	now := fixedNow
	r.m.Now = func() time.Time { return now }
	return &now
}

func TestMetadataLifetimeRoundTrip(t *testing.T) {
	r := newRig(t)
	exp := fixedNow.Add(2 * time.Hour)
	seed(t, r, Metadata{Name: "a", Host: "pve1", VMID: 105, Status: StatusBootstrapped, TTL: "2h0m0s", ExpiresAt: exp, Protected: true},
		Metadata{Name: "b", Host: "pve1", VMID: 106, Status: StatusBootstrapped})
	md := r.metadata(t, "a")
	if md.TTL != "2h0m0s" || !md.ExpiresAt.Equal(exp) || !md.Protected {
		t.Fatalf("round trip: %+v", md)
	}
	b, err := os.ReadFile(r.dir("a") + "/" + MetadataFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"expires_at": "2026-11-08T12:15:00Z"`) || !strings.Contains(string(b), `"protected": true`) {
		t.Fatalf("province.json:\n%s", b)
	}
	b, err = os.ReadFile(r.dir("b") + "/" + MetadataFile)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ttl", "expires_at", "protected"} {
		if _, ok := raw[k]; ok {
			t.Errorf("unset %s written: %s", k, b)
		}
	}
}

func TestCreateTTLStartsAtApply(t *testing.T) {
	r := newRig(t)
	now := clock(r)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", TTL: 90 * time.Minute, Protect: true}); err != nil {
		t.Fatal(err)
	}
	md := r.metadata(t, "demo")
	if md.TTL != "1h30m0s" || !md.ExpiresAt.IsZero() || !md.Protected {
		t.Fatalf("plan: %+v", md)
	}
	*now = now.Add(time.Hour + 300*time.Millisecond)
	// --apply without --ttl or --protect keeps what the plan recorded.
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true}); err != nil {
		t.Fatal(err)
	}
	md = r.metadata(t, "demo")
	want := fixedNow.Add(2*time.Hour + 30*time.Minute + time.Second) // rounded up to the second
	if !md.ExpiresAt.Equal(want) || md.ExpiresAt.Location() != time.UTC || !md.Protected {
		t.Fatalf("apply: expires %v, want %v (%+v)", md.ExpiresAt, want, md)
	}

	if _, err := r.m.Create(ctx, CreateOpts{Name: "x", TTL: -time.Second}); errs.KindOf(err) != errs.Refused {
		t.Fatalf("negative ttl: %v", err)
	}
}

func TestCreateApplyWithShortTTL(t *testing.T) {
	r := newRig(t)
	now := clock(r)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true, TTL: time.Second}); err != nil {
		t.Fatal(err)
	}
	if md := r.metadata(t, "demo"); !md.ExpiresAt.Equal(fixedNow.Add(time.Second)) {
		t.Fatalf("expires %v", md.ExpiresAt)
	}
	r.clients["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: proxmox.TypeLXC}}
	if v, _ := r.m.Show(ctx, "demo"); v.Expired {
		t.Fatal("expired at once")
	}
	*now = now.Add(time.Second)
	if v, _ := r.m.Show(ctx, "demo"); !v.Expired {
		t.Fatal("not expired after its ttl")
	}
}

func TestListExpired(t *testing.T) {
	r := newRig(t)
	now := clock(r)
	seed(t, r,
		Metadata{Name: "a", Host: "pve1", Status: StatusPlanned, ExpiresAt: fixedNow.Add(time.Hour)},
		Metadata{Name: "b", Host: "pve1", Status: StatusPlanned, ExpiresAt: fixedNow.Add(-time.Minute)},
		Metadata{Name: "c", Host: "pve1", Status: StatusPlanned},
	)
	expired := func() map[string]bool {
		views, err := r.m.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, v := range views {
			got[v.Name] = v.Expired
		}
		return got
	}
	if got := expired(); got["a"] || !got["b"] || got["c"] {
		t.Fatalf("now: %v", got)
	}
	*now = now.Add(time.Hour)
	if got := expired(); !got["a"] || !got["b"] || got["c"] {
		t.Fatalf("an hour later: %v", got)
	}
}

// expiredEnvs seeds three expired bootstrapped envs (a, b on pve1; p
// protected) and one live one (z).
func expiredEnvs(t *testing.T, r *rig) {
	t.Helper()
	past := fixedNow.Add(-time.Minute)
	seed(t, r,
		Metadata{Name: "a", Host: "pve1", Node: "pve1", VMID: 105, Status: StatusBootstrapped, ExpiresAt: past},
		Metadata{Name: "b", Host: "pve1", Node: "pve1", VMID: 106, Status: StatusBootstrapped, ExpiresAt: past},
		Metadata{Name: "p", Host: "pve1", Node: "pve1", VMID: 107, Status: StatusBootstrapped, ExpiresAt: past, Protected: true},
		Metadata{Name: "z", Host: "pve1", Node: "pve1", VMID: 108, Status: StatusBootstrapped, ExpiresAt: fixedNow.Add(time.Hour)},
	)
	r.hosts["pve1"].State = "stopped"
	r.clients["pve1"].Guests = []proxmox.Guest{
		{VMID: 105, Name: "a", Status: "stopped", Type: proxmox.TypeLXC},
		{VMID: 106, Name: "b", Status: "stopped", Type: proxmox.TypeLXC},
		{VMID: 107, Name: "p", Status: "stopped", Type: proxmox.TypeLXC},
		{VMID: 108, Name: "z", Status: "stopped", Type: proxmox.TypeLXC},
	}
}

func exists(r *rig, name string) bool {
	_, err := os.Stat(r.dir(name))
	return err == nil
}

func TestGCDryRunTouchesNothing(t *testing.T) {
	r := newRig(t)
	clock(r)
	expiredEnvs(t, r)
	res, err := r.m.GC(ctx, false)
	if res != nil || errs.KindOf(err) != errs.Refused || errs.ExitCode(err) != 2 {
		t.Fatalf("res %+v, err %v", res, err)
	}
	for _, s := range []string{"2 expired env(s)", "a, b", "skipping protected: p", "--yes"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("message lacks %q: %v", s, err)
		}
	}
	if r.mutations() != 0 || r.apiCalls() != 0 {
		t.Fatalf("calls: %v", r.j.Entries())
	}
}

func TestGCNothingExpired(t *testing.T) {
	r := newRig(t)
	clock(r)
	seed(t, r,
		Metadata{Name: "z", Host: "pve1", VMID: 108, Status: StatusBootstrapped, ExpiresAt: fixedNow.Add(time.Hour)},
		Metadata{Name: "p", Host: "pve1", VMID: 107, Status: StatusBootstrapped, ExpiresAt: fixedNow, Protected: true},
	)
	for _, yes := range []bool{false, true} {
		res, err := r.m.GC(ctx, yes)
		if err != nil || len(res.Envs) != 1 || res.Envs[0].Outcome != GCSkipped || res.Envs[0].Reason != "protected" {
			t.Fatalf("yes=%v: %+v, %v", yes, res, err)
		}
	}
	if r.mutations() != 0 || !exists(r, "p") || !exists(r, "z") {
		t.Fatalf("calls: %v", r.j.Entries())
	}
}

func TestGCDestroysExpiredAndContinuesPastAFailure(t *testing.T) {
	r := newRig(t)
	clock(r)
	expiredEnvs(t, r)
	r.tofu.Fail = func(c string) error {
		if c == "Destroy "+r.dir("a") {
			return errors.New("tofu destroy: lock held")
		}
		return nil
	}
	res, err := r.m.GC(ctx, true)
	if err == nil || errs.ExitCode(err) != 1 || res == nil {
		t.Fatalf("res %+v, err %v", res, err)
	}
	got := map[string]GCEnv{}
	for _, e := range res.Envs {
		got[e.Name] = e
	}
	if len(got) != 3 || got["a"].Outcome != GCFailed || !strings.Contains(got["a"].Error, "lock held") || got["a"].Code != errs.Failed ||
		got["b"].Outcome != GCDestroyed || got["p"].Outcome != GCSkipped || res.Failed != 1 {
		t.Fatalf("result: %+v", res)
	}
	if !exists(r, "a") || exists(r, "b") || !exists(r, "p") || !exists(r, "z") {
		t.Fatal("wrong state dirs removed")
	}
}

func TestGCReportsDestroyRefusals(t *testing.T) {
	r := newRig(t)
	clock(r)
	seed(t, r, Metadata{Name: "m", Host: "pve2", Node: "pve2", VMID: 900, Status: StatusBootstrapped, ExpiresAt: fixedNow,
		TofuOutOfSync: true, TofuFix: []string{"tofu import x pve2/900"}})
	res, err := r.m.GC(ctx, true)
	if err == nil || len(res.Envs) != 1 || res.Envs[0].Outcome != GCFailed || res.Envs[0].Code != errs.Refused ||
		!strings.Contains(res.Envs[0].Error, "tofu import x pve2/900") {
		t.Fatalf("res %+v, err %v", res, err)
	}
	if r.mutations() != 0 || !exists(r, "m") {
		t.Fatalf("calls: %v", r.j.Entries())
	}
}

func TestProtectRefusesDestroy(t *testing.T) {
	r := newRig(t)
	bootstrapped(t, r)
	if res, err := r.m.Protect("demo", true); err != nil || !res.Protected {
		t.Fatalf("protect: %+v, %v", res, err)
	}
	_, err := r.m.Destroy(ctx, "demo", true)
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "province env protect demo --off") {
		t.Fatalf("destroy: %v", err)
	}
	if r.mutations() != 0 || r.apiCalls() != 0 || !exists(r, "demo") {
		t.Fatalf("calls: %v", r.j.Entries())
	}
	if res, err := r.m.Protect("demo", false); err != nil || res.Protected || r.metadata(t, "demo").Protected {
		t.Fatalf("unprotect: %+v, %v", res, err)
	}
	if _, err := r.m.Destroy(ctx, "demo", true); err != nil {
		t.Fatalf("destroy after --off: %v", err)
	}
	if _, err := r.m.Protect("nope", true); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown env: %v", err)
	}
}
