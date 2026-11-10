package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/env"
	"github.com/wellavelino/province/internal/hostops"
	"github.com/wellavelino/province/internal/hostops/hostopstest"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/proxmox/proxmoxtest"
	"github.com/wellavelino/province/internal/run/runtest"
	"github.com/wellavelino/province/internal/tofu"
	"github.com/wellavelino/province/internal/tofu/tofutest"
)

const (
	tmpl     = "local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst"
	apiToken = "province@pve!cli=0b8e7d6c-1111-2222-3333-444455556666"
	token2   = "province@pve!cli2=7f3a9c1e-aaaa-bbbb-cccc-ddddeeeeffff"
	authKey  = "tskey-auth-kSECRET-0123456789"
	apiKey   = "tskey-api-kSECRET-9876543210"
)

type world struct {
	t        *testing.T
	cfgPath  string
	stateDir string
	env      map[string]string
	tofu     *tofutest.Fake
	tofuErr  error
	api      map[string]*proxmoxtest.Fake
	hosts    map[string]*hostopstest.Fake
	runner   *runtest.Fake
	j        *hostopstest.Journal
	tailnet  *fakeTailnet
	tokens   map[string]string // expected API token per host
	tofuTok  []string          // token each NewTofu call got
	unset    []string          // variables removed from the environment
}

type fakeTailnet struct{ calls []string }

func (f *fakeTailnet) DeleteDeviceByHostname(_ context.Context, h string) (bool, error) {
	f.calls = append(f.calls, h)
	return true, nil
}

func newWorld(t *testing.T) *world {
	t.Helper()
	tmp := t.TempDir()
	key := filepath.Join(tmp, "id.pub")
	os.WriteFile(key, []byte("ssh-ed25519 AAAAC3Nz dev@laptop\n"), 0o600)
	w := &world{t: t, stateDir: filepath.Join(tmp, "state"), j: &hostopstest.Journal{}, tailnet: &fakeTailnet{}}
	w.cfgPath = filepath.Join(tmp, "province.yaml")
	cfg := fmt.Sprintf(`state_dir: %s
user: dev
ssh_public_keys: [%s]
tailscale: { tag: "tag:dev" }
defaults: { host: pve1, storage: local-lvm, bridge: vmbr0, template: %q }
hosts:
  - { name: pve1, node: pve1, endpoint: "https://pve1.example:8006", ssh: pve1 }
  - { name: pve2, node: pve2, endpoint: "https://pve2.example:8006", ssh: pve2, token_env: PROVINCE_TOKEN_PVE2 }
`, w.stateDir, key, tmpl)
	if err := os.WriteFile(w.cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	w.env = map[string]string{
		"PROVINCE_CONFIG":      w.cfgPath,
		"PROXMOX_VE_API_TOKEN": apiToken,
		"PROVINCE_TOKEN_PVE2":  token2,
		"TS_AUTHKEY":           authKey,
	}
	w.tokens = map[string]string{"pve1": apiToken, "pve2": token2}
	w.tofu = &tofutest.Fake{PlanText: "Plan: 1 to add", Changes: true, Outputs: map[string]json.RawMessage{"vmid": json.RawMessage("105")}}
	w.tofu.OnCall = func(c string) { w.j.Add("tofu " + c) }
	w.api = map[string]*proxmoxtest.Fake{}
	w.hosts = map[string]*hostopstest.Fake{}
	for _, h := range []string{"pve1", "pve2"} {
		w.api[h] = &proxmoxtest.Fake{
			Templates: []string{tmpl},
			BridgeLs:  []string{"vmbr0"},
			StorageLs: []proxmox.Storage{{Name: "local-lvm", Content: []string{"rootdir", "images"}, Avail: 100 << 30, Active: true}},
			Configs:   map[int]map[string]string{},
		}
		w.hosts[h] = &hostopstest.Fake{Name: h, Journal: w.j, State: "stopped",
			ExecOut: "PROVINCE_TS_IP=100.64.0.7\n", Avail: map[string]int64{"/var/lib/vz/dump": 100 << 30}}
	}
	w.runner = &runtest.Fake{T: t, OnCall: func(c runtest.Call) { w.j.Add("run " + c.String()) }}
	w.runner.On([]string{"dump"}, runtest.Response{Stdout: "archive"})
	w.runner.On([]string{"receive"}, runtest.Response{})
	return w
}

func (w *world) deps() Deps {
	return Deps{
		Getenv:   func(k string) string { return w.env[k] },
		Unsetenv: func(k string) { w.unset = append(w.unset, k) },
		Now:      func() time.Time { return time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC) },
		Runner:   w.runner,
		NewTofu: func(token string, _ io.Writer) (tofu.Tofu, error) {
			w.tofuTok = append(w.tofuTok, token)
			return w.tofu, w.tofuErr
		},
		NewClient: func(h config.Host, token string) proxmox.Client {
			if token != w.tokens[h.Name] {
				w.t.Errorf("client for %s got the wrong token", h.Name)
			}
			return w.api[h.Name]
		},
		NewHost:    func(h config.Host, _ io.Writer) hostops.Host { return w.hosts[h.Name] },
		NewTailnet: func(string, string, string) env.Tailnet { return w.tailnet },
	}
}

