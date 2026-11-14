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

// Instance types as the server reports them.
const (
	TypeContainer = "container"
	TypeVM        = "virtual-machine"
)

// Instance is one instance as the server reports it.
type Instance struct {
	Name   string
	Type   string // TypeContainer | TypeVM
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
	// Server reports the server's API extensions and firewall driver.
	Server(ctx context.Context) (Server, error)
	// Network returns the network called name.
	Network(ctx context.Context, name string) (Network, error)
}

// Server is what an Incus server reports about itself.
type Server struct {
	Extensions []string
	Firewall   string // the firewall driver: nftables or xtables
}

// Network is one network as the server reports it.
type Network struct {
	Name    string
	Type    string // bridge, ovn, macvlan, physical, ...
	Managed bool
	Config  map[string]string
}

// ExtBridgeACLDevices is the API extension for network ACLs on the NICs of
// a bridge network (Incus 6.0.4 and 6.4 onward).
const ExtBridgeACLDevices = "network_bridge_acl_devices"

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
	if err := run.MarkGuest(c.Runner.Run(ctx, c.cmd(stdin, stdout, args...))); err != nil {
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

// Server implements Client.
func (c *CLI) Server(ctx context.Context) (Server, error) {
	var raw struct {
		Extensions  []string `json:"api_extensions"`
		Environment struct {
			Firewall string `json:"firewall"`
		} `json:"environment"`
	}
	if err := c.query(ctx, "/1.0", &raw); err != nil {
		return Server{}, err
	}
	return Server{Extensions: raw.Extensions, Firewall: raw.Environment.Firewall}, nil
}

// Network implements Client.
func (c *CLI) Network(ctx context.Context, name string) (Network, error) {
	var raw struct {
		Name    string            `json:"name"`
		Type    string            `json:"type"`
		Managed bool              `json:"managed"`
		Config  map[string]string `json:"config"`
	}
	if err := c.query(ctx, "/1.0/networks/"+url.PathEscape(name), &raw); err != nil {
		return Network{}, err
	}
	return Network{Name: raw.Name, Type: raw.Type, Managed: raw.Managed, Config: raw.Config}, nil
}
