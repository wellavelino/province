package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/wellavelino/province/internal/errs"
)

// Commands run in the guest (pct exec <vmid> -- ...), checked against the
// tailscale 1.104 CLI source.
var (
	tsStatusArgv = []string{"tailscale", "status", "--json", "--peers=false"}
	// `serve --https=443 off` removes the 443 handler and its funnel flag.
	// Unlike `funnel ... off` it skips the funnel capability check, so it
	// still works after the policy stopped allowing Funnel. Flags go before
	// `off`: the CLI's flag parsing stops at the first positional argument.
	unexposeArgv = []string{"tailscale", "serve", "--https=443", "--yes", "off"}
)

// exposeArgv publishes https://<node>/ (port 443) to localhost:<port>. The
// timeout caps tailscale's interactive enablement wait, which the
// capability check before it should already rule out.
func exposeArgv(port int) []string {
	return []string{"timeout", "120", "tailscale", "funnel", "--bg", strconv.Itoa(port)}
}

// ExposeResult is the outcome of env expose and env unexpose.
type ExposeResult struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	VMID      int    `json:"vmid"`
	Port      int    `json:"expose_port,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
	Exposed   bool   `json:"exposed"`
}

func validPort(p int) error {
	if p < 1 || p > 65535 {
		return errs.Refusedf("port %d is not between 1 and 65535", p)
	}
	return nil
}

// Expose publishes port of env name to the internet through Tailscale
// Funnel and records it. Without yes it refuses before touching anything.
func (m *Manager) Expose(ctx context.Context, name string, port int, yes bool) (*ExposeResult, error) {
	if err := validPort(port); err != nil {
		return nil, err
	}
	if !yes {
		return nil, errs.Refusedf("env expose %s publishes port %d to the public internet (Tailscale Funnel): pass --yes", name, port)
	}
	md, host, err := m.runningEnv(ctx, name, "expose")
	if err != nil {
		return nil, err
	}
	url, err := m.funnelOn(ctx, host, md, port)
	if err != nil {
		return nil, err
	}
	md.ExposePort, md.PublicURL = port, url
	if err := m.writeMetadata(m.dir(name), md); err != nil {
		return nil, err
	}
	return &ExposeResult{Name: name, Host: md.Host, VMID: md.VMID, Port: port, PublicURL: url, Exposed: true}, nil
}

// Unexpose turns Funnel off in env name and clears the record.
func (m *Manager) Unexpose(ctx context.Context, name string, yes bool) (*ExposeResult, error) {
	if !yes {
		return nil, errs.Refusedf("env unexpose %s turns off its public URL: pass --yes", name)
	}
	md, g, err := m.runningEnv(ctx, name, "unexpose")
	if err != nil {
		return nil, err
	}
	// "handler does not exist": nothing is served on 443, already off.
	if err := g.Exec(ctx, nil, nil, unexposeArgv...); err != nil && !strings.Contains(err.Error(), "handler does not exist") {
		return nil, fmt.Errorf("turning off Funnel in %s: %w", name, err)
	}
	md.ExposePort, md.PublicURL = 0, ""
	if err := m.writeMetadata(m.dir(name), md); err != nil {
		return nil, err
	}
	return &ExposeResult{Name: name, Host: md.Host, VMID: md.VMID}, nil
}

// runningEnv loads a bootstrapped env whose container is running: Funnel
// and recipes run through exec in the guest, which needs it running.
func (m *Manager) runningEnv(ctx context.Context, name, verb string) (*Metadata, guest, error) {
	md, err := m.load(name)
	if err != nil {
		return nil, nil, err
	}
	if md.Status != StatusBootstrapped || !md.created() {
		return nil, nil, errs.Refusedf("env %s is %s, not bootstrapped: finish `province env create %s --apply` before env %s", name, md.Status, name, verb)
	}
	h, err := m.hostOf(md)
	if err != nil {
		return nil, nil, err
	}
	b := m.backendFor(h)
	switch found, err := b.verify(ctx, md); {
	case err != nil:
		return nil, nil, err
	case !found:
		return nil, nil, errs.Preflightf("env %s's container (%s) is not on %s", name, md.guestRef(h), md.Host)
	}
	g, err := b.guest(md)
	if err != nil {
		return nil, nil, err
	}
	st, err := g.Status(ctx)
	if err != nil {
		return nil, nil, &errs.Error{Kind: errs.Preflight, Err: err}
	}
	if st != "running" {
		return nil, nil, errs.Refusedf("env %s (%s on %s) is %s; start it first (%s), then re-run env %s",
			name, md.guestRef(h), md.Host, st, g.StartHint(), verb)
	}
	return md, g, nil
}

// funnelOn checks that the guest's node may use Funnel, publishes port and
// returns the public URL.
func (m *Manager) funnelOn(ctx context.Context, g guest, md *Metadata, port int) (string, error) {
	var out strings.Builder
	if err := g.Exec(ctx, nil, &out, tsStatusArgv...); err != nil {
		return "", fmt.Errorf("tailscale status in %s: %w", md.Name, err)
	}
	self, err := parseSelf([]byte(out.String()))
	if err != nil {
		return "", fmt.Errorf("tailscale status in %s: %w", md.Name, err)
	}
	// Without both capabilities tailscale either exits 0 after printing an
	// enablement link (nothing published) or waits for an admin; refuse
	// before running it.
	if missing := self.missingCaps(); missing != "" {
		return "", errs.Preflightf("Tailscale Funnel is not allowed for %s (%s). %s", md.Name, missing, funnelHint(m.Cfg.Tailscale.Tag, md.Name, port))
	}
	if err := g.Exec(ctx, nil, nil, exposeArgv(port)...); err != nil {
		if funnelDenied(err) {
			return "", fmt.Errorf("tailscale funnel in %s: %w\n%s", md.Name, err, funnelHint(m.Cfg.Tailscale.Tag, md.Name, port))
		}
		return "", fmt.Errorf("tailscale funnel in %s: %w", md.Name, err)
	}
	return self.publicURL(), nil
}

// tsSelf is the part of `tailscale status --json` province reads.
type tsSelf struct {
	DNSName      string                     `json:"DNSName"`
	CapMap       map[string]json.RawMessage `json:"CapMap"`
	Capabilities []string                   `json:"Capabilities"`
}

func parseSelf(b []byte) (*tsSelf, error) {
	var st struct {
		Self *tsSelf `json:"Self"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("bad JSON: %w", err)
	}
	if st.Self == nil {
		return nil, errors.New("no Self node: is the guest on the tailnet?")
	}
	if strings.TrimSuffix(st.Self.DNSName, ".") == "" {
		return nil, errors.New("the node has no DNS name: enable MagicDNS in the admin console (DNS page)")
	}
	return st.Self, nil
}

