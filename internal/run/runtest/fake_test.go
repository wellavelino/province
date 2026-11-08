package runtest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/wellavelino/province/internal/run"
)

type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Errorf(format string, a ...any) { r.errs = append(r.errs, format) }

func TestFakeScriptsByPrefixAndRecords(t *testing.T) {
	f := &Fake{T: t}
	f.Once([]string{"pct", "status"}, Response{Stdout: "status: running\n"})
	f.On([]string{"pct", "status"}, Response{Stdout: "status: stopped\n"})
	f.On([]string{"pct", "start"}, Response{Code: 1, Stderr: "nope"})

	var a, b bytes.Buffer
	ctx := context.Background()
	_ = f.Run(ctx, run.Cmd{Name: "pct", Args: []string{"status", "105"}, Stdout: &a, Env: []string{"K=V"}, Stdin: strings.NewReader("in")})
	_ = f.Run(ctx, run.Cmd{Name: "pct", Args: []string{"status", "105"}, Stdout: &b})
	err := f.Run(ctx, run.Cmd{Name: "pct", Args: []string{"start", "105"}})

	if a.String() != "status: running\n" || b.String() != "status: stopped\n" {
		t.Fatalf("outputs %q %q", a.String(), b.String())
	}
	var ee *run.ExitError
	if !errors.As(err, &ee) || ee.Code != 1 {
		t.Fatalf("err = %v", err)
	}
	calls := f.Calls()
	if len(calls) != 3 || calls[0].Stdin != "in" || !slices.Equal(calls[0].Env, []string{"K=V"}) {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestFakeFailsOnUnexpected(t *testing.T) {
	rec := &recorder{TB: t}
	f := &Fake{T: rec}
	err := f.Run(context.Background(), run.Cmd{Name: "rm", Args: []string{"-rf", "/"}})
	if !errors.Is(err, ErrUnexpected) || len(rec.errs) != 1 {
		t.Fatalf("err = %v, reported = %v", err, rec.errs)
	}
}

func TestFakePipeFeedsSourceOutputToDestination(t *testing.T) {
	f := &Fake{T: t}
	f.On([]string{"dump"}, Response{Stdout: "archive"})
	f.On([]string{"recv"}, Response{})
	if err := f.Pipe(context.Background(), run.Cmd{Name: "dump"}, run.Cmd{Name: "recv"}); err != nil {
		t.Fatal(err)
	}
	if c := f.Calls(); len(c) != 2 || c[1].Stdin != "archive" {
		t.Fatalf("calls = %+v", c)
	}
}
