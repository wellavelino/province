package tailscale

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const devices = `{"devices":[
 {"id":"111","hostname":"demo","name":"demo.tail-abc.ts.net","addresses":["100.64.0.7"],"tags":["tag:dev"]},
 {"id":"222","hostname":"laptop","name":"laptop.tail-abc.ts.net"},
 {"id":"333","hostname":"web","name":"web.tail-abc.ts.net"},
 {"id":"444","hostname":"Web Server","name":"web-1.tail-abc.ts.net","addresses":["100.64.0.44"],"tags":["tag:dev"]}
]}`

type tsServer struct {
	auth    []string
	deleted []string
}

func newServer(t *testing.T, body string) (*Client, *tsServer) {
	t.Helper()
	s := &tsServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/tailnet/-/devices":
			w.Write([]byte(body))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/device/"):
			s.deleted = append(s.deleted, strings.TrimPrefix(r.URL.Path, "/api/v2/device/"))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := New("tskey-api-SECRET", "", "tag:dev")
	c.BaseURL = srv.URL
	return c, s
}

func TestDeletesTheTaggedMatch(t *testing.T) {
	c, s := newServer(t, devices)
	ok, err := c.DeleteDeviceByHostname(context.Background(), "demo", "100.64.0.7")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(s.deleted) != 1 || s.deleted[0] != "111" {
		t.Fatalf("deleted = %v", s.deleted)
	}
	for _, a := range s.auth {
		if a != "Bearer tskey-api-SECRET" {
			t.Fatalf("auth = %q", a)
		}
	}
}

func TestMatchesByMagicDNSName(t *testing.T) {
	c, s := newServer(t, devices)
	ok, err := c.DeleteDeviceByHostname(context.Background(), "web-1", "100.64.0.44")
	if err != nil || !ok || len(s.deleted) != 1 || s.deleted[0] != "444" {
		t.Fatalf("ok=%v err=%v deleted=%v", ok, err, s.deleted)
	}
}

func TestNeverDeletesUntaggedOrMissing(t *testing.T) {
	c, s := newServer(t, devices)
	for _, h := range []string{"laptop", "web", "nothere"} {
		ok, err := c.DeleteDeviceByHostname(context.Background(), h, "100.64.0.7")
		if err != nil || ok {
			t.Errorf("%s: ok=%v err=%v", h, ok, err)
		}
	}
	if len(s.deleted) != 0 {
		t.Fatalf("deleted = %v", s.deleted)
	}
}

func TestAmbiguousMatchDeletesNothing(t *testing.T) {
	c, s := newServer(t, `{"devices":[{"id":"1","hostname":"demo","tags":["tag:dev"]},{"id":"2","hostname":"demo","tags":["tag:dev"]}]}`)
	ok, err := c.DeleteDeviceByHostname(context.Background(), "demo", "100.64.0.7")
	if err == nil || ok || len(s.deleted) != 0 {
		t.Fatalf("ok=%v err=%v deleted=%v", ok, err, s.deleted)
	}
}

func TestAPIErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"API token invalid"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New("k", "-", "tag:dev")
	c.BaseURL = srv.URL
	if _, err := c.DeleteDeviceByHostname(context.Background(), "demo", "100.64.0.7"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestNameMatchAtAnotherAddressIsNotDeleted(t *testing.T) {
	c, s := newServer(t, devices)
	for _, ip := range []string{"100.64.0.99", ""} {
		ok, err := c.DeleteDeviceByHostname(context.Background(), "demo", ip)
		if err != nil || ok {
			t.Errorf("%q: ok=%v err=%v", ip, ok, err)
		}
	}
	if len(s.deleted) != 0 {
		t.Fatalf("deleted = %v", s.deleted)
	}
}

func TestFindDeviceNeedsNameTagAndAddress(t *testing.T) {
	c, _ := newServer(t, `{"devices":[
 {"id":"OWN","hostname":"demo","addresses":["100.64.0.7","fd7a::7"],"tags":["tag:dev"]},
 {"id":"STALE","hostname":"demo","addresses":["100.64.0.99"],"tags":["tag:dev"]},
 {"id":"UNTAGGED","hostname":"demo","addresses":["100.64.0.8"]}
]}`)
	for ip, want := range map[string]string{"100.64.0.7": "OWN", "100.64.0.8": "", "100.64.0.5": ""} {
		if got, err := c.FindDevice(context.Background(), "demo", ip); err != nil || got != want {
			t.Errorf("%s: got %q err %v, want %q", ip, got, err, want)
		}
	}
	if got, err := c.FindDevice(context.Background(), "other", "100.64.0.7"); err != nil || got != "" {
		t.Errorf("other: %q %v", got, err)
	}
}

func TestDeleteDeviceByID(t *testing.T) {
	c, s := newServer(t, devices)
	if err := c.DeleteDevice(context.Background(), "nABC123"); err != nil {
		t.Fatal(err)
	}
	if len(s.deleted) != 1 || s.deleted[0] != "nABC123" {
		t.Fatalf("deleted = %v", s.deleted)
	}
}
