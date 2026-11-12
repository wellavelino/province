package env

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
)

func withRecipes(r *rig) {
	r.m.Cfg.Recipes = map[string]config.Recipe{
		"base": {Packages: []string{"git"}, Dotfiles: "https://github.com/you/dotfiles"},
		"go":   {Include: []string{"base"}, Tools: map[string]string{"go": "1.24"}},
	}
}

func TestCreatePlanShowsRecipeAndRecordsIt(t *testing.T) {
	r := newRig(t)
	withRecipes(r)
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo", Recipe: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Recipe == nil || res.Recipe.Name != "go" || !slices.Equal(res.Recipe.Packages, []string{"git"}) ||
		!slices.Equal(res.Recipe.Tools, []string{"go@1.24"}) || res.Recipe.Dotfiles == "" {
		t.Fatalf("recipe = %+v", res.Recipe)
	}
	if md := r.metadata(t, "demo"); md.Recipe != "go" {
		t.Fatalf("recorded recipe = %q", md.Recipe)
	}
	// A later --apply without --recipe keeps it.
	res, err = r.m.Create(ctx, CreateOpts{Name: "demo", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pve1 Exec 105 sh -c tailscale ip -4 2>/dev/null || true", "pve1 Exec 105 bash -s", "pve1 Exec 105 bash -s"} // probe, bootstrap, then recipe
	var execs []string
	for _, e := range r.j.Entries() {
		if strings.HasPrefix(e, "pve1 Exec") {
			execs = append(execs, e)
		}
	}
	if !slices.Equal(execs, want) || res.Recipe.Name != "go" {
		t.Fatalf("execs = %v, recipe = %+v", execs, res.Recipe)
	}
	stdins := r.hosts["pve1"].ExecStdins()
	if len(stdins) != 3 || !strings.Contains(stdins[2], "P_TOOLS=go@1.24\n") || strings.Contains(stdins[2], authKey) {
		t.Fatalf("recipe payload wrong or carries the auth key")
	}
}

func TestCreateDefaultsRecipeAndNone(t *testing.T) {
	r := newRig(t)
	withRecipes(r)
	r.m.Cfg.Defaults.Recipe = "base"
	res, err := r.m.Create(ctx, CreateOpts{Name: "demo"})
	if err != nil || res.Recipe == nil || res.Recipe.Name != "base" {
		t.Fatalf("default: %+v %v", res, err)
	}
	res, err = r.m.Create(ctx, CreateOpts{Name: "bare", Recipe: config.RecipeNone})
	if err != nil || res.Recipe != nil {
		t.Fatalf("none: %+v %v", res, err)
	}
	if md := r.metadata(t, "bare"); md.Recipe != config.RecipeNone {
		t.Fatalf("none not recorded: %q", md.Recipe)
	}
}

func TestCreateUnknownRecipeTouchesNothing(t *testing.T) {
	r := newRig(t)
	withRecipes(r)
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Recipe: "rust", Apply: true})
	if errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), `unknown recipe "rust"`) {
		t.Fatalf("err = %v", err)
	}
	if r.mutations() != 0 || r.apiCalls() != 0 {
		t.Fatalf("calls: %v", r.j.Entries())
	}
}

func TestCreateRecipeFailureIsFailedAndNamesProvision(t *testing.T) {
	r := newRig(t)
	withRecipes(r)
	n := 0
	r.hosts["pve1"].Fail = func(call string) error {
		if strings.HasPrefix(call, "pve1 Exec") {
			if n++; n == 3 { // the probe, the bootstrap, the recipe
				return errors.New("apt-get: unable to locate package")
			}
		}
		return nil
	}
	_, err := r.m.Create(ctx, CreateOpts{Name: "demo", Recipe: "base", Apply: true})
	if errs.KindOf(err) != errs.Failed || !strings.Contains(err.Error(), "province env provision demo") {
		t.Fatalf("err = %v", err)
	}
	if md := r.metadata(t, "demo"); md.Status != StatusBootstrapped || md.Recipe != "base" {
		t.Fatalf("metadata = %+v", md)
	}
}

func TestProvision(t *testing.T) {
	r := newRig(t)
	withRecipes(r)
	bootstrapped(t, r)
	if _, err := r.m.Provision(ctx, "demo", ""); errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "--recipe") {
		t.Fatalf("no recipe: %v", err)
	}
	res, err := r.m.Provision(ctx, "demo", "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.Recipe.Name != "go" || r.metadata(t, "demo").Recipe != "go" {
		t.Fatalf("res = %+v", res)
	}
	if got := r.j.Entries(); got[len(got)-1] != "pve1 Exec 105 bash -s" {
		t.Fatalf("calls = %v", got)
	}
	// Re-running uses the recorded recipe.
	res, err = r.m.Provision(ctx, "demo", "")
	if err != nil || res.Recipe.Name != "go" {
		t.Fatalf("re-run: %+v %v", res, err)
	}
}

func TestProvisionRefusesUnbootstrapped(t *testing.T) {
	r := newRig(t)
	withRecipes(r)
	seed(t, r, Metadata{Name: "demo", Host: "pve1", Status: StatusPlanned})
	if _, err := r.m.Provision(ctx, "demo", "go"); errs.KindOf(err) != errs.Refused || !strings.Contains(err.Error(), "not bootstrapped") {
		t.Fatalf("err = %v", err)
	}
}
