//go:build bench

package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wellavelino/province/internal/config"
)

// TestBenchContainerSpinup times how long a Debian 13 container takes to
// become a usable env on a local container runtime, phase by phase: start
// until a shell answers and DNS resolves, the base packages the bootstrap
// installs, the recipe (first run and the idempotent second run), and
// destroy. It is the same path as TestRecipeInDebianContainer, timed.
//
// It is a baseline for the runtime, not a province number: province has no
// Docker or Apple backend, and the tofu apply and the tailnet join that a
// real `province env create --apply` does are not in it.
//
//	go test -tags bench -run TestBenchContainerSpinup -v -timeout 60m ./internal/bootstrap/
//
// PROVINCE_BENCH_RUNTIME picks the runtime: docker (default), podman,
// container (Apple's, macOS) or incus. PROVINCE_BENCH_RUNS sets the number
// of runs (default 5).
func TestBenchContainerSpinup(t *testing.T) {
	rtName := envOr("PROVINCE_BENCH_RUNTIME", "docker")
	rt, ok := runtimes[rtName]
	if !ok {
		t.Fatalf("PROVINCE_BENCH_RUNTIME=%s: want one of docker, podman, container, incus", rtName)
	}
	runs, err := strconv.Atoi(envOr("PROVINCE_BENCH_RUNS", "5"))
	if err != nil || runs < 1 {
		t.Fatalf("PROVINCE_BENCH_RUNS=%q: want a positive integer", os.Getenv("PROVINCE_BENCH_RUNS"))
	}
	if _, err := exec.LookPath(rt.bin); err != nil {
		t.Fatalf("%s is not on PATH", rt.bin)
	}

	payload, err := RecipePayload(RecipeParams{User: "dev", Hostname: "bench", Recipe: config.Recipe{
		Packages: []string{"jq"},
		Tools:    map[string]string{"go": "1.24"},
		Dotfiles: "file:///home/dev/dotfiles-src",
		Run:      []string{`echo "$P_USER@$P_HOSTNAME" > /root/recipe-ran`},
	}})
	if err != nil {
		t.Fatal(err)
	}

	// The image pull is timed once and kept out of the runs: it is a cold
	// cache cost, paid once per host, not per env.
	pull := timed(t, rt.bin, "pull", rt.pull())

	phases := []string{"start", "base packages", "recipe (1st)", "recipe (2nd)", "destroy", "total"}
	results := map[string][]time.Duration{}
	for i := range runs {
		name := fmt.Sprintf("province-bench-%d-%d", os.Getpid(), i)
		t.Cleanup(func() { exec.Command(rt.bin, rt.rm(name)...).Run() })

		var d [5]time.Duration
		d[0] = startAndWait(t, rt, name)
		d[1] = timedStdin(t, rt.bin, "base packages", rt.exec(name), []byte(basePackages))
		d[2] = timedStdin(t, rt.bin, "recipe (1st)", rt.exec(name), payload)
		d[3] = timedStdin(t, rt.bin, "recipe (2nd)", rt.exec(name), payload)
		d[4] = timed(t, rt.bin, "destroy", rt.rm(name))
		total := d[0] + d[1] + d[2] + d[3] + d[4]
		for j, p := range phases[:5] {
			results[p] = append(results[p], d[j])
		}
		results["total"] = append(results["total"], total)
		t.Logf("run %d/%d: total %s", i+1, runs, round(total))
	}

	ver, _ := exec.Command(rt.bin, rt.version...).CombinedOutput()
	var b strings.Builder
	fmt.Fprintf(&b, "\nruntime: %s (%s), %s/%s, %d CPUs, %d runs\n", rtName,
		strings.TrimSpace(firstLine(string(ver))), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runs)
	fmt.Fprintf(&b, "image pull (once, outside the runs): %s\n\n", round(pull))
	fmt.Fprintf(&b, "| phase | median | min | max |\n|---|---|---|---|\n")
	for _, p := range phases {
		s := slices.Clone(results[p])
		slices.Sort(s)
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", p, round(median(s)), round(s[0]), round(s[len(s)-1]))
	}
	fmt.Print(b.String())
}

