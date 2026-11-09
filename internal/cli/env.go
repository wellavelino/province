package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/wellavelino/province/internal/env"
	"github.com/wellavelino/province/internal/errs"
)

func (a *app) envCmd() *cobra.Command {
	c := &cobra.Command{Use: "env", Short: "Manage environments (LXC containers on Proxmox or Incus)"}
	c.AddCommand(a.envCreateCmd(), a.envListCmd(), a.envShowCmd(), a.envDestroyCmd(), a.envExposeCmd(), a.envUnexposeCmd(), a.envProvisionCmd())
	return c
}

// guestLabel names a guest in human output: its VMID on Proxmox, its
// instance name on Incus, "-" before it exists.
func guestLabel(vmid int, instance string) string {
	switch {
	case instance != "":
		return instance
	case vmid != 0:
		return fmt.Sprint(vmid)
	}
	return "-"
}

// guestRef is "vmid 105" or "instance api".
func guestRef(vmid int, instance string) string {
	if instance != "" {
		return "instance " + instance
	}
	return fmt.Sprintf("vmid %d", vmid)
}

func printRecipe(w io.Writer, r *env.Recipe) {
	if r == nil {
		return
	}
	fmt.Fprintf(w, "recipe %s\n", r.Name)
	if len(r.Packages) > 0 {
		fmt.Fprintf(w, "  packages  %s\n", strings.Join(r.Packages, " "))
	}
	if len(r.Tools) > 0 {
		fmt.Fprintf(w, "  tools     %s\n", strings.Join(r.Tools, " "))
	}
	if r.Dotfiles != "" {
		fmt.Fprintf(w, "  dotfiles  %s\n", r.Dotfiles)
	}
	for _, c := range r.Run {
		fmt.Fprintf(w, "  run       %s\n", c)
	}
}

func (a *app) envCreateCmd() *cobra.Command {
	var kind string
	var o env.CreateOpts
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Plan (default) or, with --apply, create and bootstrap an environment",
		Args:  exactArgs(1, "name"),
		RunE: a.action(func(cmd *cobra.Command, args []string) error {
			switch kind {
			case "lxc":
			case "vm":
				return errs.Refusedf("--kind vm is not supported in v1; use --kind lxc")
			default:
				return errs.Refusedf("--kind must be lxc or vm, got %q", kind)
			}
			m, err := a.manager()
			if err != nil {
				return err
			}
			o.Name = args[0]
			res, err := m.Create(cmd.Context(), o)
			if err != nil {
				return err
			}
			return a.emit(res, func(w io.Writer) {
				for _, s := range res.Warnings {
					fmt.Fprintf(w, "warning: %s\n", s)
				}
				if res.Status == env.StatusBootstrapped {
					fmt.Fprintf(w, "Created %s on %s (%s), tailnet IP %s\n%s\n", res.Name, res.Host, guestRef(res.VMID, res.Instance), res.TailscaleIP, res.SSH)
					if res.Recipe != nil {
						fmt.Fprintf(w, "Applied recipe %s\n", res.Recipe.Name)
					}
					if res.PublicURL != "" {
						fmt.Fprintf(w, "Public (internet): %s -> port %d\n", res.PublicURL, res.ExposePort)
					}
					return
				}
				fmt.Fprintf(w, "%s\n", strings.TrimRight(res.Plan, "\n"))
				printRecipe(w, res.Recipe)
				as := fmt.Sprintf("vmid %d", res.VMID)
				if res.Backend == "incus" {
					as = "instance " + res.Name
				}
				fmt.Fprintf(w, "Planned %s on %s as %s (state in %s). Re-run with --apply to create it.\n", res.Name, res.Host, as, res.Dir)
			})
		}),
	}
	fl := cmd.Flags()
	fl.StringVar(&kind, "kind", "lxc", "guest kind: lxc (vm is not supported in v1)")
	fl.StringVar(&o.Host, "host", "", "host name from the site config (default: defaults.host)")
	fl.IntVar(&o.CPU, "cpu", 0, "CPU cores (default: defaults.cpu)")
	fl.IntVar(&o.RAM, "ram", 0, "memory in MiB (default: defaults.ram)")
	fl.IntVar(&o.Disk, "disk", 0, "root disk in GiB (default: defaults.disk)")
	fl.BoolVar(&o.Docker, "docker", false, "install Docker and enable keyctl")
	fl.IntVar(&o.Expose, "expose", 0, "publish this guest port on the public internet through Tailscale Funnel (https://<name>.<tailnet>.ts.net/)")
	fl.StringVar(&o.Recipe, "recipe", "", "recipe from the site config to apply after bootstrap (default: the recorded one, then defaults.recipe; none for none)")
	fl.BoolVar(&o.Apply, "apply", false, "apply the plan and bootstrap the guest")
	return cmd
}

type listResult struct {
	Envs []env.View `json:"envs"`
}

func (a *app) envListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List environments with their live status",
		Args:  exactArgs(0),
		RunE: a.action(func(cmd *cobra.Command, _ []string) error {
			m, err := a.manager()
			if err != nil {
				return err
			}
			views, err := m.List(cmd.Context())
			if err != nil {
				return err
			}
			return a.emit(listResult{Envs: views}, func(w io.Writer) {
				tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tHOST\tGUEST\tSTATUS\tLIVE\tRECIPE\tTAILNET IP\tPUBLIC URL")
				for _, v := range views {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", v.Name, v.Host, guestLabel(v.VMID, v.Instance), v.Status, v.LiveStatus, v.Recipe, v.TailscaleIP, v.PublicURL)
				}
				tw.Flush()
				for _, v := range views {
					if v.Error != "" {
						fmt.Fprintf(w, "%s: %s\n", v.Name, v.Error)
					}
				}
			})
		}),
	}
}

