// Package vmids picks VMIDs that are free on every configured host.
// Standalone hosts do not share an ID space, so a guest moved between them
// must not land on an ID the other host already uses.
package vmids

import (
	"context"
	"fmt"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/proxmox"
)

// Used returns the VMIDs in use on the hosts. A host whose client cannot be
// built (its token is not set) or whose guest list cannot be read is
// skipped, with a warning saying so.
func Used(ctx context.Context, hosts []config.Host, client func(config.Host) (proxmox.Client, error)) (map[int]bool, []string) {
	used := map[int]bool{}
	var warnings []string
	for _, h := range hosts {
		c, err := client(h)
		var guests []proxmox.Guest
		if err == nil {
			guests, err = c.ListGuests(ctx, h.Node)
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("host %s skipped in the VMID check: %v", h.Name, err))
			continue
		}
		for _, g := range guests {
			used[g.VMID] = true
		}
	}
	return used, warnings
}

// LowestFree returns the lowest id >= start not in used.
func LowestFree(used map[int]bool, start int) int {
	id := start
	for used[id] {
		id++
	}
	return id
}
