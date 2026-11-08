package run_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/bootstrap"
	"github.com/wellavelino/province/internal/run"
)

// scriptFn runs one function of the real bootstrap script in bash, with
// the payload assignments for p, after stubs (shell functions shadowing
// commands such as tailscale or apt-get) and overrides of the script's
// globals. The temp dir is available to stubs as $T.
func scriptFn(t *testing.T, dir string, p bootstrap.Params, stubs, call string) {
	t.Helper()
	script := string(bootstrap.Script())
	const mainLine = "main \"$@\" </dev/null\n"
	if strings.Count(script, mainLine) != 1 || !strings.HasSuffix(script, mainLine) {
		t.Fatal("script no longer ends with the main line")
	}
	a, err := bootstrap.Assignments(p)
	if err != nil {
		t.Fatal(err)
	}
	in := string(a) + strings.TrimSuffix(script, mainLine) + "\nT=" + dir + "\n" + stubs + "\n" + call + "\n"
	var stderr bytes.Buffer
	if err := (run.Local{}).Run(context.Background(), run.Cmd{Name: "bash", Stdin: strings.NewReader(in), Stderr: &stderr}); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
}

func params(tsSSH bool) bootstrap.Params {
	return bootstrap.Params{
		User: "dev", Keys: []string{"ssh-ed25519 AAAA k"}, Hostname: "demo", Tag: "tag:dev",
		Tun: "userspace", TSSSH: tsSSH, AuthKey: "tskey-auth-x'y", Locales: []string{"en_US.UTF-8", "fr_FR.UTF-8"},
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, _ := os.ReadFile(path)
	if s := strings.TrimSpace(string(b)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

// joinTailnet runs join_tailnet with tailscale stubbed; joined says whether
// `tailscale ip` succeeds (the guest is already on the tailnet).
func joinTailnet(t *testing.T, tsSSH, joined bool) (calls []string, keyfile string) {
	t.Helper()
	dir := t.TempDir()
	keyfile = filepath.Join(dir, "authkey")
	ipRC := "1"
	if joined {
		ipRC = "0"
	}
	stubs := `KEYFILE=$T/authkey
tailscale() {
  case "$1" in
    ip) return ` + ipRC + ` ;;
    up) printf 'up %s key=%s\n' "${*:2}" "$(cat "$KEYFILE")" >>"$T/calls" ;;
    *) printf '%s\n' "$*" >>"$T/calls" ;;
  esac
}`
	scriptFn(t, dir, params(tsSSH), stubs, "join_tailnet")
	return readLines(t, filepath.Join(dir, "calls")), keyfile
}

func TestJoinTailnetSSHFlag(t *testing.T) {
	base := "up --auth-key file:%s --hostname demo --advertise-tags tag:dev --timeout 120s --accept-risk=lose-ssh"
	for _, c := range []struct {
		ssh  bool
		want string
	}{{true, base + " --ssh"}, {false, base}} {
		calls, keyfile := joinTailnet(t, c.ssh, false)
		want := strings.Replace(c.want, "%s", keyfile, 1) + " key=tskey-auth-x'y"
		if len(calls) != 1 || calls[0] != want {
			t.Errorf("ssh=%v: calls = %q\nwant %q", c.ssh, calls, want)
		}
		if _, err := os.Stat(keyfile); !os.IsNotExist(err) {
			t.Errorf("ssh=%v: auth key file left behind", c.ssh)
		}
	}
}

func TestJoinTailnetRerunAppliesSSHSetting(t *testing.T) {
	for ssh, want := range map[bool]string{true: "set --ssh=true --accept-risk=lose-ssh", false: "set --ssh=false --accept-risk=lose-ssh"} {
		calls, _ := joinTailnet(t, ssh, true)
		if len(calls) != 1 || calls[0] != want {
			t.Errorf("ssh=%v on a joined guest: calls = %q, want %q", ssh, calls, want)
		}
	}
}

// setupLocales runs setup_locales against a temp locale.gen, with apt-get,
// locale-gen and locale stubbed: locale-gen "generates" every uncommented
// line into $T/generated, which `locale -a` lists in its utf8 spelling.
func setupLocales(t *testing.T, dir string, p bootstrap.Params) []string {
	t.Helper()
	stubs := `LOCALE_GEN=$T/locale.gen
apt-get() { printf 'apt-get %s\n' "$*" >>"$T/calls"; }
locale-gen() {
  printf 'locale-gen\n' >>"$T/calls"
  grep -v '^#' "$LOCALE_GEN" | sed 's/\.UTF-8 UTF-8$/.utf8/' >"$T/generated"
}
locale() { cat "$T/generated" 2>/dev/null; }`
	scriptFn(t, dir, p, stubs, "setup_locales")
	calls := readLines(t, filepath.Join(dir, "calls"))
	os.Remove(filepath.Join(dir, "calls"))
	return calls
}

func TestSetupLocalesIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "locale.gen")
	os.WriteFile(gen, []byte("# en_US.UTF-8 UTF-8\n# de_DE.UTF-8 UTF-8\n"), 0o644)

	calls := setupLocales(t, dir, params(true))
	if strings.Join(calls, "|") != "apt-get install -y -q locales|locale-gen" {
		t.Fatalf("first run calls = %q", calls)
	}
	want := "en_US.UTF-8 UTF-8\n# de_DE.UTF-8 UTF-8\nfr_FR.UTF-8 UTF-8\n"
	if b, _ := os.ReadFile(gen); string(b) != want {
		t.Fatalf("locale.gen = %q\nwant %q", b, want)
	}

	calls = setupLocales(t, dir, params(true))
	if strings.Join(calls, "|") != "apt-get install -y -q locales" {
		t.Fatalf("re-run regenerated: %q", calls)
	}
	if b, _ := os.ReadFile(gen); string(b) != want {
		t.Fatalf("re-run changed locale.gen: %q", b)
	}
}

func TestSetupLocalesNoneConfigured(t *testing.T) {
	p := params(true)
	p.Locales = nil
	if calls := setupLocales(t, t.TempDir(), p); len(calls) != 0 {
		t.Fatalf("calls = %q", calls)
	}
}
