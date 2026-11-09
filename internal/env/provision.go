package env

import (
	"bytes"
	"context"

	"github.com/wellavelino/province/internal/bootstrap"
	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
)

// Recipe is a resolved recipe as plans and results show it.
type Recipe struct {
	Name     string   `json:"name"`
	Packages []string `json:"packages"`
	Tools    []string `json:"tools"` // name@version
	Dotfiles string   `json:"dotfiles,omitempty"`
	Run      []string `json:"run"`

	resolved config.Recipe
}

// recipe resolves the recipe an env gets from the flag, the recorded
// choice and defaults.recipe; nil means none.
func (m *Manager) recipe(flag, recorded string) (*Recipe, error) {
	name := m.Cfg.RecipeName(flag, recorded)
	if name == "" {
		return nil, nil
	}
	r, err := m.Cfg.ResolveRecipe(name)
	if err != nil {
		return nil, &errs.Error{Kind: errs.Refused, Err: err}
	}
	v := &Recipe{Name: name, Packages: r.Packages, Tools: bootstrap.Tools(r), Dotfiles: r.Dotfiles, Run: r.Run, resolved: r}
	if v.Packages == nil {
		v.Packages = []string{}
	}
	if v.Tools == nil {
		v.Tools = []string{}
	}
	if v.Run == nil {
		v.Run = []string{}
	}
	return v, nil
}

// applyRecipe runs the recipe script in the guest.
func (m *Manager) applyRecipe(ctx context.Context, g guest, md *Metadata, r *Recipe) error {
	payload, err := bootstrap.RecipePayload(bootstrap.RecipeParams{User: m.Cfg.User, Hostname: md.Name, Recipe: r.resolved})
	if err != nil {
		return err
	}
	m.logf("applying recipe %s to %s", r.Name, md.Name)
	return g.Exec(ctx, bytes.NewReader(payload), nil, "bash", "-s")
}

// ProvisionResult is the outcome of env provision.
type ProvisionResult struct {
	Name   string  `json:"name"`
	Host   string  `json:"host"`
	Recipe *Recipe `json:"recipe"`
}

// Provision applies a recipe to a bootstrapped, running env: recipeFlag,
// else the env's recorded recipe, else defaults.recipe. The choice is
// recorded once the recipe has run.
func (m *Manager) Provision(ctx context.Context, name, recipeFlag string) (*ProvisionResult, error) {
	md, g, err := m.runningEnv(ctx, name, "provision")
	if err != nil {
		return nil, err
	}
	r, err := m.recipe(recipeFlag, md.Recipe)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errs.Refusedf("env %s has no recipe: pass --recipe <name> (or set defaults.recipe)", name)
	}
	if err := m.applyRecipe(ctx, g, md, r); err != nil {
		return nil, err
	}
	md.Recipe = r.Name
	if err := m.writeMetadata(m.dir(name), md); err != nil {
		return nil, err
	}
	return &ProvisionResult{Name: name, Host: md.Host, Recipe: r}, nil
}
