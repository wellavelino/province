package move

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/hostops"
	"github.com/wellavelino/province/internal/hostops/hostopstest"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/proxmox/proxmoxtest"
	"github.com/wellavelino/province/internal/run/runtest"
)

var (
	ctx      = context.Background()
	fixedNow = time.Date(2026, 11, 8, 10, 15, 0, 0, time.UTC)
	dump     = "/var/lib/vz/dump/vzdump-lxc-105-2026_11_08-10_15_00.tar.zst"
)

func TestDumpName(t *testing.T) {
	if got := DumpName("lxc", 105, fixedNow); got != "vzdump-lxc-105-2026_11_08-10_15_00.tar.zst" {
		t.Fatalf("lxc: %s", got)
	}
	if got := DumpName("qemu", 200, fixedNow); got != "vzdump-qemu-200-2026_11_08-10_15_00.vma.zst" {
		t.Fatalf("qemu: %s", got)
	}
}

type rig struct {
	m      *Mover
	api    map[string]*proxmoxtest.Fake
	hosts  map[string]*hostopstest.Fake
	runner *runtest.Fake
	j      *hostopstest.Journal
}

func newRig(t *testing.T) *rig {
	t.Helper()
	cfg := &config.Config{Hosts: []config.Host{
		{Name: "pve1", Node: "pve1", SSH: "pve1", DumpDir: "/var/lib/vz/dump"},
		{Name: "pve2", Node: "pve2", SSH: "pve2", DumpDir: "/var/lib/vz/dump"},
	}}
	j := &hostopstest.Journal{}
	r := &rig{
		j: j,
		api: map[string]*proxmoxtest.Fake{
			"pve1": {
				Guests: []proxmox.Guest{{VMID: 105, Name: "demo", Status: "running", Type: "lxc"}},
				Configs: map[int]map[string]string{105: {
					"rootfs": "local-lvm:vm-105-disk-0,size=8G",
					"net0":   "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:00:00:01,ip=dhcp,type=veth",
					"onboot": "1",
				}},
			},
			"pve2": {
				Guests:    []proxmox.Guest{{VMID: 300, Name: "other", Status: "running", Type: "qemu"}},
				BridgeLs:  []string{"vmbr0", "vmbr1"},
				StorageLs: []proxmox.Storage{{Name: "local-lvm", Content: []string{"rootdir", "images"}, Avail: 100 << 30, Active: true}},
			},
		},
		hosts: map[string]*hostopstest.Fake{
			"pve1": {Name: "pve1", Journal: j, State: "running"},
			"pve2": {Name: "pve2", Journal: j, State: "stopped", Avail: map[string]int64{"/var/lib/vz/dump": 50 << 30}},
		},
		runner: &runtest.Fake{T: t, OnCall: func(c runtest.Call) { j.Add("run " + c.String()) }},
	}
	r.runner.On([]string{"dump"}, runtest.Response{Stdout: "archive-bytes"})
	r.runner.On([]string{"receive"}, runtest.Response{})
	r.m = &Mover{
		Cfg:    cfg,
		Client: func(h config.Host) (proxmox.Client, error) { return r.api[h.Name], nil },
		Host:   func(h config.Host) hostops.Host { return r.hosts[h.Name] },
		Runner: r.runner,
		Now:    func() time.Time { return fixedNow },
	}
	return r
}

func opts() Opts { return Opts{VMID: 105, From: "pve1", To: "pve2", Yes: true} }

var preflightReads = []string{"pve1 Status lxc 105", "pve2 DfAvail /var/lib/vz/dump"}

func happyOrder(start bool) []string {
	o := append(slices.Clone(preflightReads),
		"pve1 Shutdown lxc 105 120",
		"run dump pve1 lxc 105",
		"run receive pve2 "+dump,
		"pve2 ZstdTest "+dump,
		"pve2 Restore lxc 105 "+dump+" local-lvm",
		"pve1 Set lxc 105 --onboot 0",
		"pve2 Remove "+dump,
	)
	if start {
		o = append(o, "pve2 Start lxc 105")
	}
	return o
}

