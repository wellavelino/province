package bootstrap

import (
	"errors"
	"net/netip"
	"strings"

	"github.com/wellavelino/province/internal/errs"
)

const resultPrefix = "PROVINCE_TS_IP="

// ParseResult returns the Tailscale IPv4 address the script reported on its
// last PROVINCE_TS_IP= line.
func ParseResult(stdout string) (string, error) {
	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		v, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), resultPrefix)
		if !ok {
			continue
		}
		addr, err := netip.ParseAddr(v)
		if err != nil || !addr.Is4() {
			return "", errs.WithGuestOutput(errors.New("bootstrap reported a bad tailscale address (in the guest output)"), v)
		}
		return addr.String(), nil
	}
	return "", errors.New("bootstrap finished without reporting " + resultPrefix + "<ip>")
}

// ProbeArgv is the command province runs in the guest, before the
// bootstrap, to learn whether the node already joined the tailnet. It is
// the script's own check, `tailscale ip -4`, which prints nothing useful
// until the node is logged in (and on a fresh guest tailscale is not even
// installed). Its errors are silenced and its exit status is always 0, so
// a fresh guest logs no noise and only a failed exec fails the probe.
func ProbeArgv() []string { return []string{"sh", "-c", "tailscale ip -4 2>/dev/null || true"} }

// Joined reports whether the probe's stdout (from a run that succeeded) is
// a Tailscale IPv4 address, i.e. the node is on the tailnet and the
// bootstrap must not get the auth key again. Anything else counts as not
// joined, which only means the key is sent, as on a first run.
func Joined(stdout string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(stdout), "\n")
	addr, err := netip.ParseAddr(strings.TrimSpace(first))
	return err == nil && addr.Is4()
}
