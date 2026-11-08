// Package cli wires the province commands to cobra.
package cli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/secrets"
)

// Main runs province with the real dependencies and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, DefaultDeps(), args, stdout, stderr)
}

// app is the state of one invocation.
type app struct {
	deps       Deps
	red        *secrets.Redactor
	stdout     io.Writer
	stderr     io.Writer
	json       bool
	configPath string
	started    bool // a command's RunE was reached; errors before it are usage errors
}

// Run executes one invocation with the given dependencies. Every byte it
// prints, on stdout or stderr, passes through the secret redactor.
func Run(ctx context.Context, d Deps, args []string, stdout, stderr io.Writer) int {
	red := secrets.FromEnvValues(d.Getenv)
	errw := red.Writer(stderr)
	defer errw.Flush()
	a := &app{deps: d, red: red, stdout: stdout, stderr: errw, json: wantsJSON(args)}
	root := a.rootCmd()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err != nil && !a.started {
		err = &errs.Error{Kind: errs.Refused, Err: err}
	}
	if err != nil {
		a.writeError(err)
	}
	return errs.ExitCode(err)
}

// wantsJSON pre-scans args so that flag parsing errors are reported as JSON too.
func wantsJSON(args []string) bool {
	for _, s := range args {
		if s == "--" {
			return false
		}
		if s == "--json" || s == "--json=true" {
			return true
		}
	}
	return false
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "province",
		Short:         "Create, list, destroy and move environments on Proxmox VE",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if a.configPath == "" {
				a.configPath = a.deps.Getenv("PROVINCE_CONFIG")
			}
			return nil
		},
	}
	root.SetOut(redacted{a})
	root.SetErr(a.stderr)
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().BoolVar(&a.json, "json", a.json, "print exactly one JSON document on stdout")
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "site config file (default $PROVINCE_CONFIG)")

	root.AddCommand(a.envCmd(), a.guestCmd())
	return root
}

// action wraps a command body: it marks the invocation as started so that
// errors from it keep their own kind instead of becoming usage errors.
func (a *app) action(fn func(cmd *cobra.Command, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		a.started = true
		return fn(cmd, args)
	}
}

func exactArgs(n int, names ...string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != n {
			return errs.Refusedf("expected %d argument(s) <%s>, got %d", n, strings.Join(names, "> <"), len(args))
		}
		return nil
	}
}