func (w *world) run(args ...string) outcome { return runCLI(w.t, w.deps(), args...) }

func (w *world) calls() int {
	n := len(w.j.Entries())
	for _, a := range w.api {
		n += len(a.Calls())
	}
	return n
}

func TestJSONSuccessShapes(t *testing.T) {
	w := newWorld(t)

	o := w.run("--json", "env", "create", "demo")
	var plan map[string]any
	decodeOne(t, o.stdout, &plan)
	if o.code != 0 || plan["status"] != "planned" || plan["plan"] != "Plan: 1 to add" || plan["changes"] != true {
		t.Fatalf("plan: %d %v %s", o.code, plan, o.stderr)
	}

	o = w.run("--json", "env", "create", "demo", "--apply")
	var created map[string]any
	decodeOne(t, o.stdout, &created)
	if o.code != 0 || created["status"] != "bootstrapped" || created["vmid"] != 105.0 || created["ssh"] != "ssh dev@demo" || created["tailscale_ip"] != "100.64.0.7" {
		t.Fatalf("apply: %d %v %s", o.code, created, o.stderr)
	}

	w.api["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: "lxc"}}
	o = w.run("--json", "env", "list")
	var list struct{ Envs []map[string]any }
	decodeOne(t, o.stdout, &list)
	if o.code != 0 || len(list.Envs) != 1 || list.Envs[0]["name"] != "demo" || list.Envs[0]["live_status"] != "running" {
		t.Fatalf("list: %d %v", o.code, list)
	}

	w.hosts["pve1"].State = "running"
	w.hosts["pve1"].ExecOuts = map[string]string{"tailscale status --json --peers=false": `{"Self":{"DNSName":"demo.tail0000.ts.net.","CapMap":{"https":null,"funnel":null}}}`}
	o = w.run("--json", "env", "expose", "demo", "--port", "3000", "--yes")
	var exposed map[string]any
	decodeOne(t, o.stdout, &exposed)
	if o.code != 0 || exposed["public_url"] != "https://demo.tail0000.ts.net/" || exposed["expose_port"] != 3000.0 || exposed["exposed"] != true {
		t.Fatalf("expose: %d %v %s", o.code, exposed, o.stderr)
	}

	o = w.run("--json", "env", "show", "demo")
	var show map[string]any
	decodeOne(t, o.stdout, &show)
	if o.code != 0 || show["live_status"] != "running" || show["ssh"] != "ssh dev@demo" || show["host"] != "pve1" ||
		show["public_url"] != "https://demo.tail0000.ts.net/" || show["expose_port"] != 3000.0 {
		t.Fatalf("show: %d %v", o.code, show)
	}

	o = w.run("--json", "env", "unexpose", "demo", "--yes")
	var unexposed map[string]any
	decodeOne(t, o.stdout, &unexposed)
	if o.code != 0 || unexposed["exposed"] != false || unexposed["public_url"] != nil {
		t.Fatalf("unexpose: %d %v %s", o.code, unexposed, o.stderr)
	}

	w.env["TS_API_KEY"] = apiKey
	o = w.run("--json", "env", "destroy", "demo", "--yes")
	var destroyed map[string]any
	decodeOne(t, o.stdout, &destroyed)
	if o.code != 0 || destroyed["destroyed"] != true || len(w.tailnet.calls) != 1 {
		t.Fatalf("destroy: %d %v %s", o.code, destroyed, o.stderr)
	}
	if _, ok := destroyed["warnings"].([]any); !ok {
		t.Fatalf("destroy warnings not a list: %v", destroyed)
	}
}

func TestJSONMoveShape(t *testing.T) {
	w := newWorld(t)
	w.api["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: "lxc"}}
	w.api["pve1"].Configs[105] = map[string]string{"rootfs": "local-lvm:vm-105-disk-0,size=8G", "net0": "name=eth0,bridge=vmbr0"}
	w.hosts["pve1"].State = "running"
	o := w.run("guest", "move", "105", "--from", "pve1", "--to", "pve2", "--yes", "--json")
	var res map[string]any
	decodeOne(t, o.stdout, &res)
	if o.code != 0 || res["vmid"] != 105.0 || res["type"] != "lxc" || res["target"] != "stopped" || res["dump_removed"] != true {
		t.Fatalf("move: %d %v %s", o.code, res, o.stderr)
	}
}

func TestYesRequiredBeforeAnyCall(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{
		{"--json", "env", "destroy", "demo"},
		{"--json", "env", "expose", "demo", "--port", "3000"},
		{"--json", "env", "unexpose", "demo"},
		{"--json", "guest", "move", "105", "--from", "pve1", "--to", "pve2"},
	} {
		o := w.run(args...)
		var doc errorDoc
		decodeOne(t, o.stdout, &doc)
		if o.code != 2 || doc.Code != "refused" || !strings.Contains(doc.Error, "--yes") {
			t.Errorf("%v: %d %+v", args, o.code, doc)
		}
	}
	if w.calls() != 0 {
		t.Fatalf("calls made: %v", w.j.Entries())
	}
}

func TestSecretsRedactedInBothModes(t *testing.T) {
	w := newWorld(t)
	w.api["pve1"].Err = errors.New("proxy said: Authorization PVEAPIToken=" + apiToken + " rejected, key " + authKey)
	o := w.run("--json", "env", "create", "demo")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || doc.Code != "preflight" {
		t.Fatalf("json: %d %+v", o.code, doc)
	}
	for _, out := range []string{o.stdout, o.stderr} {
		if strings.Contains(out, apiToken) || strings.Contains(out, authKey) || strings.Contains(out, "0b8e7d6c-1111") {
			t.Fatalf("secret printed: %s", out)
		}
	}
	if !strings.Contains(doc.Error, "[redacted]") {
		t.Fatalf("no redaction marker: %s", doc.Error)
	}

	o = w.run("env", "create", "demo")
	if o.code != 2 || o.stdout != "" || !strings.Contains(o.stderr, "[redacted]") || strings.Contains(o.stderr, apiToken) || strings.Contains(o.stderr, authKey) {
		t.Fatalf("human: %+v", o)
	}
}

func TestBootstrapFailureIsExit1Failed(t *testing.T) {
	w := newWorld(t)
	w.hosts["pve1"].Fail = func(c string) error {
		if strings.Contains(c, "Exec") {
			return errors.New("apt failed for key " + authKey)
		}
		return nil
	}
	o := w.run("--json", "env", "create", "demo", "--apply")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 1 || doc.Code != "failed" || !strings.Contains(doc.Error, "--apply") || strings.Contains(doc.Error, authKey) {
		t.Fatalf("%d %+v", o.code, doc)
	}
}

func TestConfigFlagWinsOverEnv(t *testing.T) {
	w := newWorld(t)
	good := w.cfgPath
	w.env["PROVINCE_CONFIG"] = filepath.Join(t.TempDir(), "missing.yaml")
	if o := w.run("--json", "env", "list"); o.code != 2 || !strings.Contains(o.stdout, "missing.yaml") {
		t.Fatalf("env path not used: %+v", o)
	}
	if o := w.run("--json", "--config", good, "env", "list"); o.code != 0 {
		t.Fatalf("flag not used: %+v", o)
	}
	delete(w.env, "PROVINCE_CONFIG")
	o := w.run("--json", "env", "list")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || !strings.Contains(doc.Error, "PROVINCE_CONFIG") {
		t.Fatalf("no config: %d %+v", o.code, doc)
	}
}

func TestMissingTokenAndTofuAreExit2(t *testing.T) {
	w := newWorld(t)
	delete(w.env, "PROXMOX_VE_API_TOKEN")
	o := w.run("--json", "env", "create", "demo")
	if o.code != 2 || !strings.Contains(o.stdout, "PROXMOX_VE_API_TOKEN") || !strings.Contains(o.stdout, "pve1") || w.calls() != 0 {
		t.Fatalf("no token: %+v", o)
	}

	w = newWorld(t)
	w.tofuErr = errors.New("tofu not found on PATH: install OpenTofu")
	o = w.run("--json", "env", "create", "demo")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || doc.Code != "preflight" || w.calls() != 0 {
		t.Fatalf("no tofu: %d %+v", o.code, doc)
	}
}

func TestMoveRefusalsAndFailuresExitCodes(t *testing.T) {
	w := newWorld(t)
	if o := w.run("--json", "guest", "move", "abc", "--from", "pve1", "--to", "pve2", "--yes"); o.code != 2 {
		t.Fatalf("bad vmid: %+v", o)
	}
	o := w.run("--json", "guest", "move", "105", "--from", "pve1", "--to", "pve1", "--yes")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || doc.Code != "refused" {
		t.Fatalf("same host: %d %+v", o.code, doc)
	}
	// Unknown guest: preflight, exit 2.
	o = w.run("--json", "guest", "move", "105", "--from", "pve1", "--to", "pve2", "--yes")
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || doc.Code != "preflight" {
		t.Fatalf("missing guest: %d %+v", o.code, doc)
	}
}

func TestHumanOutput(t *testing.T) {
	w := newWorld(t)
	o := w.run("env", "create", "demo")
	if o.code != 0 || !strings.Contains(o.stdout, "Plan: 1 to add") || !strings.Contains(o.stdout, "--apply") {
		t.Fatalf("%+v", o)
	}
	o = w.run("env", "list")
	if o.code != 0 || !strings.Contains(o.stdout, "NAME") || !strings.Contains(o.stdout, "demo") {
		t.Fatalf("%+v", o)
	}
}

func TestCreatePassesTheEnvHostsTokenToTofu(t *testing.T) {
	w := newWorld(t)
	if o := w.run("--json", "env", "create", "demo", "--host", "pve2", "--apply"); o.code != 0 {
		t.Fatalf("%+v", o)
	}
	if len(w.tofuTok) != 1 || w.tofuTok[0] != token2 {
		t.Fatalf("tofu tokens: got %d, pve2's = %v", len(w.tofuTok), len(w.tofuTok) == 1 && w.tofuTok[0] == token2)
	}
	// Destroy of that env also uses pve2's token, even with pve1's unset.
	delete(w.env, "PROXMOX_VE_API_TOKEN")
	if o := w.run("--json", "env", "destroy", "demo", "--yes"); o.code != 0 {
		t.Fatalf("destroy: %+v", o)
	}
	if len(w.tofuTok) != 2 || w.tofuTok[1] != token2 {
		t.Fatal("destroy used another host's token")
	}
}

func TestOnlyTouchedHostsNeedTokens(t *testing.T) {
	w := newWorld(t)
	delete(w.env, "PROVINCE_TOKEN_PVE2")
	// Creating on pve1 does not need pve2's token.
	if o := w.run("--json", "env", "create", "demo"); o.code != 0 {
		t.Fatalf("create on pve1: %+v", o)
	}
}

func TestMoveMissingTargetTokenRefusesBeforeAnyCall(t *testing.T) {
	w := newWorld(t)
	delete(w.env, "PROVINCE_TOKEN_PVE2")
	o := w.run("--json", "guest", "move", "105", "--from", "pve1", "--to", "pve2", "--yes")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || doc.Code != "refused" || !strings.Contains(doc.Error, "PROVINCE_TOKEN_PVE2") || !strings.Contains(doc.Error, "pve2") {
		t.Fatalf("%d %+v", o.code, doc)
	}
	if w.calls() != 0 {
		t.Fatalf("calls before refusal: %v", w.j.Entries())
	}
}

func TestBothTokensRedactedAndUnset(t *testing.T) {
	w := newWorld(t)
	w.api["pve1"].Err = errors.New("echo " + apiToken + " and " + token2 + " and bare " + secretPart(token2))
	o := w.run("--json", "env", "create", "demo")
	for _, s := range []string{apiToken, token2, secretPart(apiToken), secretPart(token2)} {
		if strings.Contains(o.stdout+o.stderr, s) {
			t.Fatalf("token printed: %s", o.stdout)
		}
	}
	if strings.Count(o.stdout, "[redacted]") != 3 {
		t.Fatalf("redactions: %s", o.stdout)
	}
	for _, v := range []string{"PROXMOX_VE_API_TOKEN", "PROVINCE_TOKEN_PVE2"} {
		if !slices.Contains(w.unset, v) {
			t.Errorf("%s not removed from the environment (unset %v)", v, w.unset)
		}
	}
}

func secretPart(tok string) string { _, s, _ := strings.Cut(tok, "="); return s }

// TestMoveSendsEachHostItsOwnToken runs a move against two httptest PVE
// APIs with the real client: each rejects any token but its own.
func TestMoveSendsEachHostItsOwnToken(t *testing.T) {
	w := newWorld(t)
	routes := map[string]map[string]string{
		"pve1": {
			"/api2/json/nodes/pve1/lxc":            `{"data":[{"vmid":105,"name":"demo","status":"running"}]}`,
			"/api2/json/nodes/pve1/qemu":           `{"data":[]}`,
			"/api2/json/nodes/pve1/lxc/105/config": `{"data":{"rootfs":"local-lvm:vm-105-disk-0,size=8G","net0":"name=eth0,bridge=vmbr0"}}`,
		},
		"pve2": {
			"/api2/json/nodes/pve2/lxc":     `{"data":[]}`,
			"/api2/json/nodes/pve2/qemu":    `{"data":[]}`,
			"/api2/json/nodes/pve2/network": `{"data":[{"iface":"vmbr0"}]}`,
			"/api2/json/nodes/pve2/storage": `{"data":[{"storage":"local-lvm","type":"lvmthin","content":"rootdir,images","avail":107374182400,"active":1}]}`,
		},
	}
	seen := map[string]int{}
	endpoints := map[string]string{}
	for host, tok := range w.tokens {
		host, tok := host, tok
		srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "PVEAPIToken="+tok {
				http.Error(rw, "bad token", http.StatusUnauthorized)
				return
			}
			body, ok := routes[host][r.URL.Path]
			if !ok {
				http.Error(rw, "no route", http.StatusNotImplemented)
				return
			}
			seen[host]++
			io.WriteString(rw, body)
		}))
		t.Cleanup(srv.Close)
		endpoints[host] = srv.URL
	}
	w.hosts["pve1"].State = "running"
	d := w.deps()
	d.NewClient = func(h config.Host, token string) proxmox.Client {
		return proxmox.New(endpoints[h.Name], true, token)
	}
	o := runCLI(t, d, "--json", "guest", "move", "105", "--from", "pve1", "--to", "pve2", "--yes")
	if o.code != 0 {
		t.Fatalf("move: %+v", o)
	}
	if seen["pve1"] == 0 || seen["pve2"] == 0 {
		t.Fatalf("API calls per host: %v", seen)
	}
}

