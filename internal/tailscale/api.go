// Package tailscale removes a destroyed environment's node from the tailnet
// through the Tailscale API (tagged nodes never expire on their own).
package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// DefaultBaseURL is the public Tailscale API.
const DefaultBaseURL = "https://api.tailscale.com"

// Client talks to the Tailscale API with an API key.
type Client struct {
	BaseURL string
	APIKey  string
	Tailnet string // "-" means the key's own tailnet
	Tag     string // only devices carrying this tag are ever deleted
	HTTP    *http.Client
}

// New returns a client for the public API.
func New(apiKey, tailnet, tag string) *Client {
	if tailnet == "" {
		tailnet = "-"
	}
	return &Client{BaseURL: DefaultBaseURL, APIKey: apiKey, Tailnet: tailnet, Tag: tag, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type device struct {
	ID        string   `json:"id"`
	Hostname  string   `json:"hostname"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Tags      []string `json:"tags"`
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("tailscale API %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("tailscale API %s %s: %s: %s", method, path, resp.Status, msg)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("tailscale API %s %s: %w", method, path, err)
		}
	}
	return nil
}

// listDevices returns the tailnet's devices.
func (c *Client) listDevices(ctx context.Context) ([]device, error) {
	var list struct {
		Devices []device `json:"devices"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v2/tailnet/"+url.PathEscape(c.Tailnet)+"/devices", &list); err != nil {
		return nil, err
	}
	return list.Devices, nil
}

// named reports whether d is called hostname (its hostname, or a MagicDNS
// name starting with "hostname.") and carries the configured tag.
func (c *Client) named(d device, hostname string) bool {
	if d.Hostname != hostname && !strings.HasPrefix(d.Name, hostname+".") {
		return false
	}
	return c.Tag == "" || slices.Contains(d.Tags, c.Tag)
}

func ids(ds []device) string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.ID
	}
	return strings.Join(out, ", ")
}

// FindDevice returns the ID of the one device called hostname, carrying
// the configured tag and holding address ip; "" when there is none. It
// refuses to guess when several match.
func (c *Client) FindDevice(ctx context.Context, hostname, ip string) (string, error) {
	devices, err := c.listDevices(ctx)
	if err != nil {
		return "", err
	}
	var match []device
	for _, d := range devices {
		if c.named(d, hostname) && slices.Contains(d.Addresses, ip) {
			match = append(match, d)
		}
	}
	switch len(match) {
	case 0:
		return "", nil
	case 1:
		return match[0].ID, nil
	}
	return "", fmt.Errorf("%d tailnet devices match %s at %s (%s)", len(match), hostname, ip, ids(match))
}

// DeleteDevice deletes the device with id.
func (c *Client) DeleteDevice(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v2/device/"+url.PathEscape(id), nil)
}

// DeleteDeviceByHostname deletes the one device called hostname that
// carries the configured tag, and only if it holds address ip. It reports
// false when there is no such device (or the one so named holds another
// address), and refuses to guess when several are so named.
func (c *Client) DeleteDeviceByHostname(ctx context.Context, hostname, ip string) (bool, error) {
	devices, err := c.listDevices(ctx)
	if err != nil {
		return false, err
	}
	var match []device
	for _, d := range devices {
		if c.named(d, hostname) {
			match = append(match, d)
		}
	}
	switch {
	case len(match) > 1:
		return false, fmt.Errorf("%d tailnet devices match %s (%s); delete the stale one in the admin console", len(match), hostname, ids(match))
	case len(match) == 0 || ip == "" || !slices.Contains(match[0].Addresses, ip):
		return false, nil
	}
	return true, c.DeleteDevice(ctx, match[0].ID)
}
