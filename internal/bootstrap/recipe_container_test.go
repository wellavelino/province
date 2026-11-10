//go:build integration

package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/config"
)

// TestRecipeInDebianContainer runs the real recipe script in a real Debian
// 13 container: real apt, real mise, a real git clone. It needs Docker and
// network access, so it only builds with -tags integration (CI runs it).
//
//	go test -tags integration -run TestRecipeInDebianContainer -v ./internal/bootstrap/
func TestRecipeInDebianContainer(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("the integration test needs docker on PATH")
	}
	name := fmt.Sprintf("province-recipe-test-%d", os.Getpid())
	if out, err := exec.Command("docker", "run", "-d", "--name", name, "debian:13", "sleep", "infinity").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })

	// sh pipes script into bash -s as root in the container, the way
	// `pct exec <vmid> -- bash -s` and `incus exec <name> -- bash -s` do.
	sh := func(script []byte) (string, error) {
		cmd := exec.Command("docker", "exec", "-i", name, "bash", "-s")
		cmd.Stdin = bytes.NewReader(script)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(what string, script []byte) string {
		t.Helper()
		out, err := sh(script)
		if err != nil {
			t.Fatalf("%s: %v\n%s", what, err, out)
		}
		return out
	}

	// What the bootstrap leaves behind that a recipe relies on: the base
	// packages and the user. Plus a dotfiles repo to clone, with an
	// install.sh that records each run.
	must("prepare", []byte(`set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q curl ca-certificates sudo git
useradd -m -s /bin/bash dev
sudo -u dev -H bash -euc '
  mkdir "$HOME/dotfiles-src" && cd "$HOME/dotfiles-src"
  git init -q -b main
  printf "%s\n" "#!/bin/sh" "echo ran >> \"\$HOME/.dotfiles-installed\"" > install.sh
  git add install.sh
  git -c user.name=test -c user.email=test@example.com commit -qm init
'
`))

	payload, err := RecipePayload(RecipeParams{User: "dev", Hostname: "api", Recipe: config.Recipe{
		Packages: []string{"jq"},
		Tools:    map[string]string{"go": "1.24"},
		Dotfiles: "file:///home/dev/dotfiles-src",
		Run:      []string{`echo "$P_USER@$P_HOSTNAME" > /root/recipe-ran`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first run:\n%s", must("first recipe run", payload))
	// env provision relies on a second run being safe.
	t.Logf("second run:\n%s", must("second recipe run", payload))

	checks := []struct{ what, script, want string }{
		{"package installed", `dpkg-query -W -f='${Status}' jq`, "install ok installed"},
		// What sshd does for `ssh dev@api 'go version'`: a non-interactive,
		// non-login bash with SSH_CLIENT set and nothing else.
		{"tool on PATH over ssh", `sudo -u dev env -i HOME=/home/dev USER=dev SSH_CLIENT='192.0.2.1 1 22' /bin/bash -c 'go version'`, "go1.24"},
		{"tool on PATH in a login shell", `sudo -u dev -i go version`, "go1.24"},
		{"tools belong to the user", `stat -c %U /home/dev/.local/share/mise`, "dev"},
		{"shims line added once", `grep -c 'province: mise tools' /home/dev/.bashrc`, "1"},
		{"dotfiles cloned as the user", `stat -c %U /home/dev/.dotfiles/.git`, "dev"},
		{"install.sh ran on each run", `wc -l < /home/dev/.dotfiles-installed`, "2"},
		{"run command saw its variables", `cat /root/recipe-ran`, "dev@api"},
	}
	for _, c := range checks {
		out, err := sh([]byte("set -euo pipefail\n" + c.script + "\n"))
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%s: want %q, got %q (err %v)", c.what, c.want, strings.TrimSpace(out), err)
		}
	}
}