func (s *tsSelf) has(c string) bool {
	if _, ok := s.CapMap[c]; ok {
		return true
	}
	for _, v := range s.Capabilities {
		if v == c {
			return true
		}
	}
	return false
}

// missingCaps names the absent node capabilities Funnel needs.
func (s *tsSelf) missingCaps() string {
	var miss []string
	if !s.has("https") {
		miss = append(miss, "HTTPS certificates are not enabled")
	}
	if !s.has("funnel") {
		miss = append(miss, `the "funnel" node attribute is not set`)
	}
	return strings.Join(miss, "; ")
}

func (s *tsSelf) publicURL() string {
	return "https://" + strings.TrimSuffix(s.DNSName, ".") + "/"
}

// funnelDenied reports whether tailscale refused Funnel for lack of the
// node attribute, HTTPS or an allowed port.
func funnelDenied(err error) bool {
	msg := err.Error()
	for _, s := range []string{"Funnel not available", "node attribute", "HTTPS must be enabled", "not allowed for funnel"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func funnelHint(tag, name string, port int) string {
	return fmt.Sprintf("In the Tailscale admin console: enable HTTPS certificates (DNS page), and add to the tailnet policy file\n"+
		"  \"nodeAttrs\": [{\"target\": [%q], \"attr\": [\"funnel\"]}]\n"+
		"then run `province env expose %s --port %d --yes`.", tag, name, port)
}
