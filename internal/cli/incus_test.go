package cli

import (
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/incus"
	"github.com/wellavelino/province/internal/incus/incustest"
)

// incusWorld adds Incus host "box" and a recipe to the world's site config.
func incusWorld(t *testing.T) (*world, Deps, *incustest.Fake) {
	t.Helper()
	w := newWorld(t)
	b, err := os.ReadFile(w.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b) + "  - { name: box, backend: incus, remote: box }\nrecipes:\n  go: { packages: [git], tools: { go: \"1.24\" } }\n"
	if err := os.WriteFile(w.cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	box := &incustest.Fake{Name: "box", Journal: w.j, Pools: []string{"default"}, ExecOut: "PROVINCE_TS_IP=100.64.0.9\n"}
	w.tofu.Outputs = map[string]json.RawMessage{"name": json.RawMessage(`"api"`)}
	d := w.deps()
	d.NewIncus = func(h config.Host, _ io.Writer) incus.Client {
		if h.Remote != "box" {
			t.Errorf("incus client for remote %q", h.Remote)
		}
		return box
	}
	return w, d, box
}

func TestIncusCreateNeedsNoProxmoxToken(t *testing.T) {
	w, d, box := incusWorld(t)
	delete(w.env, "PROXMOX_VE_API_TOKEN")
	delete(w.env, "PROVINCE_TOKEN_PVE2")
	o := runCLI(t, d, "env", "create", "api", "--host", "box", "--recipe", "go", "--apply", "--json")
	if o.code != 0 {
		t.Fatalf("%+v", o)
	}
	var res struct {
		Backend  string `json:"backend"`
		Instance string `json:"instance"`
		Recipe   struct {
			Name  string   `json:"name"`
			Tools []string `json:"tools"`
		} `json:"recipe"`
	}
	if err := json.Unmarshal([]byte(o.stdout), &res); err != nil {
		t.Fatalf("%v: %s", err, o.stdout)
	}
	if res.Backend != "incus" || res.Instance != "api" || res.Recipe.Name != "go" || !slices.Equal(res.Recipe.Tools, []string{"go@1.24"}) {
		t.Fatalf("result = %+v", res)
	}
	if !slices.Equal(w.tofuTok, []string{""}) {
		t.Fatalf("tofu tokens = %q, want one empty", w.tofuTok)
	}
	if calls := box.Calls(); len(calls) != 4 || calls[1] != "box Exec api sh -c tailscale ip -4 2>/dev/null || true" || calls[2] != "box Exec api bash -s" || calls[3] != "box Exec api bash -s" {
		t.Fatalf("incus calls = %v", calls)
	}
}

func TestIncusHumanOutputAndProvision(t *testing.T) {
	_, d, box := incusWorld(t)
	o := runCLI(t, d, "env", "create", "api", "--host", "box", "--recipe", "go")
	if o.code != 0 || !strings.Contains(o.stdout, "as instance api") || !strings.Contains(o.stdout, "tools     go@1.24") {
		t.Fatalf("plan: %+v", o)
	}
	o = runCLI(t, d, "env", "create", "api", "--apply")
	if o.code != 0 || !strings.Contains(o.stdout, "(instance api)") || !strings.Contains(o.stdout, "Applied recipe go") {
		t.Fatalf("apply: %+v", o)
	}
	box.List = []incus.Instance{{Name: "api", Type: "container", Status: "running", Env: "api"}}
	o = runCLI(t, d, "env", "provision", "api")
	if o.code != 0 || !strings.Contains(o.stdout, "Applied recipe go to api") {
		t.Fatalf("provision: %+v", o)
	}
	o = runCLI(t, d, "env", "list")
	if o.code != 0 || !strings.Contains(o.stdout, "GUEST") || !strings.Contains(o.stdout, "running") {
		t.Fatalf("list: %+v", o)
	}
}

func TestGuestMoveRefusesIncusHosts(t *testing.T) {
	w, d, _ := incusWorld(t)
	o := runCLI(t, d, "guest", "move", "105", "--from", "pve1", "--to", "box", "--yes")
	if o.code != 2 || !strings.Contains(o.stderr, "Proxmox hosts only") {
		t.Fatalf("%+v", o)
	}
	if w.calls() != 0 {
		t.Fatalf("calls: %v", w.j.Entries())
	}
}

func TestKindVMOnIncusAndRefusedOnProxmox(t *testing.T) {
	w, d, box := incusWorld(t)
	o := runCLI(t, d, "env", "create", "api", "--host", "box", "--kind", "vm", "--apply", "--json")
	if o.code != 0 {
		t.Fatalf("%+v", o)
	}
	var res struct {
		Kind     string `json:"kind"`
		Instance string `json:"instance"`
	}
	decodeOne(t, o.stdout, &res)
	if res.Kind != "vm" || res.Instance != "api" {
		t.Fatalf("result = %+v", res)
	}
	if calls := box.Calls(); !slices.Contains(calls, "box Exec api true") {
		t.Fatalf("no agent wait: %v", calls)
	}

	before := len(w.j.Entries())
	o = runCLI(t, d, "--json", "env", "create", "web", "--host", "pve1", "--kind", "vm")
	if o.code != 2 {
		t.Fatalf("exit = %d, want 2: %+v", o.code, o)
	}
	var doc map[string]string
	decodeOne(t, o.stdout, &doc)
	if doc["code"] != "refused" || !strings.Contains(doc["error"], "needs a cloud-init template with qemu-guest-agent (planned)") {
		t.Fatalf("error doc = %v", doc)
	}
	if len(w.j.Entries()) != before {
		t.Fatalf("calls after refusal: %v", w.j.Entries()[before:])
	}
}