func assertJournal(t *testing.T, r *rig, want []string) {
	t.Helper()
	if got := r.j.Entries(); !slices.Equal(got, want) {
		t.Fatalf("journal:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestMoveHappyOrderWithoutStart(t *testing.T) {
	r := newRig(t)
	res, err := r.m.Move(ctx, opts())
	if err != nil {
		t.Fatal(err)
	}
	assertJournal(t, r, happyOrder(false))
	if res.Target != "stopped" || !res.DumpRemoved || res.Storage != "local-lvm" || !strings.Contains(res.Source, "onboot=0") {
		t.Fatalf("result = %+v", res)
	}
	// The dump bytes went from the source command into the receiver.
	if c := r.runner.Calls(); c[1].Stdin != "archive-bytes" {
		t.Fatalf("receiver stdin = %q", c[1].Stdin)
	}
}

func TestMoveStartsTargetOnlyAfterOnboot0(t *testing.T) {
	r := newRig(t)
	res, err := r.m.Move(ctx, Opts{VMID: 105, From: "pve1", To: "pve2", Yes: true, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	assertJournal(t, r, happyOrder(true))
	if res.Target != "running" {
		t.Fatalf("result = %+v", res)
	}
}

func TestMoveAlreadyStoppedSkipsShutdown(t *testing.T) {
	r := newRig(t)
	r.api["pve1"].Guests[0].Status = "stopped"
	if _, err := r.m.Move(ctx, opts()); err != nil {
		t.Fatal(err)
	}
	want := slices.DeleteFunc(happyOrder(false), func(s string) bool { return strings.Contains(s, "Shutdown") })
	assertJournal(t, r, want)
}

func TestMovePreflightRefusalsChangeNothing(t *testing.T) {
	cases := map[string]struct {
		mut   func(r *rig, o *Opts)
		kind  errs.Kind
		want  string
		reads []string // journal entries allowed before the refusal
	}{
		"no --yes":                {func(r *rig, o *Opts) { o.Yes = false }, errs.Refused, "--yes", nil},
		"same host":               {func(r *rig, o *Opts) { o.To = "pve1" }, errs.Refused, "same host", nil},
		"unknown host":            {func(r *rig, o *Opts) { o.To = "pve9" }, errs.Refused, "pve9", nil},
		"source missing":          {func(r *rig, o *Opts) { o.VMID = 999 }, errs.Preflight, "not found on pve1", nil},
		"vmid on target":          {func(r *rig, o *Opts) { r.api["pve2"].Guests[0].VMID = 105 }, errs.Preflight, "already exists on pve2", nil},
		"paused source":           {func(r *rig, o *Opts) { r.api["pve1"].Guests[0].Status = "paused" }, errs.Preflight, "paused", nil},
		"locked source":           {func(r *rig, o *Opts) { r.api["pve1"].Configs[105]["lock"] = "backup" }, errs.Preflight, "locked", nil},
		"bridge missing":          {func(r *rig, o *Opts) { r.api["pve2"].BridgeLs = []string{"vmbr1"} }, errs.Preflight, "bridge vmbr0", nil},
		"bridges hidden":          {func(r *rig, o *Opts) { r.api["pve2"].BridgeLs = nil }, errs.Preflight, "SDN.Audit", nil},
		"storage absent":          {func(r *rig, o *Opts) { r.api["pve2"].StorageLs[0].Name = "zfs" }, errs.Preflight, "--storage", nil},
		"explicit storage absent": {func(r *rig, o *Opts) { o.Storage = "ceph" }, errs.Preflight, "storage ceph not found", nil},
		"storage wrong content":   {func(r *rig, o *Opts) { r.api["pve2"].StorageLs[0].Content = []string{"images"} }, errs.Preflight, "rootdir", nil},
		"storage full":            {func(r *rig, o *Opts) { r.api["pve2"].StorageLs[0].Avail = 4 << 30 }, errs.Preflight, "target storage local-lvm", nil},
		"dump dir full":           {func(r *rig, o *Opts) { r.hosts["pve2"].Avail["/var/lib/vz/dump"] = 1 << 30 }, errs.Preflight, "dump dir", preflightReads},
		"source ssh down": {func(r *rig, o *Opts) {
			r.hosts["pve1"].Fail = func(string) error { return errors.New("ssh: connect to host pve1 port 22: Connection refused") }
		}, errs.Preflight, "Connection refused", preflightReads[:1]},
		"api down": {func(r *rig, o *Opts) { r.api["pve1"].Err = errors.New("dial tcp: i/o timeout") }, errs.Preflight, "timeout", nil},
	}
	for name, c := range cases {
		r := newRig(t)
		o := opts()
		c.mut(r, &o)
		_, err := r.m.Move(ctx, o)
		if errs.KindOf(err) != c.kind || errs.ExitCode(err) != 2 || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v (kind %s)", name, err, errs.KindOf(err))
		}
		if got := r.j.Entries(); !slices.Equal(got, c.reads) {
			t.Errorf("%s: journal = %v, want only %v", name, got, c.reads)
		}
	}
}

func TestMoveDefaultsStorageToSourceRootfs(t *testing.T) {
	r := newRig(t)
	r.api["pve2"].StorageLs = append(r.api["pve2"].StorageLs, proxmox.Storage{Name: "ssd", Content: []string{"rootdir"}, Avail: 100 << 30, Active: true})
	o := opts()
	o.Storage = "ssd"
	res, err := r.m.Move(ctx, o)
	if err != nil || res.Storage != "ssd" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if !slices.Contains(r.j.Entries(), "pve2 Restore lxc 105 "+dump+" ssd") {
		t.Fatalf("journal = %v", r.j.Entries())
	}
}

func TestMoveHostBoundKeysAreWarnings(t *testing.T) {
	r := newRig(t)
	r.api["pve1"].Configs[105]["dev0"] = "/dev/net/tun"
	r.api["pve1"].Configs[105]["mp0"] = "/srv/data,mp=/data"
	res, err := r.m.Move(ctx, opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 2 || !strings.HasPrefix(res.Warnings[0], "dev0") || !strings.HasPrefix(res.Warnings[1], "mp0") {
		t.Fatalf("warnings = %q", res.Warnings)
	}
}

func TestMovePipeFailureCleansUpAndExplains(t *testing.T) {
	r := newRig(t)
	r.runner = &runtest.Fake{T: t, OnCall: func(c runtest.Call) { r.j.Add("run " + c.String()) }}
	r.runner.On([]string{"dump"}, runtest.Response{Stdout: "partial", Code: 255, Stderr: "Connection reset"})
	r.runner.On([]string{"receive"}, runtest.Response{})
	r.m.Runner = r.runner
	_, err := r.m.Move(ctx, opts())
	if err == nil || errs.ExitCode(err) != 1 || !strings.Contains(err.Error(), "`pct start 105` on pve1") {
		t.Fatalf("err = %v", err)
	}
	want := append(slices.Clone(preflightReads),
		"pve1 Shutdown lxc 105 120", "run dump pve1 lxc 105", "run receive pve2 "+dump, "pve2 Remove "+dump)
	assertJournal(t, r, want)
}

func TestMoveCorruptArchiveCleansUp(t *testing.T) {
	r := newRig(t)
	r.hosts["pve2"].Fail = func(c string) error {
		if strings.Contains(c, "ZstdTest") {
			return errors.New("zstd: corrupted block")
		}
		return nil
	}
	_, err := r.m.Move(ctx, opts())
	if err == nil || !strings.Contains(err.Error(), "pct start 105") {
		t.Fatalf("err = %v", err)
	}
	e := r.j.Entries()
	if e[len(e)-1] != "pve2 Remove "+dump || slices.ContainsFunc(e, func(s string) bool { return strings.Contains(s, "Restore") }) {
		t.Fatalf("journal = %v", e)
	}
}

func TestMoveDirStorageSharingDumpFilesystemNeedsTwiceTheDisks(t *testing.T) {
	// 12 GiB guest, 20 GiB free on a dir storage: the dump (up to 12 GiB) and
	// the restored disks (12 GiB) would not both fit on the shared filesystem.
	r := newRig(t)
	r.api["pve1"].Configs[105]["rootfs"] = "local:105/vm-105-disk-0.raw,size=12G"
	r.api["pve2"].StorageLs = []proxmox.Storage{{Name: "local", Type: "dir", Content: []string{"rootdir", "backup"}, Avail: 20 << 30, Active: true}}
	_, err := r.m.Move(ctx, opts())
	if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "(dir)") || !strings.Contains(err.Error(), "needs 24.0 GiB (dump: allocated 12.0 GiB; plus restored disks: allocated 12.0 GiB)") {
		t.Fatalf("err = %v", err)
	}
	assertJournal(t, r, preflightReads) // reads only, nothing changed

	// zfspool shares the pool with the root dataset; any storage whose free
	// bytes equal dump_dir's shares its filesystem.
	for typ, avail := range map[string]int64{"zfspool": 30 << 30, "nfs": 20 << 30} {
		r = newRig(t)
		r.api["pve1"].Configs[105]["rootfs"] = "local-zfs:subvol-105-disk-0,size=12G"
		r.api["pve2"].StorageLs = []proxmox.Storage{{Name: "local-zfs", Type: typ, Content: []string{"rootdir"}, Avail: 20 << 30, Active: true}}
		r.hosts["pve2"].Avail["/var/lib/vz/dump"] = avail
		_, err = r.m.Move(ctx, opts())
		if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "shares it with the dump") {
			t.Errorf("%s: err = %v", typ, err)
		}
	}

	// The same free space is enough on a non-dir storage, and 24 GiB is enough on dir.
	r = newRig(t)
	r.api["pve1"].Configs[105]["rootfs"] = "local:105/vm-105-disk-0.raw,size=12G"
	r.api["pve2"].StorageLs = []proxmox.Storage{{Name: "local", Type: "dir", Content: []string{"rootdir"}, Avail: 24 << 30, Active: true}}
	if _, err := r.m.Move(ctx, opts()); err != nil {
		t.Fatalf("24 GiB dir: %v", err)
	}
	r = newRig(t)
	r.api["pve1"].Configs[105]["rootfs"] = "local-lvm:vm-105-disk-0,size=12G"
	r.api["pve2"].StorageLs[0].Type, r.api["pve2"].StorageLs[0].Avail = "lvmthin", 20<<30
	if _, err := r.m.Move(ctx, opts()); err != nil {
		t.Fatalf("20 GiB lvmthin: %v", err)
	}
}

func TestMoveDumpDirEstimatedFromUsedBytesOfRunningContainer(t *testing.T) {
	// 96 GiB allocated, 7 GiB used, 50 GiB free in dump_dir: a zstd dump
	// holds only used data, so the move goes ahead (7*1.1+1 = 8.7 GiB).
	r := newRig(t)
	r.api["pve1"].Configs[105]["rootfs"] = "local-lvm:vm-105-disk-0,size=96G"
	r.api["pve2"].StorageLs[0].Avail = 100 << 30
	r.api["pve1"].DiskUsed = map[int]int64{105: 7 << 30}
	if _, err := r.m.Move(ctx, opts()); err != nil {
		t.Fatalf("used 7 GiB: %v", err)
	}
	if !slices.Contains(r.api["pve1"].Calls(), "GuestDiskUsed pve1 lxc 105") {
		t.Fatalf("api calls = %v", r.api["pve1"].Calls())
	}

	// The estimate, not the used bytes alone, must fit: 8 GiB free is too little.
	r = newRig(t)
	r.api["pve1"].Configs[105]["rootfs"] = "local-lvm:vm-105-disk-0,size=96G"
	r.api["pve2"].StorageLs[0].Avail = 100 << 30
	r.api["pve1"].DiskUsed = map[int]int64{105: 7 << 30}
	r.hosts["pve2"].Avail["/var/lib/vz/dump"] = 8 << 30
	_, err := r.m.Move(ctx, opts())
	if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "has 8.0 GiB free") || !strings.Contains(err.Error(), "(used 7.0 GiB → needs 8.7 GiB)") {
		t.Fatalf("err = %v", err)
	}
	assertJournal(t, r, preflightReads)

	// Used 0 (a stopped container reports 0): fall back to the allocated
	// size, which refuses here.
	for name, set := range map[string]func(*rig){
		"zero used": func(r *rig) { r.api["pve1"].DiskUsed = map[int]int64{105: 0} },
		"stopped":   func(r *rig) { r.api["pve1"].Guests[0].Status = "stopped"; r.hosts["pve1"].State = "stopped" },
	} {
		r = newRig(t)
		r.api["pve1"].Configs[105]["rootfs"] = "local-lvm:vm-105-disk-0,size=96G"
		r.api["pve2"].StorageLs[0].Avail = 100 << 30
		set(r)
		_, err = r.m.Move(ctx, opts())
		if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "may need up to 96.0 GiB (allocated 96.0 GiB)") {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// The target storage check stays on the allocated size: a thin pool
	// still needs room for the whole volume.
	r = newRig(t)
	r.api["pve1"].Configs[105]["rootfs"] = "local-lvm:vm-105-disk-0,size=96G"
	r.api["pve1"].DiskUsed = map[int]int64{105: 7 << 30}
	r.api["pve2"].StorageLs[0].Avail = 50 << 30
	_, err = r.m.Move(ctx, opts())
	if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "target storage local-lvm on pve2 has 50.0 GiB free, guest 105 needs 96.0 GiB") {
		t.Fatalf("storage: err = %v", err)
	}
}

func TestMoveSharedStorageNeedsUsedEstimatePlusAllocated(t *testing.T) {
	// 12 GiB dir guest using 2 GiB: dump 2*1.1+1 = 3.2 GiB plus 12 GiB restored.
	r := newRig(t)
	r.api["pve1"].Configs[105]["rootfs"] = "local:105/vm-105-disk-0.raw,size=12G"
	r.api["pve1"].DiskUsed = map[int]int64{105: 2 << 30}
	r.api["pve2"].StorageLs = []proxmox.Storage{{Name: "local", Type: "dir", Content: []string{"rootdir"}, Avail: 20 << 30, Active: true}}
	if _, err := r.m.Move(ctx, opts()); err != nil {
		t.Fatalf("20 GiB dir, 2 GiB used: %v", err) // 24 GiB on the allocated basis
	}

	r = newRig(t)
	r.api["pve1"].Configs[105]["rootfs"] = "local:105/vm-105-disk-0.raw,size=12G"
	r.api["pve1"].DiskUsed = map[int]int64{105: 2 << 30}
	r.api["pve2"].StorageLs = []proxmox.Storage{{Name: "local", Type: "dir", Content: []string{"rootdir"}, Avail: 15 << 30, Active: true}}
	_, err := r.m.Move(ctx, opts())
	if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "needs 15.2 GiB (dump: used 2.0 GiB → needs 3.2 GiB; plus restored disks: allocated 12.0 GiB)") {
		t.Fatalf("err = %v", err)
	}
	assertJournal(t, r, preflightReads)
}

