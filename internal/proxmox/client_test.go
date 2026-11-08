package proxmox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "province@pve!cli=0b8e7d6c-1111-2222-3333-444455556666"

// fakePVE serves canned JSON by path and records the auth headers it saw.
func fakePVE(t *testing.T, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var auth []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		body, ok := routes[key]
		if !ok {
			http.Error(w, "no route "+key, http.StatusNotImplemented)
			return
		}
		if code, rest, ok := strings.Cut(body, "|"); ok && len(code) == 3 {
			var n int
			fmt.Sscan(code, &n)
			w.WriteHeader(n)
			body = rest
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &auth
}

func TestListGuestsMergesTypesAndSendsToken(t *testing.T) {
	srv, auth := fakePVE(t, map[string]string{
		"/api2/json/nodes/pve1/lxc":  `{"data":[{"vmid":105,"name":"demo","status":"running"},{"vmid":"106","name":"old","status":"stopped"}]}`,
		"/api2/json/nodes/pve1/qemu": `{"data":[{"vmid":200,"name":"win","status":"stopped"}]}`,
	})
	gs, err := New(srv.URL, true, token).ListGuests(context.Background(), "pve1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Guest{{105, "demo", "running", "lxc"}, {106, "old", "stopped", "lxc"}, {200, "win", "stopped", "qemu"}}
	if fmt.Sprint(gs) != fmt.Sprint(want) {
		t.Fatalf("guests = %v", gs)
	}
	for _, a := range *auth {
		if a != "PVEAPIToken="+token {
			t.Fatalf("auth header = %q", a)
		}
	}
}

func TestInsecureIsPerHost(t *testing.T) {
	srv, _ := fakePVE(t, map[string]string{"/api2/json/nodes/pve1/storage": `{"data":[]}`})
	if _, err := New(srv.URL, true, token).Storages(context.Background(), "pve1"); err != nil {
		t.Fatalf("insecure host: %v", err)
	}
	// A second host without insecure must verify the self-signed cert and fail.
	_, err := New(srv.URL, false, token).Storages(context.Background(), "pve1")
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("verifying host: err = %v", err)
	}
}

func TestAuthErrorsPointAtTheToken(t *testing.T) {
	srv, _ := fakePVE(t, map[string]string{
		"/api2/json/nodes/pve1/lxc":     `401|{"data":null}`,
		"/api2/json/nodes/pve1/storage": `403|{"data":null,"message":"Permission check failed (/storage, Datastore.Audit)"}`,
	})
	c := New(srv.URL, true, token)
	_, err := c.ListGuests(context.Background(), "pve1")
	if err == nil || !strings.Contains(err.Error(), "PROXMOX_VE_API_TOKEN") || !strings.Contains(err.Error(), "role") {
		t.Errorf("401: %v", err)
	}
	if strings.Contains(fmt.Sprint(err), token) {
		t.Errorf("401 error leaks the token")
	}
	_, err = c.Storages(context.Background(), "pve1")
	if err == nil || !strings.Contains(err.Error(), "Datastore.Audit") {
		t.Errorf("403: %v", err)
	}
}

func TestBridgesEmptyHintsSDNAudit(t *testing.T) {
	srv, _ := fakePVE(t, map[string]string{
		"/api2/json/nodes/pve1/network?type=any_bridge": `{"data":[]}`,
		"/api2/json/nodes/pve2/network?type=any_bridge": `{"data":[{"iface":"vmbr0","type":"bridge"},{"iface":"vmbr1","type":"OVSBridge"}]}`,
	})
	c := New(srv.URL, true, token)
	_, err := c.Bridges(context.Background(), "pve1")
	if !errors.Is(err, ErrNoBridges) || !strings.Contains(err.Error(), "SDN.Audit") {
		t.Fatalf("err = %v", err)
	}
	bs, err := c.Bridges(context.Background(), "pve2")
	if err != nil || fmt.Sprint(bs) != "[vmbr0 vmbr1]" {
		t.Fatalf("bridges = %v, %v", bs, err)
	}
}

func TestStoragesStatusConfigTemplate(t *testing.T) {
	srv, _ := fakePVE(t, map[string]string{
		"/api2/json/nodes/pve1/storage":                              `{"data":[{"storage":"local","type":"dir","content":"iso,vztmpl,backup","avail":1000,"active":1},{"storage":"local-lvm","content":"rootdir,images","avail":2000.0,"active":0}]}`,
		"/api2/json/nodes/pve1/lxc/105/status/current":               `{"data":{"status":"running","vmid":105,"disk":7838315315,"maxdisk":103079215104}}`,
		"/api2/json/nodes/pve1/lxc/105/config":                       `{"data":{"rootfs":"local-lvm:vm-105-disk-0,size=8G","cores":2,"onboot":1,"lxc":[["lxc.cgroup2.devices.allow","c 10:200 rwm"]]}}`,
		"/api2/json/nodes/pve1/storage/local/content?content=vztmpl": `{"data":[{"volid":"local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst"}]}`,
	})
	c := New(srv.URL, true, token)
	ctx := context.Background()
	ss, err := c.Storages(ctx, "pve1")
	if err != nil || len(ss) != 2 || !ss[0].Active || ss[1].Active || ss[1].Avail != 2000 || ss[0].Type != "dir" || !ss[0].Has("vztmpl") || ss[0].Has("rootdir") {
		t.Fatalf("storages = %+v, %v", ss, err)
	}
	if st, err := c.GuestStatus(ctx, "pve1", "lxc", 105); err != nil || st != "running" {
		t.Fatalf("status = %q, %v", st, err)
	}
	if used, err := c.GuestDiskUsed(ctx, "pve1", "lxc", 105); err != nil || used != 7838315315 {
		t.Fatalf("disk used = %d, %v", used, err)
	}
	conf, err := c.GuestConfig(ctx, "pve1", "lxc", 105)
	if err != nil || conf["cores"] != "2" || conf["rootfs"] != "local-lvm:vm-105-disk-0,size=8G" || conf["lxc.cgroup2.devices.allow"] != "c 10:200 rwm" {
		t.Fatalf("config = %v, %v", conf, err)
	}
	if ok, err := c.TemplateExists(ctx, "pve1", "local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst"); err != nil || !ok {
		t.Fatalf("template exists = %v, %v", ok, err)
	}
	if ok, err := c.TemplateExists(ctx, "pve1", "local:vztmpl/ubuntu.tar.zst"); err != nil || ok {
		t.Fatalf("missing template = %v, %v", ok, err)
	}
	if _, err := c.GuestStatus(ctx, "pve1", "../x", 1); err == nil {
		t.Fatal("bad type accepted")
	}
}
