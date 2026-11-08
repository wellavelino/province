package cli

import (
	"bytes"
	"encoding/json"
	"io"
)

// emit prints a command result: one JSON document with --json, else the
// human rendering. Either way the text is scrubbed of secrets first.
func (a *app) emit(result any, human func(w io.Writer)) error {
	var buf bytes.Buffer
	if a.json {
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	} else {
		human(&buf)
	}
	return a.print(buf.String())
}

func (a *app) print(s string) error {
	_, err := io.WriteString(a.stdout, a.red.Scrub(s))
	return err
}

// redacted is stdout for cobra's own output (help, usage).
type redacted struct{ a *app }

func (r redacted) Write(p []byte) (int, error) {
	if err := r.a.print(string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}