func TestMoveQEMUDumpDirUsesAllocatedSize(t *testing.T) {
	// PVE reports 0 used for VMs; even a non-zero value is not asked for.
	r := newRig(t)
	r.api["pve1"].Guests = []proxmox.Guest{{VMID: 200, Name: "vm", Status: "running", Type: "qemu"}}
	r.api["pve1"].Configs[200] = map[string]string{"scsi0": "local-lvm:vm-200-disk-0,size=60G"}
	r.api["pve1"].DiskUsed = map[int]int64{200: 1 << 30}
	_, err := r.m.Move(ctx, Opts{VMID: 200, From: "pve1", To: "pve2", Yes: true})
	if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "dump dir /var/lib/vz/dump on pve2 has 50.0 GiB free, the dump may need up to 60.0 GiB (allocated 60.0 GiB)") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range r.api["pve1"].Calls() {
		if strings.HasPrefix(c, "GuestDiskUsed") {
			t.Fatalf("asked used bytes for a VM: %v", r.api["pve1"].Calls())
		}
	}
}

func TestMoveRestoreFailureNeverStartsTarget(t *testing.T) {
	r := newRig(t)
	r.hosts["pve2"].Fail = func(c string) error {
		if strings.Contains(c, "Restore") {
			return errors.New("unable to restore: storage full")
		}
		return nil
	}
	o := opts()
	o.Start = true
	_, err := r.m.Move(ctx, o)
	if err == nil || !strings.Contains(err.Error(), "pct start 105") || !strings.Contains(err.Error(), "pct list") ||
		!strings.Contains(err.Error(), "kept at "+dump) || !strings.Contains(err.Error(), "`pct restore 105 "+dump+" --storage local-lvm`") {
		t.Fatalf("err = %v", err)
	}
	e := r.j.Entries()
	if slices.ContainsFunc(e, func(s string) bool { return strings.Contains(s, "Start") || strings.Contains(s, "Set") }) {
		t.Fatalf("target started or source changed after failed restore: %v", e)
	}
	// The verified dump stays for a manual restore: no second transfer.
	if last := e[len(e)-1]; last != "pve2 Restore lxc 105 "+dump+" local-lvm" || slices.Contains(e, "pve2 Remove "+dump) {
		t.Fatalf("dump removed or calls after restore: %v", e)
	}
}

