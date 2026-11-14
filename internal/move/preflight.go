package move

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/wellavelino/province/internal/config"
	"github.com/wellavelino/province/internal/errs"
	"github.com/wellavelino/province/internal/hostops"
	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/run"
	"github.com/wellavelino/province/internal/vmids"
)

// ShutdownTimeout is how long the source guest gets to shut down cleanly.
const ShutdownTimeout = 120

// Mover moves guests between the hosts of one site.
type Mover struct {
	Cfg    *config.Config
	Client func(config.Host) (proxmox.Client, error) // fails when the host's token is not set
	Host   func(config.Host) hostops.Host
	Runner run.Runner // runs the dump -> receive pipe
	Now    func() time.Time
	Log    io.Writer
}

// Opts are the guest move flags.
type Opts struct {
	VMID    int
	From    string
	To      string
	Storage string // target storage; default: the source's primary storage
	ToVMID  int    // restore under this VMID on the target; default: the same
	Start   bool   // start on the target after the source is set onboot=0
	Yes     bool
}

// plan is everything preflight learned.
type plan struct {
	from, to    config.Host
	src, dst    hostops.Host
	typ         string
	targetID    int // VMID on the target
	running     bool
	storage     string
	total       int64
	warnings    []string
	dumpPath    string
	startCmd    string // how to bring the source back
	targetStart string // how to start the moved copy
	targetList  string // how to inspect the target
	restoreCmd  string // how to restore the kept dump by hand
}

func (m *Mover) logf(format string, a ...any) {
	if m.Log != nil {
		fmt.Fprintf(m.Log, "province: "+format+"\n", a...)
	}
}

func preflightErr(err error) error { return &errs.Error{Kind: errs.Preflight, Err: err} }

func gib(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }

