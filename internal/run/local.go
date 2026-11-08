package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// waitDelay bounds how long Wait lingers on I/O after the process exits or
// is killed, so a stdin reader that never ends cannot hang province.
const waitDelay = 10 * time.Second

// Local runs commands as local processes.
type Local struct{}

func (Local) command(ctx context.Context, c Cmd) (*exec.Cmd, *tail) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdin = c.Stdin
	cmd.Stdout = c.Stdout
	t := &tail{max: 8 << 10}
	if c.Stderr != nil {
		cmd.Stderr = io.MultiWriter(c.Stderr, t)
	} else {
		cmd.Stderr = t
	}
	cmd.WaitDelay = waitDelay
	return cmd, t
}

// Run implements Runner.
func (l Local) Run(ctx context.Context, c Cmd) error {
	cmd, t := l.command(ctx, c)
	return wrap(ctx, c, cmd.Run(), t)
}

// Pipe implements Runner. src.Stdout and dst.Stdin are ignored: an OS pipe
// connects the two processes directly, so dst sees EOF as soon as src exits
// and a src failure is reported even if dst exits cleanly.
func (l Local) Pipe(ctx context.Context, src, dst Cmd) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	srcCmd, srcTail := l.command(ctx, src)
	srcCmd.Stdout = pw
	dstCmd, dstTail := l.command(ctx, dst)
	dstCmd.Stdin = pr

	if err := srcCmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return wrap(ctx, src, err, srcTail)
	}
	if err := dstCmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		_ = srcCmd.Process.Kill()
		_ = srcCmd.Wait()
		return wrap(ctx, dst, err, dstTail)
	}
	// The children hold their own copies; closing ours lets EOF and EPIPE through.
	pr.Close()
	pw.Close()
	srcErr := wrap(ctx, src, srcCmd.Wait(), srcTail)
	dstErr := wrap(ctx, dst, dstCmd.Wait(), dstTail)
	return errors.Join(srcErr, dstErr)
}

func wrap(ctx context.Context, c Cmd, err error, t *tail) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", c.Name, ctx.Err())
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Cmd: c.String(), Code: ee.ExitCode(), Stderr: t.String()}
	}
	return fmt.Errorf("%s: %w", c.Name, err)
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