func TestMoveOnbootFailureNeverStartsTarget(t *testing.T) {
	r := newRig(t)
	r.hosts["pve1"].Fail = func(c string) error {
		if strings.Contains(c, "--onboot") {
			return errors.New("ssh: connection lost")
		}
		return nil
	}
	o := opts()
	o.Start = true
	_, err := r.m.Move(ctx, o)
	if err == nil || errs.ExitCode(err) != 1 || !strings.Contains(err.Error(), "was restored") || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("err = %v", err)
	}
	e := r.j.Entries()
	if last := e[len(e)-1]; last != "pve1 Set lxc 105 --onboot 0" {
		t.Fatalf("calls after the failed onboot=0: %v", e)
	}
}

func TestMoveShutdownFailureCopiesNothing(t *testing.T) {
	r := newRig(t)
	r.hosts["pve1"].Fail = func(c string) error {
		if strings.Contains(c, "Shutdown") {
			return errors.New("guest is \"running\" after shutdown")
		}
		return nil
	}
	_, err := r.m.Move(ctx, opts())
	if err == nil || !strings.Contains(err.Error(), "may still be running") {
		t.Fatalf("err = %v", err)
	}
	assertJournal(t, r, append(slices.Clone(preflightReads), "pve1 Shutdown lxc 105 120"))
}