func TestMovingAProvinceEnvReHomesIt(t *testing.T) {
	w := newWorld(t)
	if o := w.run("--json", "env", "create", "demo", "--apply"); o.code != 0 {
		t.Fatalf("create: %+v", o)
	}
	w.api["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: "lxc"}}
	w.api["pve1"].Configs[105] = map[string]string{"rootfs": "local-lvm:vm-105-disk-0,size=8G", "net0": "name=eth0,bridge=vmbr0"}
	w.hosts["pve1"].State = "running"
	w.tofuTok = nil
	o := w.run("--json", "guest", "move", "105", "--from", "pve1", "--to", "pve2", "--to-vmid", "900", "--yes")
	var res struct {
		TargetVMID int `json:"target_vmid"`
		Env        *struct {
			Name          string `json:"name"`
			Host          string `json:"host"`
			VMID          int    `json:"vmid"`
			TofuOutOfSync bool   `json:"tofu_out_of_sync"`
		} `json:"env"`
	}
	decodeOne(t, o.stdout, &res)
	if o.code != 0 || res.TargetVMID != 900 || res.Env == nil || res.Env.Name != "demo" || res.Env.Host != "pve2" || res.Env.VMID != 900 || res.Env.TofuOutOfSync {
		t.Fatalf("move: %d %+v %s", o.code, res, o.stderr)
	}
	if len(w.tofuTok) != 1 || w.tofuTok[0] != token2 {
		t.Fatal("re-import did not use the target host's token")
	}
	var imported bool
	for _, c := range w.tofu.Calls() {
		imported = imported || strings.HasSuffix(c, "proxmox_virtual_environment_container.env pve2/900")
	}
	if !imported {
		t.Fatalf("tofu calls = %v", w.tofu.Calls())
	}
	w.api["pve2"].Guests = []proxmox.Guest{{VMID: 900, Name: "demo", Status: "stopped", Type: "lxc"}}
	o = w.run("--json", "env", "show", "demo")
	var show map[string]any
	decodeOne(t, o.stdout, &show)
	if show["host"] != "pve2" || show["vmid"] != 900.0 || show["live_status"] != "stopped" {
		t.Fatalf("show after move: %v", show)
	}
}

func TestGuestPrune(t *testing.T) {
	w := newWorld(t)
	o := w.run("--json", "guest", "prune", "105", "--host", "pve1")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || doc.Code != "refused" || !strings.Contains(doc.Error, "--yes") || w.calls() != 0 {
		t.Fatalf("no --yes: %d %+v", o.code, doc)
	}

	w.api["pve1"].Guests = []proxmox.Guest{{VMID: 105, Name: "demo", Status: "stopped", Type: "lxc"}}
	w.api["pve1"].Configs[105] = map[string]string{"onboot": "0"}
	w.api["pve2"].Guests = []proxmox.Guest{{VMID: 106, Name: "demo", Status: "running", Type: "lxc"}}
	o = w.run("--json", "guest", "prune", "105", "--host", "pve1", "--yes")
	var res map[string]any
	decodeOne(t, o.stdout, &res)
	if o.code != 0 || res["destroyed"] != true || res["type"] != "lxc" {
		t.Fatalf("prune: %d %v %s", o.code, res, o.stderr)
	}
	if e := w.j.Entries(); len(e) != 1 || e[0] != "pve1 Destroy lxc 105" {
		t.Fatalf("calls = %v", e)
	}
}

func TestGuestPruneRefusesALiveEnv(t *testing.T) {
	w := newWorld(t)
	if o := w.run("--json", "env", "create", "demo", "--apply"); o.code != 0 { // recorded as pve1/105
		t.Fatalf("create: %+v", o)
	}
	before := len(w.j.Entries())
	o := w.run("--json", "guest", "prune", "105", "--host", "pve1", "--yes")
	var doc errorDoc
	decodeOne(t, o.stdout, &doc)
	if o.code != 2 || !strings.Contains(doc.Error, "env destroy demo") || len(w.j.Entries()) != before {
		t.Fatalf("%d %+v", o.code, doc)
	}
}
