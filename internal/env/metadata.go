// Package env implements the environment lifecycle: create, list, show and
// destroy. Each environment is one OpenTofu directory under state_dir.
package env

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/hostops"
	"github.com/wellavelino/province/internal/incus"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/secrets"
	"github.com/wellavelino/province/internal/tofu"
)

// Status of an environment, as recorded in province.json.
type Status string

const (
	StatusPlanned         Status = "planned"
	StatusApplied         Status = "applied"
	StatusBootstrapped    Status = "bootstrapped"
	StatusBootstrapFailed Status = "bootstrap_failed"
)

// MetadataFile is the per-environment record next to the tofu files.
const MetadataFile = "province.json"

// Guest kinds, as recorded in Metadata.Kind.
const (
	KindLXC = "lxc" // a system container (also when empty, as in old records)
	KindVM  = "vm"  // a virtual machine (Incus only)
)

// Metadata is what province knows about an environment.
type Metadata struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // lxc or vm; see KindLXC, KindVM
	// Backend of the env's host: proxmox (also when empty, as in records
	// written before backends existed) or incus.
	Backend     string `json:"backend,omitempty"`
	Host        string `json:"host"`
	Node        string `json:"node"`
	VMID        int    `json:"vmid,omitempty"`     // Proxmox guests
	Instance    string `json:"instance,omitempty"` // Incus guests, once created
	Status      Status `json:"status"`
	CPU         int    `json:"cpu"`
	RAM         int    `json:"ram"`
	Disk        int    `json:"disk"`
	Docker      bool   `json:"docker"`
	Tun         string `json:"tun"`
	TailscaleIP string `json:"tailscale_ip,omitempty"`
	// TailscaleDeviceID is the env's node in the Tailscale API, looked up
	// from the operator side after bootstrap; destroy deletes it by ID.
	TailscaleDeviceID string `json:"tailscale_device_id,omitempty"`
	Storage           string `json:"storage,omitempty"` // set when a move put the disk elsewhere
	// ExposePort is published to the internet through Tailscale Funnel
	// (https://<PublicURL> → localhost:<ExposePort> in the guest).
	ExposePort int    `json:"expose_port,omitempty"`
	PublicURL  string `json:"public_url,omitempty"`
	// Recipe is the recipe last chosen for the env ("none" for none).
	Recipe string `json:"recipe,omitempty"`
	// TofuOutOfSync: a move re-homed the guest but the tofu state could not
	// be pointed at it; TofuFix are the steps that finish it by hand.
	TofuOutOfSync bool     `json:"tofu_out_of_sync,omitempty"`
	TofuFix       []string `json:"tofu_fix,omitempty"`
	// TTL (a Go duration) is how long the env lives once its guest is
	// created; ExpiresAt is then when `env gc --yes` may destroy it.
	TTL       string    `json:"ttl,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	// Protected: env destroy and env gc refuse until `env protect --off`.
	Protected bool      `json:"protected,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Tailnet finds and removes devices in the tailnet (Tailscale API). Only
// devices carrying the configured tag are matched.
type Tailnet interface {
	// FindDevice returns the ID of the one device named hostname that
	// holds address ip, or "" when there is none.
	FindDevice(ctx context.Context, hostname, ip string) (id string, err error)
	// DeleteDevice deletes the device with id.
	DeleteDevice(ctx context.Context, id string) error
	// DeleteDeviceByHostname deletes the one device named hostname, only
	// if it holds address ip.
	DeleteDeviceByHostname(ctx context.Context, hostname, ip string) (deleted bool, err error)
}

// Manager runs environment operations against one site.
type Manager struct {
	Cfg     *config.Config
	Secrets secrets.Secrets
	// Tofu returns tofu set up with the host's API token (create, destroy).
	Tofu func(config.Host) (tofu.Tofu, error)
	// Client returns the API client of a host; it fails when the host's
	// token is not set.
	Client func(config.Host) (proxmox.Client, error)
	Host   func(config.Host) hostops.Host
	// Incus returns the client of an Incus host.
	Incus   func(config.Host) incus.Client
	Tailnet Tailnet // nil when TS_API_KEY is not set
	// AgentTimeout bounds how long create waits for a new VM's guest agent
	// to answer before bootstrapping it (default DefaultAgentTimeout).
	AgentTimeout time.Duration
	// Sleep pauses between tries while waiting; nil sleeps for real,
	// returning early with ctx's error.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
	Log   io.Writer // progress and warnings (stderr)
}

// backend returns the env's backend, proxmox for old records.
func (md *Metadata) backend() string {
	if md.Backend == "" {
		return config.BackendProxmox
	}
	return md.Backend
}

// kind returns the env's guest kind, lxc for records without one.
func (md *Metadata) kind() string {
	if md.Kind == "" {
		return KindLXC
	}
	return md.Kind
}

// vm reports whether the env's guest is a virtual machine.
func (md *Metadata) vm() bool { return md.kind() == KindVM }

// guestNoun is "container" or "VM", for messages.
func (md *Metadata) guestNoun() string {
	if md.vm() {
		return "VM"
	}
	return "container"
}

// created reports whether the env's guest was created (tofu apply ran).
func (md *Metadata) created() bool { return md.VMID != 0 || md.Instance != "" }

// refuseOutOfSync refuses op (destroy, tofu apply) on an env whose tofu
// state still points at the guest it was moved from, with the steps that
// finish the move; nil when the state is in sync.
func (md *Metadata) refuseOutOfSync(op string) error {
	if !md.TofuOutOfSync {
		return nil
	}
	return errs.Refusedf("env %s was moved but its tofu state still points at the old guest, so %s could hit the wrong one; finish the move first:\n  %s",
		md.Name, op, strings.Join(md.TofuFix, "\n  "))
}

// guestLabel names the env's guest without its host: "vmid 105" or
// "instance api".
func (md *Metadata) guestLabel() string {
	if md.backend() == config.BackendIncus {
		return "instance " + md.Name
	}
	return fmt.Sprintf("vmid %d", md.VMID)
}

// guestRef names the env's guest for messages: "vmid 105" or
// "instance api on remote box".
func (md *Metadata) guestRef(h config.Host) string {
	if md.backend() == config.BackendIncus {
		return fmt.Sprintf("instance %s on remote %s", md.Name, h.Remote)
	}
	return fmt.Sprintf("vmid %d", md.VMID)
}

func (m *Manager) dir(name string) string { return filepath.Join(m.Cfg.StateDir, name) }

func (m *Manager) logf(format string, a ...any) {
	if m.Log != nil {
		fmt.Fprintf(m.Log, "province: "+format+"\n", a...)
	}
}

func readMetadata(dir string) (*Metadata, error) {
	b, err := os.ReadFile(filepath.Join(dir, MetadataFile))
	if err != nil {
		return nil, err
	}
	var md Metadata
	if err := json.Unmarshal(b, &md); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, MetadataFile), err)
	}
	return &md, nil
}

// writeMetadata replaces province.json atomically.
func (m *Manager) writeMetadata(dir string, md *Metadata) error {
	md.UpdatedAt = m.Now().UTC()
	b, err := json.MarshalIndent(md, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, MetadataFile), append(b, '\n'))
}

func writeFileAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func mkdirAll(dir string) error { return os.MkdirAll(dir, 0o700) }