func TestMoveQEMU(t *testing.T) {
	r := newRig(t)
	r.api["pve1"].Guests = []proxmox.Guest{{VMID: 200, Name: "vm", Status: "stopped", Type: "qemu"}}
	r.api["pve1"].Configs[200] = map[string]string{
		"scsi0": "local-lvm:vm-200-disk-0,size=32G",
		"ide2":  "local:iso/debian.iso,media=cdrom",
		"net0":  "virtio=BC:24:11:00:00:02,bridge=vmbr1",
	}
	res, err := r.m.Move(ctx, Opts{VMID: 200, From: "pve1", To: "pve2", Yes: true})
	if err != nil || res.Type != "qemu" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	qd := "/var/lib/vz/dump/vzdump-qemu-200-2026_11_08-10_15_00.vma.zst"
	if !slices.Contains(r.j.Entries(), "pve2 Restore qemu 200 "+qd+" local-lvm") {
		t.Fatalf("journal = %v", r.j.Entries())
	}

	// The recovery hint names qm for VMs.
	r = newRig(t)
	r.api["pve1"].Guests = []proxmox.Guest{{VMID: 200, Status: "stopped", Type: "qemu"}}
	r.api["pve1"].Configs[200] = map[string]string{"scsi0": "local-lvm:vm-200-disk-0,size=8G"}
	r.hosts["pve2"].Fail = func(c string) error {
		if strings.Contains(c, "Restore") {
			return errors.New("boom")
		}
		return nil
	}
	if _, err := r.m.Move(ctx, Opts{VMID: 200, From: "pve1", To: "pve2", Yes: true}); err == nil || !strings.Contains(err.Error(), "`qm start 200` on pve1") || !strings.Contains(err.Error(), "`qmrestore /var/lib/vz/dump/vzdump-qemu-200-2026_11_08-10_15_00.vma.zst 200 --storage local-lvm`") {
		t.Fatalf("err = %v", err)
	}
}

