package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wellavelino/province/internal/errs"
)

// errorDoc is a failure under --json. Error is province's own message;
// GuestOutput is text a guest produced (stderr of a command run inside the
// env, a value it reported), kept apart because it is data.
type errorDoc struct {
	Error       string    `json:"error"`
	Code        errs.Kind `json:"code"`
	GuestOutput string    `json:"guest_output,omitempty"`
}

// writeError reports err, scrubbed: as the single JSON document on stdout
// with --json, else as a line on stderr, followed by any guest output,
// labelled and indented.
func (a *app) writeError(err error) {
	msg := a.red.Scrub(err.Error())
	guest := a.red.Scrub(errs.GuestOutput(err))
	if a.json {
		b, _ := json.Marshal(errorDoc{Error: msg, Code: errs.KindOf(err), GuestOutput: guest})
		_ = a.print(string(b) + "\n")
		return
	}
	fmt.Fprintf(a.stderr, "province: %s\n", escapeControl(msg))
	if guest != "" {
		fmt.Fprintf(a.stderr, "guest output:\n  %s\n", strings.ReplaceAll(escapeControl(guest), "\n", "\n  "))
	}
}
