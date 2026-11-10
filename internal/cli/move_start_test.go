package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/proxmox"
)

// moveEnvWithStart creates env demo on pve1/105, then moves it to pve2 with
// --start; failStart makes the start on pve2 fail.
func moveEnvWithStart(t *testing.T, failStart bool) (*world, outcome) {
	t.Helper()
	w := newWorld(t)
	w.env["TS_API_KEY"] = apiKey
	if o := w.run("--json", "env", "create", "demo", "--apply"); o.code != 0 {
		t.Fatalf("create: %+v", o)
	}
	w.api["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: "lxc"}}
	w.api["pve1"].Configs[105] = map[string]string{"rootfs": "local-lvm:vm-105-disk-0,size=8G", "net0": "name=eth0,bridge=vmbr0"}
	w.hosts["pve1"].State = "running"
	if failStart {
		w.hosts["pve2"].Fail = func(call string) error {
			if strings.HasPrefix(call, "pve2 Start") {
				return errors.New("startup for container '105' failed")
			}
			return nil
		}
	}
	return w, w.run("--json", "guest", "move", "105", "--from", "pve1", "--to", "pve2", "--start", "--yes")
}

// A failed start after a completed restore must still re-home the env, so a
// later env destroy acts on the moved copy, never on the rollback copy.
func TestMoveStartFailureStillReHomesTheEnv(t *testing.T) {
	w, o := moveEnvWithStart(t, true)
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 1 || doc.Code != "failed" || !strings.Contains(doc.Error, "did not start") ||
		!strings.Contains(doc.Error, "`pct start 105` on pve2") || !strings.Contains(doc.Error, "env demo now recorded on pve2") {
		t.Fatalf("move: %d %+v", o.code, doc)
	}
	b, err := os.ReadFile(filepath.Join(w.stateDir, "demo", "province.json"))
	if err != nil {
		t.Fatal(err)
	}
	var md map[string]any
	decodeOne(t, string(b), &md)
	if md["host"] != "pve2" && md["tofu_out_of_sync"] != true {
		t.Fatalf("province.json still points at the rollback copy: %s", b)
	}

	// The rollback copy on pve1 must survive env destroy.
	w.api["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "stopped", Type: "lxc"}}
	w.api["pve2"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "stopped", Type: "lxc"}}
	n := len(w.j.Entries())
	w.run("--json", "env", "destroy", "demo", "--yes")
	if slices.ContainsFunc(w.j.Entries()[n:], func(c string) bool { return strings.HasPrefix(c, "pve1 ") }) {
		t.Fatalf("destroy touched the rollback copy on pve1: %v", w.j.Entries()[n:])
	}
	for _, tok := range w.tofuTok[len(w.tofuTok)-1:] {
		if tok != token2 {
			t.Fatal("destroy ran tofu with pve1's token: it targets the rollback copy")
		}
	}
}

func TestMoveStartSuccessReHomesTheEnv(t *testing.T) {
	w, o := moveEnvWithStart(t, false)
	var res struct {
		Target string `json:"target"`
		Env    *struct {
			Host          string `json:"host"`
			TofuOutOfSync bool   `json:"tofu_out_of_sync"`
		} `json:"env"`
	}
	decodeOne(t, o.stdout, &res)
	if o.code != 0 || res.Target != "running" || res.Env == nil || res.Env.Host != "pve2" || res.Env.TofuOutOfSync {
		t.Fatalf("move: %d %+v %s", o.code, res, o.stderr)
	}
	if w.hosts["pve2"].State != "running" {
		t.Fatalf("pve2 state = %s", w.hosts["pve2"].State)
	}
}
