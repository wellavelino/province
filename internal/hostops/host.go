// Package hostops runs mutating guest operations on a Proxmox host over SSH
// (pct, qm, vzdump), as root or through sudo -n.
package hostops

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/wellavelino/province/internal/proxmox"
	"github.com/wellavelino/province/internal/run"
	"github.com/wellavelino/province/internal/sshx"
)

// Host is one Proxmox host reached over SSH.
type Host interface {
	// Exec runs argv inside container vmid (pct exec vmid -- argv).
	Exec(ctx context.Context, vmid int, stdin io.Reader, stdout io.Writer, argv ...string) error
	Set(ctx context.Context, typ string, vmid int, opts ...string) error
	// Shutdown asks the guest to shut down, then checks that it stopped.
	Shutdown(ctx context.Context, typ string, vmid int, timeoutSec int) error
	Stop(ctx context.Context, typ string, vmid int) error
	Start(ctx context.Context, typ string, vmid int) error
	Reboot(ctx context.Context, typ string, vmid int) error
	Status(ctx context.Context, typ string, vmid int) (string, error)
	// DumpCmd streams a stop-mode zstd backup of vmid to stdout.
	DumpCmd(typ string, vmid int) run.Cmd
	// ReceiveCmd writes its stdin to path, readable by root only.
	ReceiveCmd(path string) run.Cmd
	ZstdTest(ctx context.Context, path string) error
	DfAvail(ctx context.Context, path string) (int64, error)
	Remove(ctx context.Context, path string) error
	Restore(ctx context.Context, typ string, vmid int, archive, storage string) error
	// Destroy deletes the guest and its disks (pct|qm destroy vmid --purge).
	Destroy(ctx context.Context, typ string, vmid int) error
	// VolumeUsed measures the bytes a volume holds on this host. It errors
	// on any volume it cannot measure, so the caller can fall back to the
	// allocated size.
	VolumeUsed(ctx context.Context, volid string) (int64, error)
}

// SSHHost implements Host with the system ssh binary.
type SSHHost struct {
	Runner run.Runner
	Target sshx.Target
	Log    io.Writer // remote stderr; may be nil
}

func tool(typ string) (string, error) {
	switch typ {
	case proxmox.TypeLXC:
		return "pct", nil
	case proxmox.TypeQEMU:
		return "qm", nil
	}
	return "", fmt.Errorf("unknown guest type %q", typ)
}

func (h *SSHHost) cmd(remote ...string) run.Cmd {
	c := sshx.Command(h.Target, remote...)
	c.Stderr = h.Log
	return c
}

func (h *SSHHost) run(ctx context.Context, stdout io.Writer, remote ...string) error {
	c := h.cmd(remote...)
	c.Stdout = stdout
	if err := h.Runner.Run(ctx, c); err != nil {
		return fmt.Errorf("%s on %s: %w", strings.Join(remote, " "), h.Target.Dest, err)
	}
	return nil
}

func (h *SSHHost) guest(ctx context.Context, typ, verb string, vmid int, extra ...string) error {
	t, err := tool(typ)
	if err != nil {
		return err
	}
	return h.run(ctx, nil, append([]string{t, verb, strconv.Itoa(vmid)}, extra...)...)
}

// Exec implements Host.
func (h *SSHHost) Exec(ctx context.Context, vmid int, stdin io.Reader, stdout io.Writer, argv ...string) error {
	c := h.cmd(append([]string{"pct", "exec", strconv.Itoa(vmid), "--"}, argv...)...)
	c.Stdin, c.Stdout = stdin, stdout
	if err := h.Runner.Run(ctx, c); err != nil {
		return fmt.Errorf("pct exec %d on %s: %w", vmid, h.Target.Dest, err)
	}
	return nil
}

// Set implements Host.
func (h *SSHHost) Set(ctx context.Context, typ string, vmid int, opts ...string) error {
	return h.guest(ctx, typ, "set", vmid, opts...)
}

// Shutdown implements Host.
func (h *SSHHost) Shutdown(ctx context.Context, typ string, vmid int, timeoutSec int) error {
	if err := h.guest(ctx, typ, "shutdown", vmid, "--timeout", strconv.Itoa(timeoutSec)); err != nil {
		return err
	}
	st, err := h.Status(ctx, typ, vmid)
	if err != nil {
		return err
	}
	if st != "stopped" {
		return fmt.Errorf("guest %d on %s is %q after shutdown, not stopped", vmid, h.Target.Dest, st)
	}
	return nil
}

// Stop implements Host.
func (h *SSHHost) Stop(ctx context.Context, typ string, vmid int) error {
	return h.guest(ctx, typ, "stop", vmid)
}

// Start implements Host.
func (h *SSHHost) Start(ctx context.Context, typ string, vmid int) error {
	return h.guest(ctx, typ, "start", vmid)
}

// Reboot implements Host.
func (h *SSHHost) Reboot(ctx context.Context, typ string, vmid int) error {
	return h.guest(ctx, typ, "reboot", vmid)
}

// Status implements Host: the word after "status:" (running, stopped, ...).
func (h *SSHHost) Status(ctx context.Context, typ string, vmid int) (string, error) {
	t, err := tool(typ)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := h.run(ctx, &out, t, "status", strconv.Itoa(vmid)); err != nil {
		return "", err
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "status:"); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", fmt.Errorf("%s status %d on %s: unexpected output %q", t, vmid, h.Target.Dest, out.String())
}