func (a *app) envShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show one environment, its live status and how to reach it",
		Args:  exactArgs(1, "name"),
		RunE: a.action(func(cmd *cobra.Command, args []string) error {
			m, err := a.manager()
			if err != nil {
				return err
			}
			v, err := m.Show(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(v, func(w io.Writer) {
				if v.Instance != "" || v.Backend == "incus" {
					fmt.Fprintf(w, "name:         %s\nhost:         %s (incus)\ninstance:     %s\nstatus:       %s\nlive:         %s\n",
						v.Name, v.Host, guestLabel(0, v.Instance), v.Status, v.LiveStatus)
				} else {
					fmt.Fprintf(w, "name:         %s\nhost:         %s (node %s)\nvmid:         %d\nstatus:       %s\nlive:         %s\n",
						v.Name, v.Host, v.Node, v.VMID, v.Status, v.LiveStatus)
				}
				if v.Recipe != "" {
					fmt.Fprintf(w, "recipe:       %s\n", v.Recipe)
				}
				fmt.Fprintf(w, "size:         %d cores, %d MiB, %d GiB\ndocker:       %v\ntailnet IP:   %s\n", v.CPU, v.RAM, v.Disk, v.Docker, v.TailscaleIP)
				if v.SSH != "" {
					fmt.Fprintf(w, "ssh:          %s\n", v.SSH)
				}
				switch {
				case v.PublicURL != "":
					fmt.Fprintf(w, "public URL:   %s -> port %d (Tailscale Funnel)\n", v.PublicURL, v.ExposePort)
				case v.ExposePort != 0:
					fmt.Fprintf(w, "public URL:   none yet (port %d is published once the env is bootstrapped)\n", v.ExposePort)
				}
				if v.Error != "" {
					fmt.Fprintf(w, "error:        %s\n", v.Error)
				}
			})
		}),
	}
}

func (a *app) envDestroyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "destroy <name> --yes",
		Short: "Destroy an environment: tailnet node, container and state",
		Args:  exactArgs(1, "name"),
		RunE: a.action(func(cmd *cobra.Command, args []string) error {
			if !yes {
				return errs.Refusedf("env destroy %s deletes the container and its disk: pass --yes", args[0])
			}
			m, err := a.manager()
			if err != nil {
				return err
			}
			res, err := m.Destroy(cmd.Context(), args[0], yes)
			if err != nil {
				return err
			}
			return a.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "Destroyed %s (%s on %s)\n", res.Name, guestRef(res.VMID, res.Instance), res.Host)
				for _, s := range res.Warnings {
					fmt.Fprintf(w, "warning: %s\n", s)
				}
			})
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the destruction")
	return cmd
}

func (a *app) envExposeCmd() *cobra.Command {
	var port int
	var yes bool
	cmd := &cobra.Command{
		Use:   "expose <name> --port <p> --yes",
		Short: "Publish a guest port on the public internet through Tailscale Funnel",
		Args:  exactArgs(1, "name"),
		RunE: a.action(func(cmd *cobra.Command, args []string) error {
			if port == 0 {
				return errs.Refusedf("env expose needs --port <guest port>")
			}
			if !yes {
				return errs.Refusedf("env expose %s publishes port %d to the public internet (Tailscale Funnel): pass --yes", args[0], port)
			}
			m, err := a.manager()
			if err != nil {
				return err
			}
			res, err := m.Expose(cmd.Context(), args[0], port, yes)
			if err != nil {
				return err
			}
			return a.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "Published %s port %d on the public internet: %s\n", res.Name, res.Port, res.PublicURL)
			})
		}),
	}
	cmd.Flags().IntVar(&port, "port", 0, "guest port to publish (served as https on 443)")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm publishing to the internet")
	return cmd
}

func (a *app) envUnexposeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "unexpose <name> --yes",
		Short: "Turn off an environment's Tailscale Funnel public URL",
		Args:  exactArgs(1, "name"),
		RunE: a.action(func(cmd *cobra.Command, args []string) error {
			if !yes {
				return errs.Refusedf("env unexpose %s turns off its public URL: pass --yes", args[0])
			}
			m, err := a.manager()
			if err != nil {
				return err
			}
			res, err := m.Unexpose(cmd.Context(), args[0], yes)
			if err != nil {
				return err
			}
			return a.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "%s is no longer published on the internet\n", res.Name)
			})
		}),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm")
	return cmd
}

func (a *app) envProvisionCmd() *cobra.Command {
	var recipe string
	cmd := &cobra.Command{
		Use:   "provision <name>",
		Short: "Apply a recipe (packages, tools, dotfiles) to a running environment again",
		Args:  exactArgs(1, "name"),
		RunE: a.action(func(cmd *cobra.Command, args []string) error {
			m, err := a.manager()
			if err != nil {
				return err
			}
			res, err := m.Provision(cmd.Context(), args[0], recipe)
			if err != nil {
				return err
			}
			return a.emit(res, func(w io.Writer) {
				printRecipe(w, res.Recipe)
				fmt.Fprintf(w, "Applied recipe %s to %s\n", res.Recipe.Name, res.Name)
			})
		}),
	}
	cmd.Flags().StringVar(&recipe, "recipe", "", "recipe from the site config (default: the env's recorded recipe, then defaults.recipe)")
	return cmd
}