// basePackages is what the bootstrap leaves behind that a recipe relies on
// (the base packages and the user), plus a local dotfiles repo to clone.
const basePackages = `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q curl ca-certificates sudo git
useradd -m -s /bin/bash dev
sudo -u dev -H bash -euc '
  mkdir "$HOME/dotfiles-src" && cd "$HOME/dotfiles-src"
  git init -q -b main
  printf "%s\n" "#!/bin/sh" "true" > install.sh
  git add install.sh
  git -c user.name=bench -c user.email=bench@example.com commit -qm init
'
`

// ready succeeds once the container has a shell and working DNS, which
// is when the bootstrap could start.
const ready = `getent hosts deb.debian.org >/dev/null`

type containerRuntime struct {
	bin     string
	version []string
	pullArg []string
	startFn func(name string) []string
	execFn  func(name string) []string
	rmFn    func(name string) []string
}

func (r containerRuntime) pull() []string             { return r.pullArg }
func (r containerRuntime) start(name string) []string { return r.startFn(name) }
func (r containerRuntime) exec(name string) []string  { return r.execFn(name) }
func (r containerRuntime) rm(name string) []string    { return r.rmFn(name) }

func ociRuntime(bin, image string) containerRuntime {
	return containerRuntime{
		bin:     bin,
		version: []string{"--version"},
		pullArg: []string{"pull", image},
		startFn: func(n string) []string { return []string{"run", "-d", "--name", n, image, "sleep", "infinity"} },
		execFn:  func(n string) []string { return []string{"exec", "-i", n, "bash", "-s"} },
		rmFn:    func(n string) []string { return []string{"rm", "-f", n} },
	}
}

// Only docker has been run. The others follow each tool's documented CLI
// and are untested.
var runtimes = map[string]containerRuntime{
	"docker": ociRuntime("docker", "debian:13"),
	"podman": ociRuntime("podman", "docker.io/library/debian:13"),
	"container": {
		bin:     "container",
		version: []string{"--version"},
		pullArg: []string{"image", "pull", "docker.io/library/debian:13"},
		startFn: func(n string) []string {
			return []string{"run", "-d", "--name", n, "docker.io/library/debian:13", "sleep", "infinity"}
		},
		execFn: func(n string) []string { return []string{"exec", "-i", n, "bash", "-s"} },
		rmFn:   func(n string) []string { return []string{"delete", "--force", n} },
	},
	"incus": {
		bin:     "incus",
		version: []string{"version"},
		pullArg: []string{"image", "copy", "images:debian/13", "local:", "--alias", "province-bench-debian13"},
		startFn: func(n string) []string { return []string{"launch", "province-bench-debian13", n} },
		execFn:  func(n string) []string { return []string{"exec", n, "--", "bash", "-s"} },
		rmFn:    func(n string) []string { return []string{"delete", "-f", n} },
	},
}

// startAndWait starts the container and waits, up to 2 minutes, until it
// is ready. The time includes the wait.
func startAndWait(t *testing.T, rt containerRuntime, name string) time.Duration {
	t.Helper()
	t0 := time.Now()
	if out, err := exec.Command(rt.bin, rt.start(name)...).CombinedOutput(); err != nil {
		t.Fatalf("start: %v: %s", err, out)
	}
	for {
		cmd := exec.Command(rt.bin, rt.exec(name)...)
		cmd.Stdin = strings.NewReader(ready)
		if cmd.Run() == nil {
			return time.Since(t0)
		}
		if time.Since(t0) > 2*time.Minute {
			t.Fatal("start: not ready after 2 minutes")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func timed(t *testing.T, bin, what string, args []string) time.Duration {
	t.Helper()
	return timedStdin(t, bin, what, args, nil)
}

func timedStdin(t *testing.T, bin, what string, args []string, stdin []byte) time.Duration {
	t.Helper()
	cmd := exec.Command(bin, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	t0 := time.Now()
	out, err := cmd.CombinedOutput()
	d := time.Since(t0)
	if err != nil {
		t.Fatalf("%s: %v\n%s", what, err, out)
	}
	return d
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func median(s []time.Duration) time.Duration {
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func round(d time.Duration) time.Duration { return d.Round(10 * time.Millisecond) }

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }
