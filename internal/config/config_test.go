package config

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "province.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFullSite(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	c, err := Load("testdata/site.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.StateDir != "/home/tester/infra/province" {
		t.Errorf("state_dir = %q", c.StateDir)
	}
	if c.SSHPublicKeys[0] != "/home/tester/.ssh/id_ed25519.pub" || c.SSHPublicKeys[1] != "/etc/keys/ci.pub" {
		t.Errorf("ssh_public_keys = %q", c.SSHPublicKeys)
	}
	if c.User != "dev" || c.Tailscale.Tag != "tag:dev" || c.Tailscale.Tun != TunDevice {
		t.Errorf("user/tailscale = %q %+v", c.User, c.Tailscale)
	}
	if c.Defaults.CPU != 4 || c.Defaults.RAM != 4096 || c.Defaults.Disk != 32 {
		t.Errorf("defaults = %+v", c.Defaults)
	}
	h, err := c.HostByName("pve2")
	if err != nil {
		t.Fatal(err)
	}
	if !h.Sudo || h.Insecure || h.SSH != "admin@pve2.example" || h.DumpDir != "/mnt/dump" || h.TokenEnv != "PROVINCE_TOKEN_PVE2" {
		t.Errorf("pve2 = %+v", h)
	}
	d, err := c.DefaultHost()
	if err != nil || d.Name != "pve1" || !d.Insecure || d.TokenEnv != "PROXMOX_VE_API_TOKEN" {
		t.Errorf("DefaultHost = %+v, %v", d, err)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if u.Username == "root" {
		t.Skip("the OS user fallback refuses root by design")
	}
	c, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.User != u.Username {
		t.Errorf("user = %q, want OS user %q", c.User, u.Username)
	}
	if c.Tailscale.Tun != TunUserspace {
		t.Errorf("tun = %q", c.Tailscale.Tun)
	}
	if !c.Tailscale.SSHEnabled() {
		t.Error("tailscale.ssh must default to true")
	}
	if c.VMIDStart != 100 {
		t.Errorf("vmid_start default = %d", c.VMIDStart)
	}
	if len(c.GuestLocales) != 1 || c.GuestLocales[0] != "en_US.UTF-8" {
		t.Errorf("guest_locales default = %q", c.GuestLocales)
	}
	if c.Hosts[0].DumpDir != "/var/lib/vz/dump" {
		t.Errorf("dump_dir = %q", c.Hosts[0].DumpDir)
	}
	if c.Defaults.CPU != 2 || c.Defaults.RAM != 2048 || c.Defaults.Disk != 16 {
		t.Errorf("defaults = %+v", c.Defaults)
	}
	// A single host is the default host even without defaults.host.
	if h, err := c.DefaultHost(); err != nil || h.Name != "pve1" {
		t.Errorf("DefaultHost = %+v, %v", h, err)
	}
}

func TestLoadRejects(t *testing.T) {
	base, err := os.ReadFile("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct{ body, want string }{
		"unknown key":    {string(base) + "colour: blue\n", "colour"},
		"typo in host":   {strings.Replace(string(base), "ssh: pve1", "sshh: pve1", 1), "sshh"},
		"no hosts":       {strings.Split(string(base), "hosts:")[0], "hosts"},
		"no state_dir":   {strings.Replace(string(base), "state_dir: /srv/province\n", "", 1), "state_dir"},
		"bad tun":        {strings.Replace(string(base), `tag: "tag:dev"`, `tag: "tag:dev", tun: tap`, 1), "tun"},
		"http endpoint":  {strings.Replace(string(base), "https://", "http://", 1), "endpoint"},
		"bad token_env":  {strings.Replace(string(base), "ssh: pve1 }", "ssh: pve1, token_env: \"PVE-1 TOKEN\" }", 1), "token_env"},
		"low vmid_start": {"vmid_start: 99\n" + string(base), "vmid_start"},
		"bad locale":     {"guest_locales: [\"en_US.UTF-8; rm -rf /\"]\n" + string(base), "guest_locales"},
		"latin1 locale":  {"guest_locales: [de_DE.ISO-8859-1]\n" + string(base), "guest_locales"},
		"token_env $":    {strings.Replace(string(base), "ssh: pve1 }", "ssh: pve1, token_env: \"$X\" }", 1), "token_env"},
		"empty file":     {"", "state_dir"},
		"bad user":       {"user: \"dev; rm\"\n" + string(base), "user"},
		"root user":      {"user: root\n" + string(base), "user"},
	}
	for name, c := range cases {
		_, err := Load(writeYAML(t, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want mention of %q", name, err, c.want)
		}
	}
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "PROVINCE_CONFIG") {
		t.Errorf("empty path: %v", err)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"demo", "proj-x1", "a", strings.Repeat("a", 63)} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Demo", "../x", "a_b", "a.b", "-a", "a-", "1abc", "a b", strings.Repeat("a", 64)} {
		if ValidName(bad) == nil {
			t.Errorf("ValidName(%q) accepted", bad)
		}
	}
}

