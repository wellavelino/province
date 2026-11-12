package incus

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/run/runtest"
)

func TestExecArgvAndStdin(t *testing.T) {
	f := &runtest.Fake{T: t}
	f.On([]string{"incus"}, runtest.Response{Stdout: "ok"})
	c := &CLI{Runner: f, Remote: "box", Project: "dev"}
	var out strings.Builder
	if err := c.Exec(context.Background(), "api", strings.NewReader("echo hi"), &out, "bash", "-s"); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	want := []string{"incus", "--project", "dev", "exec", "box:api", "-T", "--", "bash", "-s"}
	if len(calls) != 1 || !slices.Equal(calls[0].Argv, want) || calls[0].Stdin != "echo hi" || out.String() != "ok" {
		t.Fatalf("calls = %+v, out = %q", calls, out.String())
	}
}

func TestExecFailureNamesInstanceAndRemote(t *testing.T) {
	f := &runtest.Fake{T: t}
	f.On([]string{"incus"}, runtest.Response{Code: 1, Stderr: "Error: Instance not running"})
	err := (&CLI{Runner: f, Remote: "box"}).Exec(context.Background(), "api", nil, nil, "true")
	if err == nil || !strings.Contains(err.Error(), "api on remote box") || strings.Contains(err.Error(), "not running") {
		t.Fatalf("err = %v", err)
	}
	// The guest's stderr is data, apart from province's message.
	if got := errs.GuestOutput(err); got != "Error: Instance not running" {
		t.Fatalf("guest output = %q", got)
	}
	if got := f.Calls()[0].Argv; slices.Contains(got, "--project") {
		t.Errorf("no project configured, argv = %q", got)
	}
}

func TestInstancesLowercasesStatus(t *testing.T) {
	f := &runtest.Fake{T: t}
	f.On([]string{"incus", "query", "box:/1.0/instances?project=dev&recursion=1"},
		runtest.Response{Stdout: `[{"name":"api","type":"container","status":"Running","config":{"user.province.env":"api"}},{"name":"db","type":"container","status":"Stopped"}]`})
	got, err := (&CLI{Runner: f, Remote: "box", Project: "dev"}).Instances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Instance{Name: "api", Type: "container", Status: "running", Env: "api"}) || got[1].Env != "" || got[1].Status != "stopped" {
		t.Fatalf("got %+v", got)
	}
}

func TestHasStoragePoolAndNetwork(t *testing.T) {
	f := &runtest.Fake{T: t}
	f.On([]string{"incus", "query", "local:/1.0/storage-pools?recursion=1"}, runtest.Response{Stdout: `[{"name":"default"}]`})
	f.On([]string{"incus", "query", "local:/1.0/networks?recursion=1"}, runtest.Response{Stdout: `[{"name":"incusbr0"}]`})
	c := &CLI{Runner: f, Remote: "local"}
	ctx := context.Background()
	if ok, err := c.HasStoragePool(ctx, "default"); !ok || err != nil {
		t.Errorf("default pool: %v %v", ok, err)
	}
	if ok, err := c.HasStoragePool(ctx, "fast"); ok || err != nil {
		t.Errorf("fast pool: %v %v", ok, err)
	}
	if ok, err := c.HasNetwork(ctx, "incusbr0"); !ok || err != nil {
		t.Errorf("network: %v %v", ok, err)
	}
}

func TestQueryBadJSON(t *testing.T) {
	f := &runtest.Fake{T: t}
	f.On([]string{"incus", "query"}, runtest.Response{Stdout: "not json"})
	if _, err := (&CLI{Runner: f, Remote: "box"}).Instances(context.Background()); err == nil || !strings.Contains(err.Error(), "bad JSON") {
		t.Fatalf("err = %v", err)
	}
}

func TestServerAndNetwork(t *testing.T) {
	f := &runtest.Fake{T: t}
	f.On([]string{"incus", "query", "box:/1.0?project=dev&recursion=1"},
		runtest.Response{Stdout: `{"api_extensions":["network_acl","network_bridge_acl_devices"],"environment":{"firewall":"nftables"}}`})
	f.On([]string{"incus", "query", "box:/1.0/networks/incusbr0?project=dev&recursion=1"},
		runtest.Response{Stdout: `{"name":"incusbr0","type":"bridge","managed":true,"config":{"ipv4.address":"10.148.12.1/24","ipv6.address":"none"}}`})
	c := &CLI{Runner: f, Remote: "box", Project: "dev"}
	ctx := context.Background()
	s, err := c.Server(ctx)
	if err != nil || s.Firewall != "nftables" || !slices.Contains(s.Extensions, ExtBridgeACLDevices) {
		t.Fatalf("server = %+v, %v", s, err)
	}
	n, err := c.Network(ctx, "incusbr0")
	if err != nil || n.Type != "bridge" || !n.Managed || n.Config["ipv4.address"] != "10.148.12.1/24" {
		t.Fatalf("network = %+v, %v", n, err)
	}
}
