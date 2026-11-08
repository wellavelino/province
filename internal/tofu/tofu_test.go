package tofu

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/run/runtest"
)

const token = "province@pve!cli=0b8e7d6c-1111-2222-3333-444455556666"

func found(string) (string, error) { return "/usr/bin/tofu", nil }

func newExec(t *testing.T, f *runtest.Fake) *Exec {
	t.Helper()
	base := []string{"PATH=/usr/bin", "HOME=/home/dev", "TS_AUTHKEY=tskey-auth-x", "TS_API_KEY=tskey-api-y",
		"PROXMOX_VE_API_TOKEN=stale", "TF_IN_AUTOMATION=0"}
	e, err := New(f, found, base, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestArgvAndEnv(t *testing.T) {
	f := &runtest.Fake{T: t}
	f.On([]string{"/usr/bin/tofu"}, runtest.Response{Stdout: `{}`})
	e := newExec(t, f)
	ctx := context.Background()
	_ = e.Init(ctx, "/state/demo")
	_, _, _ = e.Plan(ctx, "/state/demo")
	_ = e.Apply(ctx, "/state/demo")
	_ = e.Destroy(ctx, "/state/demo")
	_, _ = e.Output(ctx, "/state/demo")
	_ = e.StateRm(ctx, "/state/demo", "proxmox_virtual_environment_container.env")
	_ = e.Import(ctx, "/state/demo", "proxmox_virtual_environment_container.env", "pve2/900")

	want := []string{
		"/usr/bin/tofu init -input=false -no-color",
		"/usr/bin/tofu plan -input=false -no-color -detailed-exitcode",
		"/usr/bin/tofu apply -input=false -no-color -auto-approve",
		"/usr/bin/tofu destroy -input=false -no-color -auto-approve",
		"/usr/bin/tofu output -json -no-color",
		"/usr/bin/tofu state rm proxmox_virtual_environment_container.env",
		"/usr/bin/tofu import -input=false -no-color proxmox_virtual_environment_container.env pve2/900",
	}
	if got := f.Commands(); !slices.Equal(got, want) {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range f.Calls() {
		if c.Dir != "/state/demo" {
			t.Errorf("%s: dir = %q", c, c.Dir)
		}
		if strings.Contains(c.String(), token) || strings.Contains(c.String(), "cli=") {
			t.Errorf("token in argv: %s", c)
		}
		if !slices.Contains(c.Env, "PROXMOX_VE_API_TOKEN="+token) || !slices.Contains(c.Env, "TF_IN_AUTOMATION=1") {
			t.Errorf("%s: env lacks token or TF_IN_AUTOMATION: %q", c, c.Env)
		}
		for _, kv := range c.Env {
			if strings.HasPrefix(kv, "TS_") || kv == "PROXMOX_VE_API_TOKEN=stale" || kv == "TF_IN_AUTOMATION=0" {
				t.Errorf("%s: env leaks %q", c, kv)
			}
		}
		if !slices.Contains(c.Env, "PATH=/usr/bin") {
			t.Errorf("%s: base env dropped", c)
		}
	}
}

func TestPlanDetailedExitCode(t *testing.T) {
	for code, want := range map[int]bool{0: false, 2: true} {
		f := &runtest.Fake{T: t}
		f.On([]string{"/usr/bin/tofu", "plan"}, runtest.Response{Stdout: "Plan: 1 to add", Code: code})
		text, changes, err := newExec(t, f).Plan(context.Background(), "/d")
		if err != nil || changes != want || text != "Plan: 1 to add" {
			t.Errorf("exit %d: text=%q changes=%v err=%v", code, text, changes, err)
		}
	}
	f := &runtest.Fake{T: t}
	f.On([]string{"/usr/bin/tofu", "plan"}, runtest.Response{Code: 1, Stderr: "Error: 401"})
	if _, _, err := newExec(t, f).Plan(context.Background(), "/d"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("exit 1: err = %v", err)
	}
}

func TestOutputParsesValues(t *testing.T) {
	f := &runtest.Fake{T: t}
	f.On([]string{"/usr/bin/tofu", "output"}, runtest.Response{Stdout: `{"vmid":{"sensitive":false,"type":"number","value":105}}`})
	out, err := newExec(t, f).Output(context.Background(), "/d")
	if err != nil || string(out["vmid"]) != "105" {
		t.Fatalf("out = %v, err = %v", out, err)
	}
}

func TestApplyFailureIsAnError(t *testing.T) {
	f := &runtest.Fake{T: t}
	f.On([]string{"/usr/bin/tofu", "apply"}, runtest.Response{Code: 1, Stderr: "boom"})
	if err := newExec(t, f).Apply(context.Background(), "/d"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingTofuNamesInstallStep(t *testing.T) {
	_, err := New(&runtest.Fake{T: t}, func(string) (string, error) { return "", exec.ErrNotFound }, nil, token, nil)
	if err == nil || !strings.Contains(err.Error(), "install OpenTofu") {
		t.Fatalf("err = %v", err)
	}
}