func TestHostLookupErrors(t *testing.T) {
	c := &Config{Hosts: []Host{{Name: "pve1"}, {Name: "pve2"}}}
	if _, err := c.HostByName("pve9"); err == nil || !strings.Contains(err.Error(), "pve9") {
		t.Errorf("HostByName unknown: %v", err)
	}
	if _, err := c.DefaultHost(); err == nil || !strings.Contains(err.Error(), "--host") {
		t.Errorf("DefaultHost ambiguous: %v", err)
	}
}

func TestOSUserFallbackThatIsInvalidSaysToSetUser(t *testing.T) {
	defer func(f func() (*user.User, error)) { currentUser = f }(currentUser)
	currentUser = func() (*user.User, error) { return &user.User{Username: "Wellington"}, nil }
	_, err := Load("testdata/minimal.yaml")
	if err == nil || !strings.Contains(err.Error(), "OS user \"Wellington\"") || !strings.Contains(err.Error(), "set user: in the site config") {
		t.Fatalf("err = %v", err)
	}
	// An explicit user is not blamed on the OS.
	base, _ := os.ReadFile("testdata/minimal.yaml")
	_, err = Load(writeYAML(t, "user: Wellington\n"+string(base)))
	if err == nil || strings.Contains(err.Error(), "OS user") {
		t.Fatalf("explicit user: %v", err)
	}
}

func TestTailscaleSSHCanBeTurnedOff(t *testing.T) {
	base, _ := os.ReadFile("testdata/minimal.yaml")
	off := strings.Replace(string(base), `tag: "tag:dev"`, `tag: "tag:dev", ssh: false`, 1)
	c, err := Load(writeYAML(t, "user: dev\n"+off))
	if err != nil {
		t.Fatal(err)
	}
	if c.Tailscale.SSHEnabled() {
		t.Fatal("ssh: false ignored")
	}
	on := strings.Replace(string(base), `tag: "tag:dev"`, `tag: "tag:dev", ssh: true`, 1)
	if c, err = Load(writeYAML(t, "user: dev\n"+on)); err != nil || !c.Tailscale.SSHEnabled() {
		t.Fatalf("ssh: true: %v", err)
	}
}

func TestGuestLocalesExplicit(t *testing.T) {
	base, _ := os.ReadFile("testdata/minimal.yaml")
	c, err := Load(writeYAML(t, "user: dev\nguest_locales: [pt_BR.UTF-8, en_GB.UTF-8]\n"+string(base)))
	if err != nil || strings.Join(c.GuestLocales, ",") != "pt_BR.UTF-8,en_GB.UTF-8" {
		t.Fatalf("%q, %v", c.GuestLocales, err)
	}
	c, err = Load(writeYAML(t, "user: dev\nguest_locales: []\n"+string(base)))
	if err != nil || len(c.GuestLocales) != 0 {
		t.Fatalf("empty list: %q, %v", c.GuestLocales, err)
	}
}