// DumpCmd implements Host.
func (h *SSHHost) DumpCmd(_ string, vmid int) run.Cmd {
	return h.cmd("vzdump", strconv.Itoa(vmid), "--stdout", "--mode", "stop", "--compress", "zstd")
}

// ReceiveCmd implements Host. The script is one remote word, so the path is
// quoted inside it and the whole script is quoted again by sshx.
func (h *SSHHost) ReceiveCmd(path string) run.Cmd {
	return h.cmd("sh", "-c", "umask 077; cat > "+sshx.Quote(path))
}

// ZstdTest implements Host.
func (h *SSHHost) ZstdTest(ctx context.Context, path string) error {
	return h.run(ctx, nil, "zstd", "-q", "-t", path)
}

// DfAvail implements Host: free bytes on the filesystem holding path.
func (h *SSHHost) DfAvail(ctx context.Context, path string) (int64, error) {
	var out bytes.Buffer
	if err := h.run(ctx, &out, "df", "-P", "-B1", path); err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("df %s on %s: unexpected output %q", path, h.Target.Dest, out.String())
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return 0, fmt.Errorf("df %s on %s: unexpected output %q", path, h.Target.Dest, out.String())
	}
	n, err := strconv.ParseInt(f[3], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("df %s on %s: %w", path, h.Target.Dest, err)
	}
	return n, nil
}

// Remove implements Host.
func (h *SSHHost) Remove(ctx context.Context, path string) error {
	return h.run(ctx, nil, "rm", "-f", "--", path)
}

// Restore implements Host. The archive keeps the guest's config, MAC included.
func (h *SSHHost) Restore(ctx context.Context, typ string, vmid int, archive, storage string) error {
	id := strconv.Itoa(vmid)
	switch typ {
	case proxmox.TypeLXC:
		return h.run(ctx, nil, "pct", "restore", id, archive, "--storage", storage)
	case proxmox.TypeQEMU:
		return h.run(ctx, nil, "qmrestore", archive, id, "--storage", storage)
	}
	return fmt.Errorf("unknown guest type %q", typ)
}

// Destroy implements Host.
func (h *SSHHost) Destroy(ctx context.Context, typ string, vmid int) error {
	return h.guest(ctx, typ, "destroy", vmid, "--purge")
}

// VolumeUsed implements Host. pvesm resolves the volume to a path, and the
// path says how to measure it: a zvol through zfs, any other device as a
// thin LV through lvs, a regular file by its allocated blocks. Anything
// else (a directory, a remote path, a thick LV) is an error. The PVE API
// reports no used figure for these volumes, so the host is asked.
func (h *SSHHost) VolumeUsed(ctx context.Context, volid string) (int64, error) {
	var out bytes.Buffer
	if err := h.run(ctx, &out, "pvesm", "path", volid); err != nil {
		return 0, err
	}
	path := strings.TrimSpace(out.String())
	bad := func(cmd string) (int64, error) {
		return 0, fmt.Errorf("%s %s on %s: cannot measure from %q", cmd, path, h.Target.Dest, strings.TrimSpace(out.String()))
	}
	out.Reset()
	switch {
	case strings.HasPrefix(path, "/dev/zvol/"):
		// logicalreferenced counts the data before pool compression; the
		// dump reads it uncompressed, so this is the safer figure.
		if err := h.run(ctx, &out, "zfs", "get", "-Hp", "-o", "value", "logicalreferenced", strings.TrimPrefix(path, "/dev/zvol/")); err != nil {
			return 0, err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(out.String()), 10, 64)
		if err != nil || n < 0 {
			return bad("zfs get")
		}
		return n, nil
	case strings.HasPrefix(path, "/dev/"):
		if err := h.run(ctx, &out, "lvs", "--noheadings", "--units", "b", "--nosuffix", "-o", "lv_size,data_percent", "--", path); err != nil {
			return 0, err
		}
		// A thick LV prints no data percent: its used bytes are unknown.
		f := strings.Fields(out.String())
		if len(f) != 2 {
			return bad("lvs")
		}
		size, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			return bad("lvs")
		}
		// lvs may print the percent with a decimal comma under some locales.
		pct, err := strconv.ParseFloat(strings.Replace(f[1], ",", ".", 1), 64)
		if err != nil || pct < 0 || pct > 100 {
			return bad("lvs")
		}
		return int64(math.Ceil(float64(size) * pct / 100)), nil
	case strings.HasPrefix(path, "/"):
		// Allocated blocks, not the apparent size: a sparse raw file or a
		// preallocated qcow2 is as long as the whole disk.
		if err := h.run(ctx, &out, "stat", "-L", "-c", "%b:%B:%F", "--", path); err != nil {
			return 0, err
		}
		f := strings.SplitN(strings.TrimSpace(out.String()), ":", 3)
		if len(f) != 3 || f[2] != "regular file" {
			return bad("stat")
		}
		blocks, err1 := strconv.ParseInt(f[0], 10, 64)
		bsize, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 != nil || err2 != nil || blocks < 0 || bsize <= 0 {
			return bad("stat")
		}
		return blocks * bsize, nil
	}
	return 0, fmt.Errorf("pvesm path %s on %s: %q is not a local path", volid, h.Target.Dest, path)
}
