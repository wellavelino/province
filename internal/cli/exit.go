package cli

import (
	"encoding/json"
	"fmt"

	"github.com/wellavelino/province/internal/errs"
)

type errorDoc struct {
	Error string    `json:"error"`
	Code  errs.Kind `json:"code"`
}

// writeError reports err, scrubbed: as the single JSON document on stdout
// with --json, else as a line on stderr.
func (a *app) writeError(err error) {
	msg := a.red.Scrub(err.Error())
	if a.json {
		b, _ := json.Marshal(errorDoc{Error: msg, Code: errs.KindOf(err)})
		_ = a.print(string(b) + "\n")
		return
	}
	fmt.Fprintf(a.stderr, "province: %s\n", escapeControl(msg))
}
