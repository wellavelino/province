// Package incus talks to an Incus server through the local incus CLI and a
// remote the operator set up once with `incus remote add`. The remote holds
// the client certificate, so no secret passes through province.
package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/wellavelino/province/internal/run"
)

// EnvKey is the instance config key province sets to the env's name.
const EnvKey = "user.province.env"

// Instance is one instance as the server reports it.
type Instance struct {
	Name   string
	Type   string // container | virtual-machine
	Status string // lowercase: running, stopped, frozen, error, ...
	Env    string // config user.province.env: the env that created it, if any
}

// Client is what province asks of one Incus host.
type Client interface {
	// Exec runs argv in instance name as root (incus exec).
	Exec(ctx context.Context, name string, stdin io.Reader, stdout io.Writer, argv ...string) error
	// Instances lists the instances of the host's project.
	Instances(ctx context.Context) ([]Instance, error)
	// HasStoragePool reports whether the server has pool.
	HasStoragePool(ctx context.Context, pool string) (bool, error)
	// HasNetwork reports whether the project sees network.
	HasNetwork(ctx context.Context, network string) (bool, error)
}

// CLI implements Client with the incus binary.
type CLI struct {
	Runner  run.Runner
	Remote  string
	Project string // empty: the remote's default project
	Log     io.Writer
}

func (c *CLI) cmd(stdin io.Reader, stdout io.Writer, args ...string) run.Cmd {
	var global []string
	if c.Project != "" {
		global = append(global, "--project", c.Project)
	}
	return run.Cmd{Name: "incus", Args: append(global, args...), Stdin: stdin, Stdout: stdout, Stderr: c.Log}
}

// Exec implements Client. -T: stdin is a script, not a terminal.
func (c *CLI) Exec(ctx context.Context, name string, stdin io.Reader, stdout io.Writer, argv ...string) error {
	args := append([]string{"exec", c.Remote + ":" + name, "-T", "--"}, argv...)
	if err := c.Runner.Run(ctx, c.cmd(stdin, stdout, args...)); err != nil {
		return fmt.Errorf("incus exec in %s on remote %s: %w", name, c.Remote, err)
	}
	return nil
}

// query GETs an API path on the remote and decodes the JSON answer.
func (c *CLI) query(ctx context.Context, path string, v any) error {
	q := url.Values{}
	q.Set("recursion", "1")
	if c.Project != "" {
		q.Set("project", c.Project)
	}
	var out bytes.Buffer
	cmd := run.Cmd{Name: "incus", Args: []string{"query", c.Remote + ":" + path + "?" + q.Encode()}, Stdout: &out, Stderr: c.Log}
	if err := c.Runner.Run(ctx, cmd); err != nil {
		return fmt.Errorf("incus query %s on remote %s: %w", path, c.Remote, err)
	}
	if err := json.Unmarshal(out.Bytes(), v); err != nil {
		return fmt.Errorf("incus query %s on remote %s: bad JSON: %w", path, c.Remote, err)
	}
	return nil
}

// Instances implements Client.
func (c *CLI) Instances(ctx context.Context) ([]Instance, error) {
	var raw []struct {
		Name   string            `json:"name"`
		Type   string            `json:"type"`
		Status string            `json:"status"`
		Config map[string]string `json:"config"`
	}
	if err := c.query(ctx, "/1.0/instances", &raw); err != nil {
		return nil, err
	}
	out := make([]Instance, 0, len(raw))
	for _, r := range raw {
		out = append(out, Instance{Name: r.Name, Type: r.Type, Status: strings.ToLower(r.Status), Env: r.Config[EnvKey]})
	}
	return out, nil
}

func (c *CLI) hasNamed(ctx context.Context, path, name string) (bool, error) {
	var raw []struct {
		Name string `json:"name"`
	}
	if err := c.query(ctx, path, &raw); err != nil {
		return false, err
	}
	for _, r := range raw {
		if r.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// HasStoragePool implements Client.
func (c *CLI) HasStoragePool(ctx context.Context, pool string) (bool, error) {
	return c.hasNamed(ctx, "/1.0/storage-pools", pool)
}

// HasNetwork implements Client.
func (c *CLI) HasNetwork(ctx context.Context, network string) (bool, error) {
	return c.hasNamed(ctx, "/1.0/networks", network)
}
