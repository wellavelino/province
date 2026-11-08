package bootstrap

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
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
			return "", fmt.Errorf("bootstrap reported a bad tailscale address %q", v)
		}
		return addr.String(), nil
	}
	return "", errors.New("bootstrap finished without reporting " + resultPrefix + "<ip>")
}
