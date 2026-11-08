package secrets

import (
	"bytes"
	"strings"
	"testing"
)

const token = "province@pve!cli=0b8e7d6c-1111-2222-3333-444455556666"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFromEnvTailscale(t *testing.T) {
	s := FromEnv(env(map[string]string{EnvAuthKey: "tskey-auth-x"}))
	if s.AuthKey != "tskey-auth-x" || s.APIKey != "" || s.Tailnet != "-" {
		t.Fatalf("secrets = %+v", s)
	}
}

func TestTokenNamesVarAndHost(t *testing.T) {
	_, err := Token(env(nil), "PROVINCE_TOKEN_PVE2", "pve2")
	if err == nil || !strings.Contains(err.Error(), "PROVINCE_TOKEN_PVE2") || !strings.Contains(err.Error(), "pve2") {
		t.Fatalf("missing token: %v", err)
	}
	for _, bad := range []string{"abc", "user@pve=secret", "user!id=secret", "user@pve!id", "user@pve!id="} {
		_, err := Token(env(map[string]string{"T": bad}), "T", "pve1")
		if err == nil {
			t.Errorf("accepted malformed token %q", bad)
			continue
		}
		if strings.Contains(err.Error(), bad) {
			t.Errorf("error echoes the value: %v", err)
		}
	}
	if v, err := Token(env(map[string]string{"T": " " + token + "\n"}), "T", "pve1"); err != nil || v != token {
		t.Fatalf("token = %q, %v", v, err)
	}
}

func TestAddTokenScrubsTokenAndBareSecret(t *testing.T) {
	r := NewRedactor()
	r.AddToken("root@pam!b=1111-2222")
	if got := r.Scrub("x root@pam!b=1111-2222 y 1111-2222"); got != "x [redacted] y [redacted]" {
		t.Fatalf("Scrub = %q", got)
	}
}

func TestScrub(t *testing.T) {
	r := FromEnvValues(env(map[string]string{EnvAPIToken: token, EnvAuthKey: "tskey-auth-abc"}))
	in := "header PVEAPIToken=" + token + " key=tskey-auth-abc bare=" + TokenSecret(token)
	got := r.Scrub(in)
	want := "header PVEAPIToken=[redacted] key=[redacted] bare=[redacted]"
	if got != want {
		t.Fatalf("Scrub = %q\nwant    %q", got, want)
	}
}

func TestScrubIgnoresUnsetSecrets(t *testing.T) {
	// TS_API_KEY is optional; an empty value must not match everywhere.
	r := FromEnvValues(env(map[string]string{EnvAPIToken: token}))
	if got := r.Scrub("plain text"); got != "plain text" {
		t.Fatalf("Scrub = %q", got)
	}
	if got := NewRedactor("", "  ").Scrub("abc"); got != "abc" {
		t.Fatalf("Scrub = %q", got)
	}
}

func TestWriterScrubsValuesSplitAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	w := NewRedactor("tskey-auth-abc").Writer(&out)
	for _, chunk := range []string{"line one tskey-", "auth-abc end\nsecond tskey-au", "th-abc"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(out.String(), "second") {
		t.Fatalf("partial line written before Flush: %q", out.String())
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if want := "line one [redacted] end\nsecond [redacted]"; out.String() != want {
		t.Fatalf("out = %q, want %q", out.String(), want)
	}
}
