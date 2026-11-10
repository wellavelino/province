package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
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
		return a.print(escapeControl(buf.String()))
	}
	return a.print(buf.String())
}

// escapeControl makes text safe for a terminal: stored strings, some of
// them reported by a guest, must not move the cursor, clear lines or
// reorder text. Control characters other than newline and tab, and the
// bidi overrides, are written as \xNN or \uNNNN.
func escapeControl(s string) string {
	if !strings.ContainsFunc(s, unsafeRune) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case !unsafeRune(r):
			b.WriteRune(r)
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

func unsafeRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
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
