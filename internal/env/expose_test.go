package env

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/run"
)

const (
	statusCmd   = "tailscale status --json --peers=false"
	funnelCmd   = "timeout 120 tailscale funnel --bg 3000"
	unexposeCmd = "tailscale serve --https=443 --yes off"
	demoURL     = "https://demo.tail0000.ts.net/"
	// Trimmed `tailscale status --json --peers=false` of a node allowed to use Funnel.
	statusFunnelOK = `{"Version":"1.104.5","BackendState":"Running","Self":{"HostName":"demo","DNSName":"demo.tail0000.ts.net.",` +
		`"CapMap":{"https":null,"funnel":null,"https://tailscale.com/cap/funnel-ports?ports=443,8443,10000":null}}}`
	statusNoFunnel = `{"Self":{"DNSName":"demo.tail0000.ts.net.","CapMap":{"https":null}}}`
)

// exposable seeds a bootstrapped, running demo whose node may use Funnel.
func exposable(t *testing.T, r *rig) {
	t.Helper()
	bootstrapped(t, r)
	r.hosts["pve1"].ExecOuts = map[string]string{statusCmd: statusFunnelOK}
}

func TestExposePublishesAndRecordsURL(t *testing.T) {
	r := newRig(t)
	exposable(t, r)
	res, err := r.m.Expose(ctx, "demo", 3000, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pve1 Status lxc 105", "pve1 Exec 105 " + statusCmd, "pve1 Exec 105 " + funnelCmd}
	if got := r.j.Entries(); !slices.Equal(got, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if res.PublicURL != demoURL || res.Port != 3000 || !res.Exposed {
		t.Fatalf("result = %+v", res)
	}
	md := r.metadata(t, "demo")
	if md.ExposePort != 3000 || md.PublicURL != demoURL {
		t.Fatalf("metadata = %+v", md)
	}
	// list and show carry it from province.json.
	views, err := r.m.List(ctx)
	if err != nil || len(views) != 1 || views[0].PublicURL != demoURL || views[0].ExposePort != 3000 {
		t.Fatalf("list = %+v, %v", views, err)
	}

	// Re-running is idempotent: same commands, same record.
	if _, err := r.m.Expose(ctx, "demo", 3000, true); err != nil {
		t.Fatal(err)
	}
	if md := r.metadata(t, "demo"); md.PublicURL != demoURL {
		t.Fatalf("metadata after re-run = %+v", md)
	}
}

func TestExposeRefusalsTouchNothing(t *testing.T) {
	cases := map[string]struct {
		port  int
		yes   bool
		state string
		want  string
	}{
		"no --yes":      {3000, false, "running", "public internet"},
		"port 0":        {0, true, "running", "between 1 and 65535"},
		"port too high": {70000, true, "running", "between 1 and 65535"},
		"stopped":       {3000, true, "stopped", "`pct start 105` on pve1"},
	}
	for name, c := range cases {
		r := newRig(t)
		exposable(t, r)
		r.hosts["pve1"].State = c.state
		_, err := r.m.Expose(ctx, "demo", c.port, c.yes)
		if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		for _, e := range r.j.Entries() {
			if strings.Contains(e, "Exec") {
				t.Errorf("%s: ran %s", name, e)
			}
		}
		if md := r.metadata(t, "demo"); md.ExposePort != 0 || md.PublicURL != "" {
			t.Errorf("%s: metadata = %+v", name, md)
		}
	}

	// A planned env has no guest to publish from.
	r := newRig(t)
	seed(t, r, Metadata{Name: "demo", Host: "pve1", Node: "pve1", Status: StatusPlanned})
	if _, err := r.m.Expose(ctx, "demo", 3000, true); errs.KindOf(err) != errs.Refused || r.mutations() != 0 {
		t.Fatalf("planned: err = %v, calls %v", err, r.j.Entries())
	}
}

func TestExposeFunnelNotAllowedHint(t *testing.T) {
	// Capabilities missing: refuse before running `tailscale funnel`, which
	// would otherwise exit 0 after printing an enablement link.
	r := newRig(t)
	exposable(t, r)
	r.hosts["pve1"].ExecOuts[statusCmd] = statusNoFunnel
	_, err := r.m.Expose(ctx, "demo", 3000, true)
	if errs.KindOf(err) != errs.Preflight ||
		!strings.Contains(err.Error(), `the "funnel" node attribute is not set`) ||
		!strings.Contains(err.Error(), `"nodeAttrs": [{"target": ["tag:dev"], "attr": ["funnel"]}]`) ||
		!strings.Contains(err.Error(), "enable HTTPS certificates (DNS page)") {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(r.j.Entries(), "pve1 Exec 105 "+funnelCmd) {
		t.Fatal("ran tailscale funnel without the capability")
	}
	if md := r.metadata(t, "demo"); md.ExposePort != 0 || md.PublicURL != "" {
		t.Fatalf("metadata = %+v", md)
	}

	// The funnel command itself refuses (e.g. port 443 not in funnel-ports).
	stderr := map[string]bool{
		"port 443 is not allowed for funnel; allowed ports are: 8443":                     true,
		"Funnel not available; HTTPS must be enabled. See https://tailscale.com/s/https.": true,
		"dial unix /var/run/tailscale/tailscaled.sock: connect: no such file":             false,
	}
	for msg, hint := range stderr {
		r := newRig(t)
		exposable(t, r)
		r.hosts["pve1"].Fail = func(call string) error {
			if strings.HasSuffix(call, funnelCmd) {
				return &run.ExitError{Cmd: "ssh pve1 pct exec 105 -- tailscale funnel", Code: 1, Stderr: msg}
			}
			return nil
		}
		_, err := r.m.Expose(ctx, "demo", 3000, true)
		if err == nil || strings.Contains(err.Error(), `"attr": ["funnel"]`) != hint {
			t.Errorf("%q: hint %v, err = %v", msg, hint, err)
		}
		if md := r.metadata(t, "demo"); md.ExposePort != 0 {
			t.Errorf("%q: metadata = %+v", msg, md)
		}
	}
}

func TestParseSelf(t *testing.T) {
	s, err := parseSelf([]byte(statusFunnelOK))
	if err != nil || s.publicURL() != demoURL || s.missingCaps() != "" {
		t.Fatalf("self = %+v, %v", s, err)
	}
	// Older clients list capabilities only in the deprecated array.
	s, err = parseSelf([]byte(`{"Self":{"DNSName":"demo.tail0000.ts.net.","Capabilities":["https","funnel"]}}`))
	if err != nil || s.missingCaps() != "" {
		t.Fatalf("capabilities array: %+v, %v", s, err)
	}
	s, _ = parseSelf([]byte(`{"Self":{"DNSName":"demo.tail0000.ts.net."}}`))
	if m := s.missingCaps(); !strings.Contains(m, "HTTPS certificates") || !strings.Contains(m, "funnel") {
		t.Fatalf("missing = %q", m)
	}
	for in, want := range map[string]string{
		`{"BackendState":"NeedsLogin"}`: "no Self node",
		`{"Self":{"DNSName":""}}`:       "enable MagicDNS",
		`not json`:                      "bad JSON",
	} {
		if _, err := parseSelf([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", in, err)
		}
	}
}

func TestUnexpose(t *testing.T) {
	r := newRig(t)
	exposable(t, r)
	if _, err := r.m.Unexpose(ctx, "demo", false); errs.KindOf(err) != errs.Refused || r.mutations() != 0 {
		t.Fatalf("no --yes: err = %v, calls %v", err, r.j.Entries())
	}
	if _, err := r.m.Expose(ctx, "demo", 3000, true); err != nil {
		t.Fatal(err)
	}

	// Stopped: refused, record kept (the funnel config comes back on start).
	r.hosts["pve1"].State = "stopped"
	if _, err := r.m.Unexpose(ctx, "demo", true); errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "pct start 105") {
		t.Fatalf("stopped: err = %v", err)
	}
	r.hosts["pve1"].State = "running"

	// A failure keeps the record.
	r.hosts["pve1"].Fail = func(call string) error {
		if strings.HasSuffix(call, unexposeCmd) {
			return errors.New("connection reset")
		}
		return nil
	}
	if _, err := r.m.Unexpose(ctx, "demo", true); err == nil {
		t.Fatal("failure ignored")
	}
	if md := r.metadata(t, "demo"); md.PublicURL != demoURL {
		t.Fatalf("metadata after failure = %+v", md)
	}

	r.hosts["pve1"].Fail = nil
	res, err := r.m.Unexpose(ctx, "demo", true)
	if err != nil || res.Exposed || res.PublicURL != "" {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if e := r.j.Entries(); e[len(e)-1] != "pve1 Exec 105 "+unexposeCmd {
		t.Fatalf("calls = %v", e)
	}
	if md := r.metadata(t, "demo"); md.ExposePort != 0 || md.PublicURL != "" {
		t.Fatalf("metadata = %+v", md)
	}

	// Already off ("handler does not exist") is success.
	r.hosts["pve1"].Fail = func(call string) error {
		if strings.HasSuffix(call, unexposeCmd) {
			return &run.ExitError{Cmd: "ssh", Code: 1, Stderr: "error: failed to remove web serve: handler does not exist"}
		}
		return nil
	}
	if _, err := r.m.Unexpose(ctx, "demo", true); err != nil {
		t.Fatalf("already off: %v", err)
	}
}

func TestCreateExposeIsRecordedAndAppliedAfterBootstrap(t *testing.T) {
	// Plan only: recorded, nothing runs in a guest.
	r := newRig(t)
	if _, err := r.m.Create(ctx, CreateOpts{Name: "demo", Expose: 3000}); err != nil {
		t.Fatal(err)
	}
	if md := r.metadata(t, "demo"); md.ExposePort != 3000 || md.PublicURL != "" {
		t.Fatalf("planned metadata = %+v", md)
	}
	// Apply without repeating --expose keeps the recorded port and
	// publishes it right after the bootstrap.
	r.hosts["pve1"].ExecOuts = map[string]string{statusCmd: statusFunnelOK}
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	e := r.j.Entries()
	if want := []string{"pve1 Exec 105 bash -s", "pve1 Exec 105 " + statusCmd, "pve1 Exec 105 " + funnelCmd}; !slices.Equal(e[len(e)-3:], want) {
		t.Fatalf("calls = %v", e)
	}
	if res.PublicURL != demoURL || res.ExposePort != 3000 {
		t.Fatalf("result = %+v", res)
	}
	if md := r.metadata(t, "demo"); md.PublicURL != demoURL || md.Status != StatusBootstrapped {
		t.Fatalf("metadata = %+v", md)
	}

	if _, err := newRig(t).m.Create(ctx, CreateOpts{Name: "demo", Expose: -1}); errs.KindOf(err) != errs.Refused {
		t.Fatalf("bad port: %v", err)
	}
}

func TestCreateExposeFailureKeepsTheBootstrappedEnv(t *testing.T) {
	r := newRig(t)
	r.hosts["pve1"].ExecOuts = map[string]string{statusCmd: statusNoFunnel}
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Expose: 3000, Apply: true})
	// The guest exists: a failure after changes (exit 1), with the retry.
	if errs.KindOf(err) != errs.Failed || !strings.Contains(err.Error(), "province env expose demo --port 3000 --yes") ||
		!strings.Contains(err.Error(), `"attr": ["funnel"]`) {
		t.Fatalf("err = %v (kind %s)", err, errs.KindOf(err))
	}
	md := r.metadata(t, "demo")
	if md.Status != StatusBootstrapped || md.TailscaleIP != "100.64.0.7" || md.ExposePort != 3000 || md.PublicURL != "" {
		t.Fatalf("metadata = %+v", md)
	}
}
