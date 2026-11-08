// Package bootstrap builds the stdin payload that bootstraps a new guest and
// parses its result.
package bootstrap

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/wellavelino/province/internal/sshx"
)

//go:embed bootstrap.sh
var script []byte

// Script returns the embedded bootstrap script.
func Script() []byte { return append([]byte(nil), script...) }

// Params configure one bootstrap run.
type Params struct {
	User     string
	Keys     []string // authorized_keys lines
	Hostname string
	Tag      string // tag:<name>
	Tun      string // userspace | device
	Docker   bool
	TSSSH    bool     // tailscale up --ssh (Tailscale SSH)
	Locales  []string // generated in the guest, e.g. en_US.UTF-8
	AuthKey  string   // Tailscale auth key; travels only on stdin
}

func (p Params) validate() error {
	switch {
	case p.User == "", p.Hostname == "", len(p.Keys) == 0:
		return errors.New("bootstrap: user, hostname and keys are required")
	case !strings.HasPrefix(p.Tag, "tag:"):
		return fmt.Errorf("bootstrap: tag %q must start with tag:", p.Tag)
	case p.Tun != "userspace" && p.Tun != "device":
		return fmt.Errorf("bootstrap: tun %q must be userspace or device", p.Tun)
	case p.AuthKey == "":
		return errors.New("bootstrap: TS_AUTHKEY is not set")
	}
	return nil
}

// Assignments renders the shell variable lines that precede the script.
// They are plain assignments, not exports, so no child process (curl | sh
// installers included) inherits the auth key.
func Assignments(p Params) ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	var b strings.Builder
	for _, kv := range [][2]string{
		{"P_USER", p.User},
		{"P_KEYS", strings.Join(p.Keys, "\n")},
		{"P_HOSTNAME", p.Hostname},
		{"P_TAG", p.Tag},
		{"P_TUN", p.Tun},
		{"P_DOCKER", flag(p.Docker)},
		{"P_TS_SSH", flag(p.TSSSH)},
		{"P_LOCALES", strings.Join(p.Locales, "\n")},
		{"TS_AUTHKEY", p.AuthKey},
	} {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], sshx.Quote(kv[1]))
	}
	return []byte(b.String()), nil
}

// Payload is Assignments followed by the script, for `bash -s` on stdin.
func Payload(p Params) ([]byte, error) {
	a, err := Assignments(p)
	if err != nil {
		return nil, err
	}
	return append(a, script...), nil
}
