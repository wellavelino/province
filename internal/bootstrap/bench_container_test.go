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
		Run:      []string{`echo "$P_USER@$P_HOSTNAME" > /root/recipe-ran`},
	}})
	if err != nil {
		t.Fatal(err)
	}

	// The image pull is timed once and kept out of the runs: it is paid
	// once per host, not per env. It is only a cold pull when the image
	// was not already local; the output says which.
	cached := exec.Command(rt.bin, rt.inspect...).Run() == nil
	pull := timed(t, rt.bin, "pull", rt.pullArg)
	pullKind := "cold"
	if cached {
		pullKind = "image already local, so a registry check only"
	}

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
	fmt.Fprintf(&b, "image pull (once, outside the runs): %s (%s)\n\n", round(pull), pullKind)
	fmt.Fprintf(&b, "| phase | median | min | max |\n|---|---|---|---|\n")
	for _, p := range phases {
		s := slices.Clone(results[p])
		slices.Sort(s)
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", p, round(median(s)), round(s[0]), round(s[len(s)-1]))
	}
	fmt.Print(b.String())
}

// basePackages is the package step of bootstrap.sh's install_base plus
// the user a recipe runs as. The tailnet join and sshd start are left out:
// they need a key and an init system. The recipe gets no dotfiles, so it
// installs git itself, as it would on a fresh env.
const basePackages = `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q curl ca-certificates sudo openssh-server
useradd -m -s /bin/bash dev
`

// ready succeeds once the container has a shell and working DNS, which
// is when the bootstrap could start.
const ready = `getent hosts deb.debian.org >/dev/null`

type containerRuntime struct {
	bin     string
	version []string
	inspect []string // succeeds when the image is already local
	pullArg []string
	start   func(name string) []string
	exec    func(name string) []string
	rm      func(name string) []string
}

func ociRuntime(bin, image string) containerRuntime {
	return containerRuntime{
		bin:     bin,
		version: []string{"--version"},
		inspect: []string{"image", "inspect", image},
		pullArg: []string{"pull", image},
		start:   func(n string) []string { return []string{"run", "-d", "--name", n, image, "sleep", "infinity"} },
		exec:    func(n string) []string { return []string{"exec", "-i", n, "bash", "-s"} },
		rm:      func(n string) []string { return []string{"rm", "-f", n} },
	}
}

// Only docker has been run. The others follow each tool's documented CLI
// and are untested.
var runtimes = map[string]containerRuntime{
	"docker":    ociRuntime("docker", "debian:13"),
	"podman":    ociRuntime("podman", "docker.io/library/debian:13"),
	"container": appleContainer(),
	"incus": {
		bin:     "incus",
		version: []string{"version"},
		inspect: []string{"image", "info", "province-bench-debian13"},
		pullArg: []string{"image", "copy", "images:debian/13", "local:", "--alias", "province-bench-debian13"},
		start:   func(n string) []string { return []string{"launch", "province-bench-debian13", n} },
		exec:    func(n string) []string { return []string{"exec", n, "--", "bash", "-s"} },
		rm:      func(n string) []string { return []string{"delete", "-f", n} },
	},
}

// appleContainer is Apple's container CLI: OCI-like, but images and
// deletion live under their own subcommands.
func appleContainer() containerRuntime {
	r := ociRuntime("container", "docker.io/library/debian:13")
	r.pullArg = []string{"image", "pull", "docker.io/library/debian:13"}
	r.rm = func(n string) []string { return []string{"delete", "--force", n} }
	return r
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
		out, err := cmd.CombinedOutput()
		if err == nil {
			return time.Since(t0)
		}
		if time.Since(t0) > 2*time.Minute {
			t.Fatalf("start: not ready after 2 minutes; last check: %v: %s", err, out)
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
