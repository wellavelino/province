package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wellavelino/province/internal/env"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/move"
)

func (a *app) guestCmd() *cobra.Command {
	c := &cobra.Command{Use: "guest", Short: "Operate on existing Proxmox guests"}
	c.AddCommand(a.guestMoveCmd(), a.guestPruneCmd())
	return c
}

// moveResult is a move plus, when the guest was a province env, how its
// records followed it.
type moveResult struct {
	*move.Result
	Env *env.RehomeResult `json:"env,omitempty"`
}

func (a *app) guestMoveCmd() *cobra.Command {
	var o move.Opts
	cmd := &cobra.Command{
		Use:   "move <vmid> --from <host> --to <host> --yes",
		Short: "Move a container or VM to another host (stop, dump, restore with the same VMID)",
		Args:  exactArgs(1, "vmid"),
		RunE: a.action(func(cmd *cobra.Command, args []string) error {
			vmid, err := strconv.Atoi(args[0])
			if err != nil || vmid <= 0 {
				return errs.Refusedf("vmid must be a positive number, got %q", args[0])
			}
			o.VMID = vmid
			if !o.Yes {
				return errs.Refusedf("guest move stops guest %d on %s: pass --yes", vmid, o.From)
			}
			s, err := a.loadSite()
			if err != nil {
				return err
			}
			mgr := a.managerFor(s)
			envName, err := mgr.FindByGuest(o.From, vmid)
			if err != nil {
				return err
			}
			moved, err := a.moverFor(s).Move(cmd.Context(), o)
			if err != nil {
				return err
			}
			res := moveResult{Result: moved}
			if envName != "" {
				// The guest is safe on the target; this only updates records.
				to, _ := s.cfg.HostByName(o.To)
				res.Env = mgr.Rehome(cmd.Context(), envName, to, moved.TargetVMID, moved.Storage)
			}
			return a.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "Moved %s %d from %s to %s as %d (storage %s); target is %s.\nSource on %s: %s\n",
					res.Type, res.VMID, res.From, res.To, res.TargetVMID, res.Storage, res.Target, res.From, res.Source)
				if res.Note != "" {
					fmt.Fprintln(w, res.Note)
				}
				if res.Dump != "" {
					fmt.Fprintf(w, "dump left at %s on %s\n", res.Dump, res.To)
				}
				for _, s := range res.Warnings {
					fmt.Fprintf(w, "warning: %s\n", s)
				}
				if e := res.Env; e != nil {
					if !e.TofuOutOfSync {
						fmt.Fprintf(w, "env %s now recorded on %s as vmid %d (tofu state re-imported)\n", e.Name, e.Host, e.VMID)
					} else {
						fmt.Fprintf(w, "env %s: tofu state NOT updated (%s); finish by hand:\n  %s\n", e.Name, e.Error, strings.Join(e.Fix, "\n  "))
					}
				}
			})
		}),
	}
	fl := cmd.Flags()
	fl.StringVar(&o.From, "from", "", "source host name (required)")
	fl.StringVar(&o.To, "to", "", "target host name (required)")
	fl.IntVar(&o.ToVMID, "to-vmid", 0, "restore under this VMID on the target (the MAC is kept, so DHCP gives the same IP)")
	fl.StringVar(&o.Storage, "storage", "", "target storage (default: the source's rootfs/first-disk storage)")
	fl.BoolVar(&o.Start, "start", false, "start the guest on the target after the source is set onboot=0")
	fl.BoolVar(&o.Yes, "yes", false, "confirm the move")
	return cmd
}

func (a *app) guestPruneCmd() *cobra.Command {
	var o move.PruneOpts
	cmd := &cobra.Command{
		Use:   "prune <vmid> --host <host> --yes",
		Short: "Delete the stopped copy a move left behind (pct|qm destroy --purge)",
		Args:  exactArgs(1, "vmid"),
		RunE: a.action(func(cmd *cobra.Command, args []string) error {
			vmid, err := strconv.Atoi(args[0])
			if err != nil || vmid <= 0 {
				return errs.Refusedf("vmid must be a positive number, got %q", args[0])
			}
			o.VMID = vmid
			if !o.Yes {
				return errs.Refusedf("guest prune deletes guest %d and its disks on %s: pass --yes", vmid, o.Host)
			}
			if o.Host == "" {
				return errs.Refusedf("--host is required")
			}
			s, err := a.loadSite()
			if err != nil {
				return err
			}
			o.EnvAt = a.managerFor(s).FindByGuest
			res, err := a.moverFor(s).Prune(cmd.Context(), o)
			if err != nil {
				return err
			}
			return a.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "Destroyed %s %d (%s) on %s", res.Type, res.VMID, res.Name, res.Host)
				if len(res.CopiesOn) > 0 {
					fmt.Fprintf(w, "; copy remains on %s", strings.Join(res.CopiesOn, ", "))
				}
				fmt.Fprintln(w)
				for _, s := range res.Warnings {
					fmt.Fprintf(w, "warning: %s\n", s)
				}
			})
		}),
	}
	fl := cmd.Flags()
	fl.StringVar(&o.Host, "host", "", "host holding the copy to delete (required)")
	fl.BoolVar(&o.Force, "force", false, "delete even if no guest of the same name exists on another host")
	fl.BoolVar(&o.Yes, "yes", false, "confirm the deletion")
	return cmd
}
