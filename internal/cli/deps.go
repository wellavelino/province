package cli

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/env"
	"github.com/wellavelino/province/internal/hostops"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/run"
	"github.com/wellavelino/province/internal/secrets"
	"github.com/wellavelino/province/internal/sshx"
	"github.com/wellavelino/province/internal/tailscale"
	"github.com/wellavelino/province/internal/tofu"
)

// Deps holds everything a command touches outside the process, so tests can
// replace each piece with a fake.
type Deps struct {
	Getenv     func(string) string
	Unsetenv   func(string) // removes a secret variable once read; may be nil
	Now        func() time.Time
	Runner     run.Runner // the dump -> receive pipe of guest move
	NewTofu    func(token string, log io.Writer) (tofu.Tofu, error)
	NewClient  func(h config.Host, token string) proxmox.Client
	NewHost    func(h config.Host, log io.Writer) hostops.Host
	NewTailnet func(apiKey, tailnet, tag string) env.Tailnet
}

// DefaultDeps returns the real implementations. Secret variables (the
// fixed names here, each host's token_env once the config is loaded) are
// removed from the process environment after they are read, so no child
// process (ssh, tofu, installers) inherits them; tofu gets its token
// explicitly. Getenv remembers every value it read, so a removed variable
// still reads back.
func DefaultDeps() Deps {
	var mu sync.Mutex
	held := map[string]string{}
	getenv := func(k string) string {
		mu.Lock()
		defer mu.Unlock()
		if v, ok := held[k]; ok {
			return v
		}
		v, ok := os.LookupEnv(k)
		if ok {
			held[k] = v
		}
		return v
	}
	unsetenv := func(k string) {
		getenv(k)
		os.Unsetenv(k)
	}
	for _, n := range secrets.Names {
		unsetenv(n)
	}
	local := run.Local{}
	return Deps{
		Getenv:   getenv,
		Unsetenv: unsetenv,
		Now:      time.Now,
		Runner:   local,
		NewTofu: func(token string, log io.Writer) (tofu.Tofu, error) {
			return tofu.New(local, exec.LookPath, os.Environ(), token, log)
		},
		NewClient: func(h config.Host, token string) proxmox.Client {
			return proxmox.New(h.Endpoint, h.Insecure, token)
		},
		NewHost: func(h config.Host, log io.Writer) hostops.Host {
			return &hostops.SSHHost{Runner: local, Target: sshx.Target{Dest: h.SSH, Sudo: h.Sudo}, Log: log}
		},
		NewTailnet: func(apiKey, tailnet, tag string) env.Tailnet {
			return tailscale.New(apiKey, tailnet, tag)
		},
	}
}