// preflight checks everything it can without changing anything.
func (m *Mover) preflight(ctx context.Context, o Opts) (*plan, error) {
	if !o.Yes {
		return nil, errs.Refusedf("guest move stops guest %d on %s: pass --yes", o.VMID, o.From)
	}
	if o.VMID <= 0 {
		return nil, errs.Refusedf("vmid must be a positive number")
	}
	if o.From == "" || o.To == "" {
		return nil, errs.Refusedf("--from and --to are required")
	}
	if o.ToVMID != 0 && o.ToVMID < 100 {
		return nil, errs.Refusedf("--to-vmid must be 100 or more, got %d", o.ToVMID)
	}
	if o.From == o.To {
		return nil, errs.Refusedf("--from and --to are the same host (%s)", o.From)
	}
	from, err := m.Cfg.HostByName(o.From)
	if err != nil {
		return nil, &errs.Error{Kind: errs.Refused, Err: err}
	}
	to, err := m.Cfg.HostByName(o.To)
	if err != nil {
		return nil, &errs.Error{Kind: errs.Refused, Err: err}
	}
	for _, h := range []config.Host{from, to} {
		if h.IsIncus() {
			return nil, errs.Refusedf("guest move works between Proxmox hosts only; %s is an incus host", h.Name)
		}
	}
	// Both hosts' API tokens must be present before anything is asked.
	srcAPI, err := m.Client(from)
	if err != nil {
		return nil, err
	}
	dstAPI, err := m.Client(to)
	if err != nil {
		return nil, err
	}
	p := &plan{from: from, to: to, src: m.Host(from), dst: m.Host(to), targetID: o.VMID}
	if o.ToVMID != 0 {
		p.targetID = o.ToVMID
	}

	srcGuests, err := srcAPI.ListGuests(ctx, from.Node)
	if err != nil {
		return nil, preflightErr(err)
	}
	var g *proxmox.Guest
	for i := range srcGuests {
		if srcGuests[i].VMID == o.VMID {
			g = &srcGuests[i]
		}
	}
	if g == nil {
		return nil, errs.Preflightf("guest %d not found on %s", o.VMID, from.Name)
	}
	p.typ = g.Type
	switch g.Status {
	case "running":
		p.running = true
	case "stopped":
	default:
		return nil, errs.Preflightf("guest %d on %s is %q; only running or stopped guests are moved", o.VMID, from.Name, g.Status)
	}
	tool := "pct"
	if p.typ == proxmox.TypeQEMU {
		tool = "qm"
	}
	p.startCmd = fmt.Sprintf("`%s start %d` on %s", tool, o.VMID, from.Name)
	p.targetStart = fmt.Sprintf("`%s start %d` on %s", tool, p.targetID, to.Name)
	p.targetList = fmt.Sprintf("`%s list` on %s", tool, to.Name)

	dstGuests, err := dstAPI.ListGuests(ctx, to.Node)
	if err != nil {
		return nil, preflightErr(err)
	}
	for _, dg := range dstGuests {
		if dg.VMID == p.targetID {
			used, _ := vmids.Used(ctx, m.Cfg.ProxmoxHosts(), m.Client)
			free := vmids.LowestFree(used, max(m.Cfg.VMIDStart, 100))
			return nil, errs.Preflightf("VMID %d already exists on %s (%s %q); nothing was changed; pass --to-vmid %d (free on every host with a token) to restore it under another id",
				p.targetID, to.Name, dg.Type, dg.Name, free)
		}
	}

	conf, err := srcAPI.GuestConfig(ctx, from.Node, p.typ, o.VMID)
	if err != nil {
		return nil, preflightErr(err)
	}
	if lock := conf["lock"]; lock != "" {
		return nil, errs.Preflightf("guest %d on %s is locked (%s)", o.VMID, from.Name, lock)
	}
	disks, err := proxmox.Disks(conf)
	if err != nil {
		return nil, preflightErr(err)
	}
	p.total = proxmox.TotalSize(disks)

	if want := proxmox.NetBridges(conf); len(want) > 0 {
		have, err := dstAPI.Bridges(ctx, to.Node)
		if err != nil {
			return nil, preflightErr(err)
		}
		for _, b := range want {
			if !slices.Contains(have, b) {
				return nil, errs.Preflightf("bridge %s used by guest %d is missing on %s (have %s)", b, o.VMID, to.Name, strings.Join(have, ", "))
			}
		}
	}

	p.storage = o.Storage
	explicit := p.storage != ""
	if !explicit {
		p.storage = proxmox.PrimaryStorage(disks)
	}
	content := "rootdir"
	if p.typ == proxmox.TypeQEMU {
		content = "images"
	}
	storages, err := dstAPI.Storages(ctx, to.Node)
	if err != nil {
		return nil, preflightErr(err)
	}
	var st *proxmox.Storage
	for i := range storages {
		if storages[i].Name == p.storage {
			st = &storages[i]
		}
	}
	switch {
	case st == nil && explicit:
		return nil, errs.Preflightf("storage %s not found on %s", p.storage, to.Name)
	case st == nil:
		return nil, errs.Preflightf("source storage %q is not on %s: pass --storage <target storage>", p.storage, to.Name)
	case !st.Active || !st.Has(content):
		return nil, errs.Preflightf("storage %s on %s is inactive or does not hold %s", p.storage, to.Name, content)
	case st.Avail < p.total:
		return nil, errs.Preflightf("target storage %s on %s has %s free, guest %d needs %s", p.storage, to.Name, gib(st.Avail), o.VMID, gib(p.total))
	}

	// SSH reads: prove both hosts answer (and sudo works) before the first change.
	if _, err := p.src.Status(ctx, p.typ, o.VMID); err != nil {
		return nil, preflightErr(err)
	}
	dumpNeed, basis := m.dumpEstimate(ctx, srcAPI, p, o.VMID, disks)
	avail, err := p.dst.DfAvail(ctx, to.DumpDir)
	if err != nil {
		return nil, preflightErr(err)
	}
	if avail < dumpNeed {
		return nil, errs.Preflightf("dump dir %s on %s has %s free, the dump may need up to %s (%s)", to.DumpDir, to.Name, gib(avail), gib(dumpNeed), basis)
	}
	// dir storage (e.g. local under /var/lib/vz) and zfspool (same pool as
	// the root dataset) usually share free space with dump_dir, as does any
	// storage reporting exactly dump_dir's free bytes: the dump and the
	// restored disks then coexist, so both must fit.
	if shared := st.Type == "dir" || st.Type == "zfspool" || st.Avail == avail; shared && st.Avail < dumpNeed+p.total {
		return nil, errs.Preflightf("target storage %s (%s) on %s has %s free and shares it with the dump in %s, so guest %d needs %s (dump: %s; plus restored disks: allocated %s)",
			p.storage, st.Type, to.Name, gib(st.Avail), to.DumpDir, o.VMID, gib(dumpNeed+p.total), basis, gib(p.total))
	}

	for _, k := range proxmox.HostBound(conf) {
		p.warnings = append(p.warnings, fmt.Sprintf("%s: %s is tied to %s and may not work on %s", k, conf[k], from.Name, to.Name))
	}
	p.dumpPath = strings.TrimRight(to.DumpDir, "/") + "/" + DumpName(p.typ, o.VMID, m.Now())
	if p.typ == proxmox.TypeQEMU {
		p.restoreCmd = fmt.Sprintf("`qmrestore %s %d --storage %s`", p.dumpPath, p.targetID, p.storage)
	} else {
		p.restoreCmd = fmt.Sprintf("`pct restore %d %s --storage %s`", p.targetID, p.dumpPath, p.storage)
	}
	return p, nil
}

// dumpEstimate is how much room the dump may need in dump_dir, and the
// basis to name in a refusal. A zstd vzdump holds only used data. PVE
// reports used bytes for a running container; otherwise (stopped, or a
// VM, which reports 0) each disk is measured on the source host. A disk
// that cannot be measured counts at its allocated size, and an estimate
// above the allocated total gives way to it, since the dump cannot be
// bigger than the disks: the estimate is never above the old one.
func (m *Mover) dumpEstimate(ctx context.Context, srcAPI proxmox.Client, p *plan, vmid int, disks []proxmox.Disk) (int64, string) {
	allocated := "allocated " + gib(p.total)
	var used int64
	var unmeasured []string
	if p.typ == proxmox.TypeLXC {
		// An API error falls through to measuring, like a 0.
		if n, err := srcAPI.GuestDiskUsed(ctx, p.from.Node, p.typ, vmid); err == nil && n > 0 {
			used = n
		}
	}
	if used == 0 {
		for _, d := range disks {
			n, err := p.src.VolumeUsed(ctx, d.Volume)
			if err != nil {
				m.logf("could not measure %s (%s) on %s, counting its allocated size: %v", d.Key, d.Volume, p.from.Name, err)
				n = d.Size
				unmeasured = append(unmeasured, d.Key)
			}
			used += n
		}
	}
	// With no disk measured, used is the allocated total and the cap below
	// returns the allocated basis.
	need := used + used/10 + 1<<30
	if need >= p.total {
		return p.total, allocated
	}
	basis := "used " + gib(used)
	if len(unmeasured) > 0 {
		basis += ", counting " + strings.Join(unmeasured, ", ") + " at allocated size"
	}
	return need, basis + " → needs " + gib(need)
}
