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
	ID       string   `json:"id"`
	Hostname string   `json:"hostname"`
	Name     string   `json:"name"`
	Tags     []string `json:"tags"`
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

// DeleteDeviceByHostname deletes the one device whose hostname is hostname
// (or whose MagicDNS name starts with "hostname.") and that carries the
// configured tag. It reports false when there is none, and refuses to
// guess when several match.
func (c *Client) DeleteDeviceByHostname(ctx context.Context, hostname string) (bool, error) {
	var list struct {
		Devices []device `json:"devices"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v2/tailnet/"+url.PathEscape(c.Tailnet)+"/devices", &list); err != nil {
		return false, err
	}
	var match []device
	for _, d := range list.Devices {
		if d.Hostname != hostname && !strings.HasPrefix(d.Name, hostname+".") {
			continue
		}
		if c.Tag != "" && !slices.Contains(d.Tags, c.Tag) {
			continue
		}
		match = append(match, d)
	}
	switch len(match) {
	case 0:
		return false, nil
	case 1:
		return true, c.do(ctx, http.MethodDelete, "/api/v2/device/"+url.PathEscape(match[0].ID), nil)
	}
	ids := make([]string, len(match))
	for i, d := range match {
		ids[i] = d.ID
	}
	return false, fmt.Errorf("%d tailnet devices match %s (%s); delete the stale one in the admin console", len(match), hostname, strings.Join(ids, ", "))
}
