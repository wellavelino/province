package bootstrap

import (
	"bytes"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/config"
)

func TestRecipePayloadIsAssignmentsThenScript(t *testing.T) {
	p, err := RecipePayload(RecipeParams{User: "dev", Hostname: "api", Recipe: config.Recipe{
		Packages: []string{"git", "tmux"},
		Tools:    map[string]string{"node": "22", "go": "1.24"},
		Dotfiles: "https://github.com/you/dotfiles",
		Run:      []string{"echo it's done", "make -C /srv setup"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "P_USER=dev\nP_HOSTNAME=api\nP_PACKAGES='git\ntmux'\nP_TOOLS='go@1.24\nnode@22'\n" +
		"P_DOTFILES=https://github.com/you/dotfiles\nP_RUN_N=2\nP_RUN_0='echo it'\\''s done'\nP_RUN_1='make -C /srv setup'\n"
	if !bytes.HasPrefix(p, []byte(want)) {
		t.Fatalf("payload head:\n%s\nwant:\n%s", p[:min(len(p), len(want))], want)
	}
	if !bytes.Equal(p[len(want):], recipeScript) {
		t.Fatal("payload tail is not the embedded recipe script")
	}
}

func TestRecipePayloadNeedsUserAndHostname(t *testing.T) {
	if _, err := RecipePayload(RecipeParams{Hostname: "api"}); err == nil {
		t.Fatal("no user accepted")
	}
}

// TestRecipeScriptRunsCommandsInOrder runs the real script with only run
// commands, as the current user, so nothing is installed.
func TestRecipeScriptRunsCommandsInOrder(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	payload, err := RecipePayload(RecipeParams{User: u.Username, Hostname: "api", Recipe: config.Recipe{
		Run: []string{`echo "first $P_HOSTNAME" >>` + out, `echo "second $P_USER" >>` + out},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	b, _ := os.ReadFile(out)
	if got, want := string(b), "first api\nsecond "+u.Username+"\n"; got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
	if !strings.Contains(stderr.String(), "province-recipe: done") {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestRecipeScriptStopsAtFailingCommand(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	payload, _ := RecipePayload(RecipeParams{User: u.Username, Hostname: "api", Recipe: config.Recipe{
		Run: []string{"false", "touch " + out},
	}})
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = bytes.NewReader(payload)
	if err := cmd.Run(); err == nil {
		t.Fatal("failing run command did not fail the script")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("commands after the failing one ran")
	}
}