func TestMoveCancelledContextStillCleansUp(t *testing.T) {
	r := newRig(t)
	cctx, cancel := context.WithCancel(ctx)
	r.runner = &runtest.Fake{T: t, OnCall: func(c runtest.Call) {
		r.j.Add("run " + c.String())
		cancel() // Ctrl-C during the dump
	}}
	r.runner.On([]string{"dump"}, runtest.Response{Err: context.Canceled})
	r.runner.On([]string{"receive"}, runtest.Response{})
	r.m.Runner = r.runner
	// Remove must get a live context even though cctx is cancelled.
	_, err := r.m.Move(cctx, opts())
	if err == nil {
		t.Fatal("no error")
	}
	e := r.j.Entries()
	if e[len(e)-1] != "pve2 Remove "+dump {
		t.Fatalf("journal = %v", e)
	}
}

func TestMoveToOtherVMID(t *testing.T) {
	r := newRig(t)
	o := opts()
	o.ToVMID, o.Start = 900, true
	res, err := r.m.Move(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	want := happyOrder(true)
	for i, e := range want {
		e = strings.Replace(e, "pve2 Restore lxc 105", "pve2 Restore lxc 900", 1)
		want[i] = strings.Replace(e, "pve2 Start lxc 105", "pve2 Start lxc 900", 1)
	}
	assertJournal(t, r, want) // source ops still use 105; the dump is named after the source id
	if res.TargetVMID != 900 || res.VMID != 105 || !strings.Contains(res.Note, "MAC") {
		t.Fatalf("result = %+v", res)
	}
}

func TestMoveVMIDClashSuggestsAFreeID(t *testing.T) {
	r := newRig(t)
	r.api["pve1"].Guests = append(r.api["pve1"].Guests, proxmox.Guest{VMID: 101, Type: "lxc"}, proxmox.Guest{VMID: 103, Type: "lxc"})
	r.api["pve2"].Guests = []proxmox.Guest{{VMID: 105, Type: "qemu", Name: "win"}, {VMID: 100, Type: "qemu"}}
	_, err := r.m.Move(ctx, opts())
	if errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "--to-vmid 102") {
		t.Fatalf("err = %v", err)
	}
	assertJournal(t, r, nil)

	// A --to-vmid that is itself taken on the target is refused the same way.
	o := opts()
	o.ToVMID = 100
	if _, err := r.m.Move(ctx, o); errs.KindOf(err) != errs.Preflight || !strings.Contains(err.Error(), "VMID 100 already exists on pve2") {
		t.Fatalf("taken --to-vmid: %v", err)
	}
	o.ToVMID = 50
	if _, err := r.m.Move(ctx, o); errs.KindOf(err) != errs.Refused {
		t.Fatalf("--to-vmid 50: %v", err)
	}
}

func TestMoveToOtherVMIDRestoreFailureNamesTheTargetID(t *testing.T) {
	r := newRig(t)
	r.hosts["pve2"].Fail = func(c string) error {
		if strings.Contains(c, "Restore") {
			return errors.New("storage full")
		}
		return nil
	}
	o := opts()
	o.ToVMID = 900
	_, err := r.m.Move(ctx, o)
	if err == nil || !strings.Contains(err.Error(), "`pct restore 900 "+dump+" --storage local-lvm`") || !strings.Contains(err.Error(), "`pct start 105` on pve1") {
		t.Fatalf("err = %v", err)
	}
}
