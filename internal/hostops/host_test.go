package hostops

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

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

func TestVolumeUsed(t *testing.T) {
	type step struct {
		argv []string
		resp runtest.Response
	}
	path := func(volid, out string) step {
		return step{remote("pvesm", "path", volid), runtest.Response{Stdout: out + "\n"}}
	}
	lvs := func(dev, out string) step {
		return step{remote("lvs", "--noheadings", "--units", "b", "--nosuffix", "-o", "lv_size,data_percent", "--", dev), runtest.Response{Stdout: out}}
	}
	stat := func(file, out string) step {
		return step{remote("stat", "-L", "-c", "%b:%B:%F", "--", file), runtest.Response{Stdout: out}}
	}
	cases := map[string]struct {
		volid string
		steps []step
		want  int64 // -1: an error is expected
	}{
		"thin LV": {"local-lvm:vm-200-disk-0", []step{
			path("local-lvm:vm-200-disk-0", "/dev/pve/vm-200-disk-0"),
			lvs("/dev/pve/vm-200-disk-0", "  103079215104 57.60\n"),
		}, 59373627900}, // 96 GiB at 57.60 %, rounded up
		"thin LV, comma decimal": {"local-lvm:vm-200-disk-0", []step{
			path("local-lvm:vm-200-disk-0", "/dev/pve/vm-200-disk-0"),
			lvs("/dev/pve/vm-200-disk-0", "  1073741824 50,00\n"),
		}, 1 << 29},
		"thick LV has no data percent": {"vg:vm-200-disk-0", []step{
			path("vg:vm-200-disk-0", "/dev/vg/vm-200-disk-0"),
			lvs("/dev/vg/vm-200-disk-0", "  1073741824 \n"),
		}, -1},
		"device that is not an LV": {"rbd:vm-200-disk-0", []step{
			path("rbd:vm-200-disk-0", "/dev/rbd/pool/vm-200-disk-0"),
			{remote("lvs"), runtest.Response{Code: 5, Stderr: "Failed to find logical volume"}},
		}, -1},
		"zvol": {"local-zfs:vm-200-disk-0", []step{
			path("local-zfs:vm-200-disk-0", "/dev/zvol/rpool/data/vm-200-disk-0"),
			{remote("zfs", "get", "-Hp", "-o", "value", "logicalreferenced", "rpool/data/vm-200-disk-0"), runtest.Response{Stdout: "21474836480\n"}},
		}, 20 << 30},
		"qcow2 file": {"local:200/vm-200-disk-0.qcow2", []step{
			path("local:200/vm-200-disk-0.qcow2", "/var/lib/vz/images/200/vm-200-disk-0.qcow2"),
			stat("/var/lib/vz/images/200/vm-200-disk-0.qcow2", "4194304:512:regular file\n"),
		}, 2 << 30},
		"directory (subvol)": {"local-zfs:subvol-105-disk-0", []step{
			path("local-zfs:subvol-105-disk-0", "/rpool/data/subvol-105-disk-0"),
			stat("/rpool/data/subvol-105-disk-0", "9:512:directory\n"),
		}, -1},
		"path that is not local": {"iscsi:0.0.0.scsi-example", []step{
			path("iscsi:0.0.0.scsi-example", "iscsi://192.0.2.10/iqn.2026-11.org.example:t/0"),
		}, -1},
		"pvesm fails": {"gone:vm-200-disk-0", []step{
			{remote("pvesm", "path"), runtest.Response{Code: 255, Stderr: "storage 'gone' does not exist"}},
		}, -1},
	}
	for name, c := range cases {
		h, f := newHost(t, false)
		var want []string
		for _, s := range c.steps {
			f.On(s.argv, s.resp)
			want = append(want, strings.Join(s.argv, " "))
		}
		n, err := h.VolumeUsed(context.Background(), c.volid)
		switch {
		case c.want < 0 && err == nil:
			t.Errorf("%s: used = %d, want an error", name, n)
		case c.want >= 0 && (err != nil || n != c.want):
			t.Errorf("%s: used = %d, %v; want %d", name, n, err, c.want)
		}
		// Each step ran once, in order; a prefix-only step matches any argv.
		if got := f.Commands(); len(got) != len(want) {
			t.Errorf("%s: commands = %q", name, got)
		}
	}
}
