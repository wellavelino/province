package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type outcome struct {
	code           int
	stdout, stderr string
}

func runCLI(t *testing.T, d Deps, args ...string) outcome {
	t.Helper()
	var out, errw bytes.Buffer
	code := Run(context.Background(), d, args, &out, &errw)
	return outcome{code, out.String(), errw.String()}
}

func noEnv(string) string { return "" }

// decodeOne asserts stdout holds exactly one JSON document and decodes it.
func decodeOne(t *testing.T, stdout string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one JSON document:\n%s", stdout)
	}
}

func TestKindVMRefusedBeforeAnyIO(t *testing.T) {
	// No config, no secrets: the refusal must not need either.
	o := runCLI(t, Deps{Getenv: noEnv}, "--json", "env", "create", "x", "--kind", "vm")
	if o.code != 2 {
		t.Fatalf("exit = %d, want 2; stdout=%s stderr=%s", o.code, o.stdout, o.stderr)
	}
	var doc map[string]string
	decodeOne(t, o.stdout, &doc)
	if doc["code"] != "refused" || !strings.Contains(doc["error"], "vm") {
		t.Fatalf("error doc = %v", doc)
	}
	if len(doc) != 2 {
		t.Fatalf("error doc has extra keys: %v", doc)
	}
}

func TestUsageErrorsAreRefusedJSON(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "env", "create"},                 // missing name
		{"--json", "env", "create", "x", "--bogus"}, // unknown flag
		{"env", "list", "--json", "--nope"},         // --json after the subcommand
	} {
		o := runCLI(t, Deps{Getenv: noEnv}, args...)
		if o.code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, o.code)
			continue
		}
		var doc map[string]string
		decodeOne(t, o.stdout, &doc)
		if doc["code"] != "refused" {
			t.Errorf("%v: code = %q", args, doc["code"])
		}
	}
}

func TestHumanErrorGoesToStderr(t *testing.T) {
	o := runCLI(t, Deps{Getenv: noEnv}, "env", "create", "x", "--kind", "vm")
	if o.code != 2 || o.stdout != "" || !strings.Contains(o.stderr, "province: ") {
		t.Fatalf("got %+v", o)
	}
}
