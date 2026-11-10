package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Strings province stored (some came from a guest) reach a terminal only
// with their control characters escaped; --json output is unchanged JSON.
func TestHumanOutputEscapesControlCharacters(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(w.stateDir, "demo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := `{"name":"demo","kind":"lxc","host":"pve1","node":"pve1","status":"planned","recipe":"x\u001b[2Ky\u0007\r","created_at":"2026-11-08T10:15:00Z","updated_at":"2026-11-08T10:15:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "province.json"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"env", "show", "demo"}, {"env", "list"}} {
		o := w.run(args...)
		if o.code != 0 || strings.ContainsAny(o.stdout, "\x1b\x07\r") || !strings.Contains(o.stdout, `x\x1b[2Ky\x07\x0d`) {
			t.Fatalf("%v: %d %q %s", args, o.code, o.stdout, o.stderr)
		}
	}
	o := w.run("--json", "env", "show", "demo")
	var v map[string]any
	decodeOne(t, o.stdout, &v)
	if v["recipe"] != "x\x1b[2Ky\x07\r" {
		t.Fatalf("json recipe = %q", v["recipe"])
	}
}
