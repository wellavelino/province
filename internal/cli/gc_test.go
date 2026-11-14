package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wellavelino/province/internal/proxmox"
)

// runAt runs the CLI with the clock at now.
func (w *world) runAt(now time.Time, args ...string) outcome {
	d := w.deps()
	d.Now = func() time.Time { return now }
	return runCLI(w.t, d, args...)
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"1s": time.Second, "90m": 90 * time.Minute, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour} {
		if got, err := parseTTL(in); err != nil || got != want {
			t.Errorf("%q: %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0", "0s", "-1h", "0d", "-2d", "1.5d", "d", "soon"} {
		if _, err := parseTTL(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestTTLAndGC(t *testing.T) {
	w := newWorld(t)
	t0 := time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC)
	if o := w.runAt(t0, "--json", "env", "create", "demo", "--ttl", "0s"); o.code != 2 || !strings.Contains(o.stdout, "--ttl") {
		t.Fatalf("zero ttl: %+v", o)
	}
	if o := w.runAt(t0, "--json", "env", "create", "demo", "--apply", "--ttl", "2h"); o.code != 0 {
		t.Fatalf("create: %+v", o)
	}
	w.api["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: "lxc"}}

	var list struct{ Envs []map[string]any }
	o := w.runAt(t0.Add(time.Hour), "--json", "env", "list")
	decodeOne(t, o.stdout, &list)
	if o.code != 0 || list.Envs[0]["expires_at"] != "2026-11-08T12:15:00Z" || list.Envs[0]["expired"] != false {
		t.Fatalf("list before: %+v", list)
	}
	later := t0.Add(3 * time.Hour)
	o = w.runAt(later, "--json", "env", "list")
	decodeOne(t, o.stdout, &list)
	if list.Envs[0]["expired"] != true {
		t.Fatalf("list after: %+v", list)
	}
	o = w.runAt(later, "env", "list")
	if !strings.Contains(o.stdout, "EXPIRES") || !strings.Contains(o.stdout, "2026-11-08T12:15:00Z (expired)") {
		t.Fatalf("list text:\n%s", o.stdout)
	}
	var show map[string]any
	o = w.runAt(later, "--json", "env", "show", "demo")
	decodeOne(t, o.stdout, &show)
	if show["expired"] != true || show["expires_at"] != "2026-11-08T12:15:00Z" {
		t.Fatalf("show: %v", show)
	}

	// Before expiry, gc has nothing to do.
	var res struct {
		Envs   []map[string]any
		Failed int
	}
	o = w.runAt(t0, "--json", "env", "gc")
	decodeOne(t, o.stdout, &res)
	if o.code != 0 || len(res.Envs) != 0 {
		t.Fatalf("gc early: %+v", o)
	}

	// Dry run: refused like destroy without --yes, nothing touched.
	before := w.calls()
	o = w.runAt(later, "--json", "env", "gc")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || doc.Code != "refused" || !strings.Contains(doc.Error, "demo") || !strings.Contains(doc.Error, "--yes") || w.calls() != before {
		t.Fatalf("gc dry run: %d %+v", o.code, doc)
	}

	o = w.runAt(later, "--json", "env", "gc", "--yes")
	decodeOne(t, o.stdout, &res)
	if o.code != 0 || len(res.Envs) != 1 || res.Envs[0]["outcome"] != "destroyed" || res.Envs[0]["name"] != "demo" {
		t.Fatalf("gc: %+v", o)
	}
	if _, err := os.Stat(filepath.Join(w.stateDir, "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state kept")
	}
}

func TestGCFailureIsExit1WithOneDocument(t *testing.T) {
	w := newWorld(t)
	t0 := time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC)
	w.runAt(t0, "env", "create", "demo", "--apply", "--ttl", "1s")
	w.tofu.Fail = func(c string) error {
		if strings.HasPrefix(c, "Destroy ") {
			return errors.New("state lock held")
		}
		return nil
	}
	o := w.runAt(t0.Add(time.Second), "--json", "env", "gc", "--yes")
	var res struct {
		Envs   []map[string]any
		Failed int
	}
	decodeOne(t, o.stdout, &res)
	if o.code != 1 || res.Failed != 1 || res.Envs[0]["outcome"] != "failed" || !strings.Contains(res.Envs[0]["error"].(string), "lock held") {
		t.Fatalf("gc: %+v", o)
	}
	o = w.runAt(t0.Add(time.Second), "env", "gc", "--yes")
	if o.code != 1 || !strings.Contains(o.stdout, "FAILED demo") || !strings.Contains(o.stderr, "1 of 1") {
		t.Fatalf("gc text: %+v", o)
	}
}

func TestProtectCommand(t *testing.T) {
	w := newWorld(t)
	if o := w.run("env", "create", "demo", "--apply", "--protect"); o.code != 0 {
		t.Fatalf("create: %+v", o)
	}
	w.api["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: "lxc"}}
	before := w.calls()
	o := w.run("--json", "env", "destroy", "demo", "--yes")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || doc.Code != "refused" || !strings.Contains(doc.Error, "province env protect demo --off") || w.calls() != before {
		t.Fatalf("destroy protected: %d %+v", o.code, doc)
	}
	var show map[string]any
	decodeOne(t, w.run("--json", "env", "show", "demo").stdout, &show)
	if show["protected"] != true {
		t.Fatalf("show: %v", show)
	}

	var res map[string]any
	o = w.run("--json", "env", "protect", "demo", "--off")
	decodeOne(t, o.stdout, &res)
	if o.code != 0 || res["protected"] != false || res["name"] != "demo" {
		t.Fatalf("protect --off: %+v", o)
	}
	o = w.run("--json", "env", "protect", "demo")
	decodeOne(t, o.stdout, &res)
	if o.code != 0 || res["protected"] != true {
		t.Fatalf("protect: %+v", o)
	}
	if o = w.run("env", "protect", "demo", "--off"); o.code != 0 || !strings.Contains(o.stdout, "no longer protected") {
		t.Fatalf("protect --off text: %+v", o)
	}
	if o = w.run("--json", "env", "destroy", "demo", "--yes"); o.code != 0 {
		t.Fatalf("destroy: %+v", o)
	}
	if o = w.run("--json", "env", "protect", "nope"); o.code != 1 {
		t.Fatalf("unknown env: %+v", o)
	}
}
