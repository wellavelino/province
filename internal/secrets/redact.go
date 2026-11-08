package secrets

import (
	"bytes"
	"io"
	"sort"
	"strings"
	"sync"
)

// Mask replaces every secret value.
const Mask = "[redacted]"

// Redactor scrubs secret values from text. It is safe for concurrent use.
type Redactor struct {
	mu     sync.RWMutex
	values []string // longest first, so a value never leaves a fragment of a longer one
}

// NewRedactor scrubs the given values. Empty values are ignored.
func NewRedactor(values ...string) *Redactor {
	r := &Redactor{}
	r.Add(values...)
	return r
}

// Add registers more secret values. Empty values are ignored.
func (r *Redactor) Add(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			r.values = append(r.values, v)
		}
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
}

// AddToken registers an API token and its bare secret part.
func (r *Redactor) AddToken(token string) {
	token = strings.TrimSpace(token)
	r.Add(token, TokenSecret(token))
}

// FromEnvValues builds a Redactor over every secret variable's raw value,
// valid or not, plus the bare secret of the API token.
func FromEnvValues(getenv func(string) string) *Redactor {
	var vals []string
	for _, n := range Names {
		vals = append(vals, getenv(n))
	}
	vals = append(vals, TokenSecret(strings.TrimSpace(getenv(EnvAPIToken))))
	return NewRedactor(vals...)
}

// Scrub returns s with every secret replaced by Mask.
func (r *Redactor) Scrub(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, Mask)
	}
	return s
}

// Writer returns a writer that scrubs each line before passing it to w.
// Secrets never contain newlines, so line buffering catches values split
// across writes. Call Flush to emit a trailing partial line.
func (r *Redactor) Writer(w io.Writer) *Writer {
	return &Writer{r: r, w: w}
}

// Writer is a line-buffered scrubbing writer, safe for concurrent use.
type Writer struct {
	mu  sync.Mutex
	r   *Redactor
	w   io.Writer
	buf []byte
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	i := bytes.LastIndexByte(w.buf, '\n')
	if i < 0 {
		return len(p), nil
	}
	out := w.r.Scrub(string(w.buf[:i+1]))
	w.buf = append(w.buf[:0], w.buf[i+1:]...)
	if _, err := io.WriteString(w.w, out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes any buffered partial line.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) == 0 {
		return nil
	}
	out := w.r.Scrub(string(w.buf))
	w.buf = w.buf[:0]
	_, err := io.WriteString(w.w, out)
	return err
}
