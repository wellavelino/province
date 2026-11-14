package run

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunExitErrorKeepsCodeAndStderr(t *testing.T) {
	var own bytes.Buffer
	err := Local{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo boom >&2; exit 3"}, Stderr: &own})
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want *ExitError", err)
	}
	if ee.Code != 3 || strings.TrimSpace(ee.Stderr) != "boom" {
		t.Fatalf("ExitError = %+v", ee)
	}
	// The caller's own stderr writer still sees the output.
	if strings.TrimSpace(own.String()) != "boom" {
		t.Fatalf("caller stderr = %q", own.String())
	}
}

func TestRunPassesStdinEnvAndDir(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	err := Local{}.Run(context.Background(), Cmd{
		Name:   "sh",
		Args:   []string{"-c", `printf '%s|%s|' "$X" "$(pwd)"; cat`},
		Env:    []string{"X=1"},
		Dir:    dir,
		Stdin:  strings.NewReader("in"),
		Stdout: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "1|" + dir + "|in"; out.String() != want {
		t.Fatalf("out = %q, want %q", out.String(), want)
	}
}

func TestPipeDeliversBytes(t *testing.T) {
	var out bytes.Buffer
	err := Local{}.Pipe(context.Background(),
		Cmd{Name: "printf", Args: []string{"abc"}},
		Cmd{Name: "cat", Stdout: &out})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "abc" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestPipeReportsFailingSource(t *testing.T) {
	// A dump that dies midway must be an error even though the receiver
	// wrote what it got and exited 0.
	var out bytes.Buffer
	err := Local{}.Pipe(context.Background(),
		Cmd{Name: "sh", Args: []string{"-c", "printf abc; exit 3"}},
		Cmd{Name: "cat", Stdout: &out})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("err = %v, want ExitError code 3", err)
	}
	if out.String() != "abc" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestPipeReportsFailingDestination(t *testing.T) {
	err := Local{}.Pipe(context.Background(),
		Cmd{Name: "printf", Args: []string{"abc"}},
		Cmd{Name: "sh", Args: []string{"-c", "cat >/dev/null; exit 4"}})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 4 {
		t.Fatalf("err = %v, want ExitError code 4", err)
	}
}

func TestContextCancelKillsChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Local{}.Run(ctx, Cmd{Name: "sleep", Args: []string{"30"}})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("child not killed promptly: %v", time.Since(start))
	}
}

func TestMissingBinary(t *testing.T) {
	err := Local{}.Run(context.Background(), Cmd{Name: "province-no-such-binary"})
	var ee *ExitError
	if err == nil || errors.As(err, &ee) {
		t.Fatalf("err = %v, want a start error", err)
	}
}

func TestExitErrorKeepsOnlyTheLastStderrLines(t *testing.T) {
	err := Local{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "i=1; while [ $i -le 40 ]; do echo line$i >&2; i=$((i+1)); done; exit 1"}})
	msg := err.Error()
	if strings.Contains(msg, "line25\n") || !strings.Contains(msg, "line26\n") || !strings.HasSuffix(msg, "line40") {
		t.Fatalf("message = %q", msg)
	}
	if !strings.Contains(msg, "(25 earlier stderr lines omitted)") || strings.Count(msg, "\n") != ErrorLines {
		t.Fatalf("message = %q", msg)
	}
	var ee *ExitError
	if errors.As(err, &ee); !strings.Contains(ee.Stderr, "line1\n") {
		t.Fatal("full stderr tail no longer kept on the error value")
	}
}

func TestGuestExitErrorKeepsStderrOutOfMessage(t *testing.T) {
	err := MarkGuest(Local{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "i=1; while [ $i -le 40 ]; do echo line$i >&2; i=$((i+1)); done; exit 1"}}))
	if msg := err.Error(); strings.Contains(msg, "line40") || !strings.HasSuffix(msg, "exit status 1") {
		t.Fatalf("message = %q", msg)
	}
	var ee *ExitError
	errors.As(err, &ee)
	out := ee.GuestOutput()
	if !strings.HasPrefix(out, "(25 earlier stderr lines omitted)\nline26\n") || !strings.HasSuffix(out, "line40") {
		t.Fatalf("guest output = %q", out)
	}
}
