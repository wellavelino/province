package env

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/hostops"
	"github.com/wellavelino/province/internal/hostops/hostopstest"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/proxmox/proxmoxtest"
	"github.com/wellavelino/province/internal/secrets"
	"github.com/wellavelino/province/internal/tofu"
	"github.com/wellavelino/province/internal/tofu/tofutest"
)

const (
	template = "local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst"
	authKey  = "tskey-auth-kSECRET-0123456789"
	apiToken = "province@pve!cli=0b8e7d6c-1111-2222-3333-444455556666"
	pubKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk+/9Qx= dev@laptop"
)

var fixedNow = time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC)

type rig struct {
	m         *Manager
	tofu      *tofutest.Fake
	tofuHosts []string // host each Tofu() call was made for
	clients   map[string]*proxmoxtest.Fake
	hosts     map[string]*hostopstest.Fake
	j         *hostopstest.Journal
	log       *bytes.Buffer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	tmp := t.TempDir()
	keyFile := filepath.Join(tmp, "id.pub")
	if err := os.WriteFile(keyFile, []byte(pubKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		StateDir:      filepath.Join(tmp, "state"),
		User:          "dev",
		VMIDStart:     100,
		SSHPublicKeys: []string{keyFile},
		Tailscale:     config.Tailscale{Tag: "tag:dev", Tun: config.TunUserspace},
		Defaults: config.Defaults{Host: "pve1", Storage: "local-lvm", Bridge: "vmbr0", Template: template,
			CPU: 2, RAM: 2048, Disk: 16},
		Hosts: []config.Host{
			{Name: "pve1", Node: "pve1", Endpoint: "https://pve1.example:8006", SSH: "pve1", DumpDir: "/var/lib/vz/dump", TokenEnv: "PROXMOX_VE_API_TOKEN"},
			{Name: "pve2", Node: "pve2", Endpoint: "https://pve2.example:8006", SSH: "pve2", DumpDir: "/var/lib/vz/dump", TokenEnv: "PROXMOX_VE_API_TOKEN"},
		},
	}
	j := &hostopstest.Journal{}
	r := &rig{
		tofu:    &tofutest.Fake{PlanText: "Plan: 1 to add", Changes: true, Outputs: map[string]json.RawMessage{"vmid": json.RawMessage("105")}},
		clients: map[string]*proxmoxtest.Fake{},
		hosts:   map[string]*hostopstest.Fake{},
		j:       j,
		log:     &bytes.Buffer{},
	}
	r.tofu.OnCall = func(c string) { j.Add("tofu " + c) }
	for _, h := range cfg.Hosts {
		r.clients[h.Name] = &proxmoxtest.Fake{
			Templates: []string{template},
			BridgeLs:  []string{"vmbr0"},
			StorageLs: []proxmox.Storage{{Name: "local-lvm", Content: []string{"rootdir", "images"}, Avail: 100 << 30, Active: true}},
		}
		r.hosts[h.Name] = &hostopstest.Fake{Name: h.Name, Journal: j, ExecOut: "apt noise\nPROVINCE_TS_IP=100.64.0.7\n"}
	}
	r.m = &Manager{
		Cfg:     cfg,
		Secrets: secrets.Secrets{AuthKey: authKey, Tailnet: "-"},
		Tofu: func(h config.Host) (tofu.Tofu, error) {
			r.tofuHosts = append(r.tofuHosts, h.Name)
			return r.tofu, nil
		},
		Client: func(h config.Host) (proxmox.Client, error) { return r.clients[h.Name], nil },
		Host:   func(h config.Host) hostops.Host { return r.hosts[h.Name] },
		Now:    func() time.Time { return fixedNow },
		Log:    r.log,
	}
	return r
}

func (r *rig) dir(name string) string { return filepath.Join(r.m.Cfg.StateDir, name) }

func (r *rig) metadata(t *testing.T, name string) *Metadata {
	t.Helper()
	md, err := readMetadata(r.dir(name))
	if err != nil {
		t.Fatal(err)
	}
	return md
}

// mutations counts calls that could change anything: tofu and SSH.
func (r *rig) mutations() int { return len(r.j.Entries()) }

func (r *rig) apiCalls() int {
	n := 0
	for _, c := range r.clients {
		n += len(c.Calls())
	}
	return n
}
