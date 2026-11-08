package vmids

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/proxmox/proxmoxtest"
)

func TestUsedAcrossHostsSkipsHostsWithoutToken(t *testing.T) {
	hosts := []config.Host{{Name: "pve1", Node: "pve1"}, {Name: "pve2", Node: "pve2"}, {Name: "pve3", Node: "pve3"}}
	fakes := map[string]*proxmoxtest.Fake{
		"pve1": {Guests: []proxmox.Guest{{VMID: 100, Type: "lxc"}, {VMID: 102, Type: "lxc"}}},
		"pve2": {Guests: []proxmox.Guest{{VMID: 101, Type: "qemu"}}},
		"pve3": {Guests: []proxmox.Guest{{VMID: 103, Type: "lxc"}}},
	}
	client := func(h config.Host) (proxmox.Client, error) {
		if h.Name == "pve3" {
			return nil, errors.New("PROVINCE_TOKEN_PVE3 (API token for host pve3) is not set")
		}
		return fakes[h.Name], nil
	}
	used, warnings := Used(context.Background(), hosts, client)
	if got := LowestFree(used, 100); got != 103 {
		t.Fatalf("LowestFree = %d, want 103 (100-102 used across pve1 and pve2)", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "pve3") || !strings.Contains(warnings[0], "PROVINCE_TOKEN_PVE3") {
		t.Fatalf("warnings = %q", warnings)
	}
	if LowestFree(used, 200) != 200 {
		t.Fatal("start ignored")
	}
}
