package hostops

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/run/runtest"
	"github.com/wellavelino/province/internal/sshx"
)

var sshPrefix = []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=30", "--", "pve1"}

func remote(words ...string) []string { return append(slices.Clone(sshPrefix), words...) }

func newHost(t *testing.T, sudo bool) (*SSHHost, *runtest.Fake) {
	f := &runtest.Fake{T: t}
	return &SSHHost{Runner: f, Target: sshx.Target{Dest: "pve1", Sudo: sudo}}, f
}

func TestExecSendsStdinToBashInGuest(t *testing.T) {
	h, f := newHost(t, false)
	f.On(remote("pct", "exec", "105", "--", "bash", "-s"), runtest.Response{Stdout: "PROVINCE_TS_IP=100.64.0.1\n"})
	var out bytes.Buffer
	if err := h.Exec(context.Background(), 105, strings.NewReader("payload"), &out, "bash", "-s"); err != nil {
		t.Fatal(err)
	}
	c := f.Calls()[0]
	if c.Stdin != "payload" || out.String() != "PROVINCE_TS_IP=100.64.0.1\n" {
		t.Fatalf("stdin=%q out=%q", c.Stdin, out.String())
	}
}

// The guest's stderr is kept out of the message and returned as guest
// output; a host command's stderr stays in its message.
func TestExecFailureKeepsGuestStderrApart(t *testing.T) {
	h, f := newHost(t, false)
	f.On(remote("pct", "exec", "105", "--", "bash", "-s"), runtest.Response{Code: 3, Stderr: "recipe step failed\n"})
	f.On(remote("pct", "start", "105"), runtest.Response{Code: 1, Stderr: "CT 105 already running"})
	err := h.Exec(context.Background(), 105, strings.NewReader("x"), nil, "bash", "-s")
	if err == nil || !strings.Contains(err.Error(), "pct exec 105 on pve1") || strings.Contains(err.Error(), "recipe step failed") {
		t.Fatalf("err = %v", err)
	}
	if got := errs.GuestOutput(err); got != "recipe step failed" {
		t.Fatalf("guest output = %q", got)
	}
	err = h.Start(context.Background(), "lxc", 105)
	if err == nil || !strings.Contains(err.Error(), "already running") || errs.GuestOutput(err) != "" {
		t.Fatalf("host command: err = %v, guest output = %q", err, errs.GuestOutput(err))
	}
}

func TestGuestVerbsForBothTypes(t *testing.T) {
	h, f := newHost(t, false)
	f.On(sshPrefix, runtest.Response{})
	ctx := context.Background()
	_ = h.Set(ctx, "lxc", 105, "--dev0", "/dev/net/tun")
	_ = h.Set(ctx, "qemu", 200, "--onboot", "0")
	_ = h.Start(ctx, "lxc", 105)
	_ = h.Stop(ctx, "qemu", 200)
	_ = h.Reboot(ctx, "lxc", 105)
	_ = h.Restore(ctx, "lxc", 105, "/var/lib/vz/dump/a.tar.zst", "local-lvm")
	_ = h.Restore(ctx, "qemu", 200, "/var/lib/vz/dump/b.vma.zst", "local-lvm")
	_ = h.ZstdTest(ctx, "/var/lib/vz/dump/a.tar.zst")
	_ = h.Remove(ctx, "/var/lib/vz/dump/a.tar.zst")
	_ = h.Destroy(ctx, "lxc", 105)
	_ = h.Destroy(ctx, "qemu", 200)
	p := strings.Join(sshPrefix, " ") + " "
	want := []string{
		p + "pct set 105 --dev0 /dev/net/tun",
		p + "qm set 200 --onboot 0",
		p + "pct start 105",
		p + "qm stop 200",
		p + "pct reboot 105",
		p + "pct restore 105 /var/lib/vz/dump/a.tar.zst --storage local-lvm",
		p + "qmrestore /var/lib/vz/dump/b.vma.zst 200 --storage local-lvm",
		p + "zstd -q -t /var/lib/vz/dump/a.tar.zst",
		p + "rm -f -- /var/lib/vz/dump/a.tar.zst",
		p + "pct destroy 105 --purge",
		p + "qm destroy 200 --purge",
	}
	if got := f.Commands(); !slices.Equal(got, want) {
		t.Fatalf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if err := h.Set(ctx, "vm", 1); err == nil {
		t.Fatal("unknown type accepted")
	}
}

func TestShutdownVerifiesStopped(t *testing.T) {
	h, f := newHost(t, false)
	f.On(remote("pct", "shutdown", "105", "--timeout", "60"), runtest.Response{})
	f.On(remote("pct", "status", "105"), runtest.Response{Stdout: "status: stopped\n"})
	if err := h.Shutdown(context.Background(), "lxc", 105, 60); err != nil {
		t.Fatal(err)
	}
	if n := len(f.Calls()); n != 2 {
		t.Fatalf("calls = %v", f.Commands())
	}

	h, f = newHost(t, false)
	f.On(remote("qm", "shutdown", "200", "--timeout", "60"), runtest.Response{})
	f.On(remote("qm", "status", "200"), runtest.Response{Stdout: "status: running\n"})
	if err := h.Shutdown(context.Background(), "qemu", 200, 60); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("still running: err = %v", err)
	}

	h, f = newHost(t, false)
	f.On(remote("pct", "shutdown"), runtest.Response{Code: 255, Stderr: "timeout"})
	if err := h.Shutdown(context.Background(), "lxc", 105, 60); err == nil {
		t.Fatal("failed shutdown not reported")
	}
}

