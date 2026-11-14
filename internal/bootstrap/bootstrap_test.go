package bootstrap

import (
	"bytes"
	"slices"
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
		`: "${TS_AUTHKEY?}"`,
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
	allowed := []string{
		`: "${TS_AUTHKEY?}"`,
		`if [ -z "$TS_AUTHKEY" ]; then`,
		`(umask 077 && printf '%s' "$TS_AUTHKEY" >"$KEYFILE")`,
		"unset TS_AUTHKEY",
	}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if !strings.Contains(l, "TS_AUTHKEY") || strings.HasPrefix(l, "#") {
			continue
		}
		if !slices.ContainsFunc(allowed, func(a string) bool { return strings.HasPrefix(l, a) }) {
			t.Errorf("TS_AUTHKEY used outside the check, the key file write and the unset: %q", l)
		}
	}
}

// fnBody returns the lines of the script's shell function name.
func fnBody(t *testing.T, name string) []string {
	t.Helper()
	s := string(script)
	_, rest, ok := strings.Cut(s, "\n"+name+"() {\n")
	if !ok {
		t.Fatalf("script has no function %s", name)
	}
	body, _, _ := strings.Cut(rest, "\n}\n")
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// TestScriptJoinsBeforeDockerAndDropsKey pins the order: the node joins
// the tailnet, and the key is unset, before Docker's installer runs.
func TestScriptJoinsBeforeDockerAndDropsKey(t *testing.T) {
	main := fnBody(t, "main")
	at := func(step string) int {
		i := slices.Index(main, step)
		if i < 0 {
			t.Fatalf("main does not run %s: %q", step, main)
		}
		return i
	}
	if !(at("install_tailscale") < at("join_tailnet") && at("join_tailnet") < at("install_docker") && at("install_docker") < at("report")) {
		t.Fatalf("main order = %q", main)
	}
	join := fnBody(t, "join_tailnet")
	up := slices.IndexFunc(join, func(l string) bool { return strings.HasPrefix(l, "tailscale up ") })
	if up < 0 || up+2 >= len(join) || join[up+1] != `rm -f "$KEYFILE"` || join[up+2] != "unset TS_AUTHKEY" {
		t.Fatalf("join_tailnet does not drop the key right after tailscale up: %q", join)
	}
	// The already-joined path returns early; it must drop the key too.
	set := slices.IndexFunc(join, func(l string) bool { return strings.HasPrefix(l, "tailscale set ") })
	if set < 0 || join[set+1] != "unset TS_AUTHKEY" || join[set+2] != "return 0" {
		t.Fatalf("joined path does not drop the key: %q", join)
	}
}

func TestPayloadJoinedSendsNoKey(t *testing.T) {
	p := params()
	p.Joined = true
	a, err := Assignments(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(a), p.AuthKey) || !strings.HasSuffix(string(a), "\nTS_AUTHKEY=''\n") {
		t.Fatalf("assignments for a joined guest = %q", a)
	}
	p.AuthKey = ""
	if _, err := Payload(p); err != nil {
		t.Fatalf("joined guest without a key refused: %v", err)
	}
}

func TestJoined(t *testing.T) {
	for out, want := range map[string]bool{
		"100.64.0.7\n":                           true,
		" 100.64.0.7 \n100.64.0.8\n":             true,
		"":                                       false,
		"fd7a:115c:a1e0::1\n":                    false,
		"apt noise\nPROVINCE_TS_IP=100.64.0.7\n": false,
		"no current Tailscale IPs; state: NeedsLogin\n": false,
	} {
		if got := Joined(out); got != want {
			t.Errorf("Joined(%q) = %v, want %v", out, got, want)
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
