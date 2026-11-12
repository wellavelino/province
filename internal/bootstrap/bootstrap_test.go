package bootstrap

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/errs"
)

func params() Params {
	return Params{
		User:     "dev",
		Keys:     []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk+/9Qx= dev@laptop", "ssh-ed25519 AAAAB ci's key"},
		Hostname: "demo",
		Tag:      "tag:dev",
		Tun:      "userspace",
		Docker:   true,
		TSSSH:    true,
		Locales:  []string{"en_US.UTF-8", "pt_BR.UTF-8"},
		AuthKey:  "tskey-auth-k123-abc",
	}
}

func TestPayloadIsAssignmentsThenScript(t *testing.T) {
	p, err := Payload(params())
	if err != nil {
		t.Fatal(err)
	}
	want := "P_USER=dev\n" +
		"P_KEYS='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk+/9Qx= dev@laptop\nssh-ed25519 AAAAB ci'\\''s key'\n" +
		"P_HOSTNAME=demo\nP_TAG=tag:dev\nP_TUN=userspace\nP_DOCKER=1\nP_TS_SSH=1\nP_LOCALES='en_US.UTF-8\npt_BR.UTF-8'\nTS_AUTHKEY=tskey-auth-k123-abc\n"
	if !bytes.HasPrefix(p, []byte(want)) {
		t.Fatalf("payload head:\n%s\nwant:\n%s", p[:len(want)], want)
	}
	if !bytes.Equal(p[len(want):], script) {
		t.Fatal("payload tail is not the embedded script byte for byte")
	}
	if bytes.Contains(p, []byte("export TS_AUTHKEY")) {
		t.Fatal("auth key exported to child processes")
	}
}

func TestPayloadRefusesIncompleteParams(t *testing.T) {
	for name, mut := range map[string]func(*Params){
		"no auth key": func(p *Params) { p.AuthKey = "" },
		"no keys":     func(p *Params) { p.Keys = nil },
		"bad tag":     func(p *Params) { p.Tag = "dev" },
		"bad tun":     func(p *Params) { p.Tun = "tap" },
	} {
		p := params()
		mut(&p)
		if _, err := Payload(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestScriptShape(t *testing.T) {
	s := string(script)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for _, l := range lines {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if l != "export LC_ALL=C.UTF-8 LANG=C.UTF-8" {
			t.Fatalf("first command must pin the locale, got %q", l)
		}
		break
	}
	if last := lines[len(lines)-1]; last != `main "$@" </dev/null` {
		t.Fatalf("last line = %q", last)
	}
	for _, must := range []string{
		`: "${TS_AUTHKEY:?}"`,
		"getent hosts deb.debian.org",
		"apt-get install -y -q curl ca-certificates sudo openssh-server",
		"https://get.docker.com",
		"https://tailscale.com/install.sh",
		`FLAGS="--tun=userspace-networking"`,
		`--auth-key "file:$KEYFILE"`,
		`--advertise-tags "$P_TAG"`,
		"umask 077",
		`rm -f "$KEYFILE"`,
		"PROVINCE_TS_IP=",
	} {
		if !strings.Contains(s, must) {
			t.Errorf("script lacks %q", must)
		}
	}
	// The key must never be an argument (visible in ps) or exported.
	for _, l := range lines {
		if strings.Contains(l, "TS_AUTHKEY") && !strings.Contains(l, `: "${TS_AUTHKEY:?}"`) && !strings.Contains(l, `printf '%s' "$TS_AUTHKEY" >"$KEYFILE"`) {
			if !strings.HasPrefix(strings.TrimSpace(l), "#") {
				t.Errorf("TS_AUTHKEY used outside the check and the key file write: %q", l)
			}
		}
	}
}

func TestParseResult(t *testing.T) {
	ip, err := ParseResult("noise\nPROVINCE_TS_IP=100.64.0.9\n")
	if err != nil || ip != "100.64.0.9" {
		t.Fatalf("ip = %q, %v", ip, err)
	}
	for _, bad := range []string{"", "apt output\n", "PROVINCE_TS_IP=\n", "PROVINCE_TS_IP=fd7a::1\n", "PROVINCE_TS_IP=x\n"} {
		if _, err := ParseResult(bad); err == nil {
			t.Errorf("ParseResult(%q) accepted", bad)
		}
	}
	// The value the guest printed is data, kept out of the message.
	_, err = ParseResult("PROVINCE_TS_IP=x\x1b[2K\n")
	if strings.Contains(err.Error(), "x\x1b") || errs.GuestOutput(err) != "x\x1b[2K" {
		t.Fatalf("err = %q, guest output = %q", err, errs.GuestOutput(err))
	}
}

func TestPayloadTailscaleSSHOff(t *testing.T) {
	p := params()
	p.TSSSH = false
	a, err := Assignments(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(a), "\nP_TS_SSH=0\n") {
		t.Fatalf("assignments = %q", a)
	}
}
