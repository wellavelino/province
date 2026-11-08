// Package proxmox reads from the Proxmox VE HTTP API. It never mutates:
// changes go through tofu or over SSH.
package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is the read-only API surface province uses.
type Client interface {
	ListGuests(ctx context.Context, node string) ([]Guest, error)
	GuestStatus(ctx context.Context, node, typ string, vmid int) (string, error)
	GuestDiskUsed(ctx context.Context, node, typ string, vmid int) (int64, error)
	GuestConfig(ctx context.Context, node, typ string, vmid int) (map[string]string, error)
	Storages(ctx context.Context, node string) ([]Storage, error)
	Bridges(ctx context.Context, node string) ([]string, error)
	TemplateExists(ctx context.Context, node, volid string) (bool, error)
}

// HTTP is the API client for one host.
type HTTP struct {
	base  string // https://host:8006/api2/json
	token string
	hc    *http.Client
}

// New returns a client for endpoint. insecure skips TLS verification for
// this host only (self-signed PVE certificates).
func New(endpoint string, insecure bool, token string) *HTTP {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecure} // opt-in per host
	return &HTTP{
		base:  strings.TrimRight(endpoint, "/") + "/api2/json",
		token: token,
		hc:    &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
}

func (c *HTTP) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox API %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("proxmox API %s: %w", path, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("proxmox API %s: 401 unauthorized: check PROXMOX_VE_API_TOKEN (user@realm!tokenid=secret) and that the token and its role exist on this host", path)
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("proxmox API %s: 403 permission denied: the token's role lacks a privilege (needs VM.Audit, Datastore.Audit, Sys.Audit, SDN.Audit): %s", path, snippet(body))
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("proxmox API %s: %s: %s", path, resp.Status, snippet(body))
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("proxmox API %s: bad JSON: %w", path, err)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("proxmox API %s: unexpected data: %w", path, err)
	}
	return nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func nodePath(node string, parts ...string) string {
	p := "/nodes/" + url.PathEscape(node)
	for _, s := range parts {
		p += "/" + url.PathEscape(s)
	}
	return p
}

func checkType(typ string) error {
	if typ != TypeLXC && typ != TypeQEMU {
		return fmt.Errorf("unknown guest type %q", typ)
	}
	return nil
}

// ListGuests implements Client: containers and VMs of node.
func (c *HTTP) ListGuests(ctx context.Context, node string) ([]Guest, error) {
	var out []Guest
	for _, typ := range []string{TypeLXC, TypeQEMU} {
		var gs []apiGuest
		if err := c.get(ctx, nodePath(node, typ), nil, &gs); err != nil {
			return nil, err
		}
		for _, g := range gs {
			out = append(out, Guest{VMID: int(g.VMID), Name: g.Name, Status: g.Status, Type: typ})
		}
	}
	return out, nil
}

// GuestStatus implements Client.
func (c *HTTP) GuestStatus(ctx context.Context, node, typ string, vmid int) (string, error) {
	if err := checkType(typ); err != nil {
		return "", err
	}
	var st struct {
		Status string `json:"status"`
	}
	err := c.get(ctx, nodePath(node, typ, strconv.Itoa(vmid), "status", "current"), nil, &st)
	return st.Status, err
}

// GuestDiskUsed implements Client: the used bytes PVE reports in
// status/current ("disk"). It is non-zero only for a running container;
// a stopped container and a VM report 0, meaning unknown.
func (c *HTTP) GuestDiskUsed(ctx context.Context, node, typ string, vmid int) (int64, error) {
	if err := checkType(typ); err != nil {
		return 0, err
	}
	var st struct {
		Disk flexInt `json:"disk"`
	}
	err := c.get(ctx, nodePath(node, typ, strconv.Itoa(vmid), "status", "current"), nil, &st)
	return int64(st.Disk), err
}

// GuestConfig implements Client.
func (c *HTTP) GuestConfig(ctx context.Context, node, typ string, vmid int) (map[string]string, error) {
	if err := checkType(typ); err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := c.get(ctx, nodePath(node, typ, strconv.Itoa(vmid), "config"), nil, &raw); err != nil {
		return nil, err
	}
	return configValues(raw), nil
}

// Storages implements Client.
func (c *HTTP) Storages(ctx context.Context, node string) ([]Storage, error) {
	var ss []apiStorage
	if err := c.get(ctx, nodePath(node, "storage"), nil, &ss); err != nil {
		return nil, err
	}
	out := make([]Storage, 0, len(ss))
	for _, s := range ss {
		var content []string
		for _, c := range strings.Split(s.Content, ",") {
			if c = strings.TrimSpace(c); c != "" {
				content = append(content, c)
			}
		}
		out = append(out, Storage{Name: s.Storage, Type: s.Type, Content: content, Avail: int64(s.Avail), Active: s.Active == 1})
	}
	return out, nil
}

// ErrNoBridges means the API listed no bridges, which usually means the
// token cannot see them.
var ErrNoBridges = errors.New("the API lists no bridges: the token's role probably lacks SDN.Audit (bridges are hidden without it)")

// Bridges implements Client.
func (c *HTTP) Bridges(ctx context.Context, node string) ([]string, error) {
	var ifs []apiIface
	if err := c.get(ctx, nodePath(node, "network"), url.Values{"type": {"any_bridge"}}, &ifs); err != nil {
		return nil, err
	}
	if len(ifs) == 0 {
		return nil, fmt.Errorf("node %s: %w", node, ErrNoBridges)
	}
	out := make([]string, len(ifs))
	for i, f := range ifs {
		out[i] = f.Iface
	}
	return out, nil
}

// TemplateExists implements Client. volid is storage:vztmpl/file.
func (c *HTTP) TemplateExists(ctx context.Context, node, volid string) (bool, error) {
	storage, _, ok := strings.Cut(volid, ":")
	if !ok || storage == "" {
		return false, fmt.Errorf("template %q is not a volume id (storage:vztmpl/file)", volid)
	}
	var vols []apiVolume
	if err := c.get(ctx, nodePath(node, "storage", storage, "content"), url.Values{"content": {"vztmpl"}}, &vols); err != nil {
		return false, err
	}
	for _, v := range vols {
		if v.VolID == volid {
			return true, nil
		}
	}
	return false, nil
}
