package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestGuestOutputKeptApartFromMessage(t *testing.T) {
	base := WithGuestOutput(Preflightf("guest reported a bad name"), "evil\x1bname")
	err := fmt.Errorf("expose demo: %w", base)
	if err.Error() != "expose demo: guest reported a bad name" {
		t.Fatalf("message = %q", err)
	}
	if got := GuestOutput(err); got != "evil\x1bname" {
		t.Fatalf("guest output = %q", got)
	}
	if KindOf(err) != Preflight {
		t.Fatalf("kind = %s", KindOf(err))
	}
	if got := GuestOutput(errors.Join(errors.New("other"), err)); got != "evil\x1bname" {
		t.Fatalf("joined: guest output = %q", got)
	}
	if GuestOutput(errors.New("plain")) != "" || GuestOutput(nil) != "" {
		t.Fatal("guest output on an error without any")
	}
}
