package move

import (
	"context"
	"fmt"

	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/proxmox"
)

// PruneOpts are the guest prune flags.
type PruneOpts struct {
	VMID  int
	Host  string
	Force bool // skip the "moved copy exists elsewhere" check
	Yes   bool
	// EnvAt names the province env recorded at host+vmid, or "".
	EnvAt func(host string, vmid int) (string, error)
}

// PruneResult is the outcome of guest prune.
type PruneResult struct {
	VMID      int      `json:"vmid"`
	Host      string   `json:"host"`
	Type      string   `json:"type"`
	Name      string   `json:"name"`
	CopiesOn  []string `json:"copies_on"` // other hosts with a guest of the same name
	Forced    bool     `json:"forced"`
	Destroyed bool     `json:"destroyed"`
	Warnings  []string `json:"warnings"`
}

// Prune deletes the copy a move left behind: a stopped guest with
// onboot=0, no protection, not a province env, and (unless Force) with a
// guest of the same name on another host. Every check is a read.
func (m *Mover) Prune(ctx context.Context, o PruneOpts) (*PruneResult, error) {
	if !o.Yes {
		return nil, errs.Refusedf("guest prune deletes guest %d and its disks on %s: pass --yes", o.VMID, o.Host)
	}
	if o.VMID <= 0 {
		return nil, errs.Refusedf("vmid must be a positive number")
	}
	h, err := m.Cfg.HostByName(o.Host)
	if err != nil {
		return nil, &errs.Error{Kind: errs.Refused, Err: err}
	}
	if o.EnvAt != nil {
		name, err := o.EnvAt(h.Name, o.VMID)
		if err != nil {
			return nil, err
		}
		if name != "" {
			return nil, errs.Refusedf("guest %d on %s is province env %s; use `province env destroy %s --yes`", o.VMID, h.Name, name, name)
		}
	}
	api, err := m.Client(h)
	if err != nil {
		return nil, err
	}
	guests, err := api.ListGuests(ctx, h.Node)
	if err != nil {
		return nil, preflightErr(err)
	}
	var g *proxmox.Guest
	for i := range guests {
		if guests[i].VMID == o.VMID {
			g = &guests[i]
		}
	}
	if g == nil {
		return nil, errs.Preflightf("guest %d not found on %s", o.VMID, h.Name)
	}
	if g.Status != "stopped" {
		return nil, errs.Preflightf("guest %d on %s is %s; prune only deletes a stopped copy left by a move", o.VMID, h.Name, g.Status)
	}
	conf, err := api.GuestConfig(ctx, h.Node, g.Type, o.VMID)
	if err != nil {
		return nil, preflightErr(err)
	}
	if conf["onboot"] != "0" {
		return nil, errs.Preflightf("guest %d on %s does not have onboot=0 (has %q); a move sets that on the copy it leaves behind, so this does not look like one", o.VMID, h.Name, conf["onboot"])
	}
	if conf["protection"] == "1" {
		return nil, errs.Preflightf("guest %d on %s has protection=1; remove the flag first if you really want it gone", o.VMID, h.Name)
	}
	if lock := conf["lock"]; lock != "" {
		return nil, errs.Preflightf("guest %d on %s is locked (%s)", o.VMID, h.Name, lock)
	}

	res := &PruneResult{VMID: o.VMID, Host: h.Name, Type: g.Type, Name: g.Name, CopiesOn: []string{}, Forced: o.Force, Warnings: []string{}}
	for _, other := range m.Cfg.Hosts {
		if other.Name == h.Name {
			continue
		}
		c, err := m.Client(other)
		var og []proxmox.Guest
		if err == nil {
			og, err = c.ListGuests(ctx, other.Node)
		}
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("host %s not checked for a copy: %v", other.Name, err))
			continue
		}
		for _, x := range og {
			if g.Name != "" && x.Name == g.Name {
				res.CopiesOn = append(res.CopiesOn, other.Name)
				break
			}
		}
	}
	if len(res.CopiesOn) == 0 && !o.Force {
		return nil, errs.Preflightf("moved-copy check failed: no guest named %q found on any other configured host, so %d on %s may be the only copy; pass --force to delete it anyway", g.Name, o.VMID, h.Name)
	}

	m.logf("destroying %s %d (%s) on %s", g.Type, o.VMID, g.Name, h.Name)
	if err := m.Host(h).Destroy(ctx, g.Type, o.VMID); err != nil {
		return nil, err
	}
	res.Destroyed = true
	return res, nil
}
