package bootstrap

import (
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/sshx"
)

//go:embed recipe.sh
var recipeScript []byte

// RecipeScript returns the embedded recipe script.
func RecipeScript() []byte { return append([]byte(nil), recipeScript...) }

// RecipeParams configure one recipe run.
type RecipeParams struct {
	User     string
	Hostname string
	Recipe   config.Recipe // resolved: includes already merged
}

// Tools returns the recipe's tools as sorted name@version words.
func Tools(r config.Recipe) []string {
	var out []string
	for t, v := range r.Tools {
		out = append(out, t+"@"+v)
	}
	slices.Sort(out)
	return out
}

// RecipePayload renders the recipe's assignments followed by the recipe
// script, for `bash -s` on stdin. No secret goes into it.
func RecipePayload(p RecipeParams) ([]byte, error) {
	if p.User == "" || p.Hostname == "" {
		return nil, errors.New("recipe: user and hostname are required")
	}
	kvs := [][2]string{
		{"P_USER", p.User},
		{"P_HOSTNAME", p.Hostname},
		{"P_PACKAGES", strings.Join(p.Recipe.Packages, "\n")},
		{"P_TOOLS", strings.Join(Tools(p.Recipe), "\n")},
		{"P_DOTFILES", p.Recipe.Dotfiles},
		{"P_RUN_N", strconv.Itoa(len(p.Recipe.Run))},
	}
	for i, c := range p.Recipe.Run {
		kvs = append(kvs, [2]string{"P_RUN_" + strconv.Itoa(i), c})
	}
	var b strings.Builder
	for _, kv := range kvs {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], sshx.Quote(kv[1]))
	}
	return append([]byte(b.String()), recipeScript...), nil
}
