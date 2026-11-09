package cli

import (
	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/env"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/hostops"
	"github.com/wellavelino/province/internal/incus"
	"github.com/wellavelino/province/internal/move"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/secrets"
	"github.com/wellavelino/province/internal/tofu"
)

// site is the loaded config and secrets of one invocation.
type site struct {
	cfg     *config.Config
	secrets secrets.Secrets
}

func (a *app) loadSite() (*site, error) {
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return nil, &errs.Error{Kind: errs.Refused, Err: err}
	}
	// Register every configured token with the redactor and drop it from
	// the environment, whether or not this command needs it.
	for _, h := range cfg.ProxmoxHosts() {
		a.red.AddToken(a.deps.Getenv(h.TokenEnv))
		if a.deps.Unsetenv != nil {
			a.deps.Unsetenv(h.TokenEnv)
		}
	}
	return &site{cfg: cfg, secrets: secrets.FromEnv(a.deps.Getenv)}, nil
}

// token returns host h's API token; a missing or malformed one is refused.
func (a *app) token(h config.Host) (string, error) {
	t, err := secrets.Token(a.deps.Getenv, h.TokenEnv, h.Name)
	if err != nil {
		return "", &errs.Error{Kind: errs.Refused, Err: err}
	}
	return t, nil
}

func (a *app) client() func(config.Host) (proxmox.Client, error) {
	return func(h config.Host) (proxmox.Client, error) {
		t, err := a.token(h)
		if err != nil {
			return nil, err
		}
		return a.deps.NewClient(h, t), nil
	}
}

func (a *app) tofu() func(config.Host) (tofu.Tofu, error) {
	return func(h config.Host) (tofu.Tofu, error) {
		// Incus hosts authenticate through the operator's incus remote; tofu
		// gets no token for them.
		var t string
		if !h.IsIncus() {
			var err error
			if t, err = a.token(h); err != nil {
				return nil, err
			}
		}
		tf, err := a.deps.NewTofu(t, a.stderr)
		if err != nil {
			return nil, &errs.Error{Kind: errs.Preflight, Err: err}
		}
		return tf, nil
	}
}

func (a *app) host() func(config.Host) hostops.Host {
	return func(h config.Host) hostops.Host { return a.deps.NewHost(h, a.stderr) }
}

func (a *app) incus() func(config.Host) incus.Client {
	return func(h config.Host) incus.Client { return a.deps.NewIncus(h, a.stderr) }
}

// manager builds an env.Manager.
func (a *app) manager() (*env.Manager, error) {
	s, err := a.loadSite()
	if err != nil {
		return nil, err
	}
	return a.managerFor(s), nil
}

func (a *app) managerFor(s *site) *env.Manager {
	m := &env.Manager{
		Cfg:     s.cfg,
		Secrets: s.secrets,
		Tofu:    a.tofu(),
		Client:  a.client(),
		Host:    a.host(),
		Incus:   a.incus(),
		Now:     a.deps.Now,
		Log:     a.stderr,
	}
	if s.secrets.APIKey != "" {
		m.Tailnet = a.deps.NewTailnet(s.secrets.APIKey, s.secrets.Tailnet, s.cfg.Tailscale.Tag)
	}
	return m
}

func (a *app) moverFor(s *site) *move.Mover {
	return &move.Mover{
		Cfg:    s.cfg,
		Client: a.client(),
		Host:   a.host(),
		Runner: a.deps.Runner,
		Now:    a.deps.Now,
		Log:    a.stderr,
	}
}