func TestDumpAndReceiveArgv(t *testing.T) {
	h, _ := newHost(t, false)
	if got, want := h.DumpCmd("lxc", 105).Argv(), remote("vzdump", "105", "--stdout", "--mode", "stop", "--compress", "zstd"); !slices.Equal(got, want) {
		t.Fatalf("dump argv = %q", got)
	}
	got := h.ReceiveCmd("/var/lib/vz/dump/vzdump-lxc-105-2026_11_08-10_15_00.tar.zst").Argv()
	want := remote("sh", "-c", "'umask 077; cat > /var/lib/vz/dump/vzdump-lxc-105-2026_11_08-10_15_00.tar.zst'")
	if !slices.Equal(got, want) {
		t.Fatalf("receive argv = %q\nwant %q", got, want)
	}
	// A path that needs quoting is quoted inside the script, then the script again.
	got = h.ReceiveCmd("/mnt/my dump/x").Argv()
	if last := got[len(got)-1]; last != `'umask 077; cat > '\''/mnt/my dump/x'\'''` {
		t.Fatalf("quoted receive script = %s", last)
	}
}

func TestDfAvail(t *testing.T) {
	h, f := newHost(t, false)
	f.On(remote("df", "-P", "-B1", "/var/lib/vz/dump"), runtest.Response{Stdout: "Filesystem 1-blocks Used Available Capacity Mounted on\n/dev/mapper/pve-root 100000 40000 60000 40% /\n"})
	n, err := h.DfAvail(context.Background(), "/var/lib/vz/dump")
	if err != nil || n != 60000 {
		t.Fatalf("avail = %d, %v", n, err)
	}
}

func TestSudoPrefixPropagates(t *testing.T) {
	h, f := newHost(t, true)
	f.On(sshPrefix, runtest.Response{Stdout: "status: running\n"})
	ctx := context.Background()
	_, _ = h.Status(ctx, "lxc", 105)
	_ = h.Exec(ctx, 105, nil, nil, "tailscale", "logout")
	for _, argv := range [][]string{h.DumpCmd("lxc", 105).Argv(), h.ReceiveCmd("/x").Argv()} {
		if !slices.Equal(argv[len(sshPrefix):len(sshPrefix)+2], []string{"sudo", "-n"}) {
			t.Errorf("no sudo: %q", argv)
		}
	}
	for _, c := range f.Calls() {
		if !slices.Equal(c.Argv[len(sshPrefix):len(sshPrefix)+2], []string{"sudo", "-n"}) {
			t.Errorf("no sudo: %s", c)
		}
	}
}
