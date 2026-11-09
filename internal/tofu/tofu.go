// Package tofu drives the OpenTofu binary for one environment directory.
package tofu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/wellavelino/province/internal/run"
	"github.com/wellavelino/province/internal/secrets"
)

// Tofu is the subset of OpenTofu province uses.
type Tofu interface {
	Init(ctx context.Context, dir string) error
	Plan(ctx context.Context, dir string) (text string, changes bool, err error)
	Apply(ctx context.Context, dir string) error
	Destroy(ctx context.Context, dir string) error
	Output(ctx context.Context, dir string) (map[string]json.RawMessage, error)
	StateRm(ctx context.Context, dir, addr string) error
	Import(ctx context.Context, dir, addr, id string) error
}

// Exec runs the tofu binary through a Runner.
type Exec struct {
	runner run.Runner
	bin    string
	env    []string
	log    io.Writer
}

// New finds tofu on PATH and prepares its environment: baseEnv without any
// Tailscale secret, plus the Proxmox API token, which reaches tofu only this
// way (never argv, never a file); an empty token (Incus hosts, which
// authenticate through the operator's incus remote) sets none. log
// receives tofu's progress output.
func New(r run.Runner, lookPath func(string) (string, error), baseEnv []string, token string, log io.Writer) (*Exec, error) {
	bin, err := lookPath("tofu")
	if err != nil {
		return nil, errors.New("tofu not found on PATH: install OpenTofu (https://opentofu.org/docs/intro/install/) and retry")
	}
	if log == nil {
		log = io.Discard
	}
	drop := map[string]bool{"TF_IN_AUTOMATION": true}
	for _, n := range secrets.Names {
		drop[n] = true
	}
	var env []string
	for _, kv := range baseEnv {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	if token != "" {
		env = append(env, secrets.EnvAPIToken+"="+token)
	}
	env = append(env, "TF_IN_AUTOMATION=1")
	return &Exec{runner: r, bin: bin, env: env, log: log}, nil
}

func (e *Exec) cmd(dir string, stdout io.Writer, args ...string) run.Cmd {
	return run.Cmd{Name: e.bin, Args: args, Dir: dir, Env: e.env, Stdout: stdout, Stderr: e.log}
}

func (e *Exec) run(ctx context.Context, dir string, args ...string) error {
	if err := e.runner.Run(ctx, e.cmd(dir, e.log, args...)); err != nil {
		return fmt.Errorf("tofu %s in %s: %w", args[0], dir, err)
	}
	return nil
}

// Init implements Tofu.
func (e *Exec) Init(ctx context.Context, dir string) error {
	return e.run(ctx, dir, "init", "-input=false", "-no-color")
}

// Plan implements Tofu. Exit code 2 of -detailed-exitcode means changes.
func (e *Exec) Plan(ctx context.Context, dir string) (string, bool, error) {
	var out bytes.Buffer
	err := e.runner.Run(ctx, e.cmd(dir, &out, "plan", "-input=false", "-no-color", "-detailed-exitcode"))
	var ee *run.ExitError
	switch {
	case err == nil:
		return out.String(), false, nil
	case errors.As(err, &ee) && ee.Code == 2:
		return out.String(), true, nil
	}
	return out.String(), false, fmt.Errorf("tofu plan in %s: %w", dir, err)
}

// Apply implements Tofu.
func (e *Exec) Apply(ctx context.Context, dir string) error {
	return e.run(ctx, dir, "apply", "-input=false", "-no-color", "-auto-approve")
}

// Destroy implements Tofu.
func (e *Exec) Destroy(ctx context.Context, dir string) error {
	return e.run(ctx, dir, "destroy", "-input=false", "-no-color", "-auto-approve")
}

// Output implements Tofu: it returns each output's value.
func (e *Exec) Output(ctx context.Context, dir string) (map[string]json.RawMessage, error) {
	var out bytes.Buffer
	if err := e.runner.Run(ctx, e.cmd(dir, &out, "output", "-json", "-no-color")); err != nil {
		return nil, fmt.Errorf("tofu output in %s: %w", dir, err)
	}
	var raw map[string]struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("tofu output in %s: %w", dir, err)
	}
	vals := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		vals[k] = v.Value
	}
	return vals, nil
}

// StateRm implements Tofu: forget addr without touching the real resource.
func (e *Exec) StateRm(ctx context.Context, dir, addr string) error {
	return e.run(ctx, dir, "state", "rm", addr)
}

// Import implements Tofu: adopt an existing resource as addr.
func (e *Exec) Import(ctx context.Context, dir, addr, id string) error {
	return e.run(ctx, dir, "import", "-input=false", "-no-color", addr, id)
}
