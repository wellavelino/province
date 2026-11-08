package run_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/bootstrap"
	"github.com/wellavelino/province/internal/run"
	"github.com/wellavelino/province/internal/sshx"
)

// Real-shell round trips live here because only run tests start processes.

func TestBootstrapAssignmentsRoundTripThroughSh(t *testing.T) {
	keys := []string{"ssh-ed25519 AAAAC3Nz+/= it's $HOME `id` key", "ssh-rsa AAAAB3 second"}
	authKey := `tskey-auth-k1'2"3$x`
	a, err := bootstrap.Assignments(bootstrap.Params{
		User: "dev", Keys: keys, Hostname: "demo", Tag: "tag:dev", Tun: "userspace", AuthKey: authKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	stdin := string(a) + `printf '%s\n--\n%s' "$P_KEYS" "$TS_AUTHKEY"` + "\n"
	var out bytes.Buffer
	if err := (run.Local{}).Run(context.Background(), run.Cmd{Name: "sh", Stdin: strings.NewReader(stdin), Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if want := strings.Join(keys, "\n") + "\n--\n" + authKey; out.String() != want {
		t.Fatalf("sh saw %q\nwant    %q", out.String(), want)
	}
}

func TestQuoteRoundTripsThroughSh(t *testing.T) {
	for _, s := range []string{"", "plain", "a b", "it's", "$HOME", "`id`", "a\nb", `\'"`, "*"} {
		var out bytes.Buffer
		err := (run.Local{}).Run(context.Background(), run.Cmd{Name: "sh", Args: []string{"-c", "printf '%s' " + sshx.Quote(s)}, Stdout: &out})
		if err != nil {
			t.Fatal(err)
		}
		if out.String() != s {
			t.Errorf("Quote(%q) came back as %q", s, out.String())
		}
	}
}
