package tfgen

import (
	"encoding/json"
	"strings"
)

// Every string the generated config carries is typed lit or expr, never a
// plain string (TestNoPlainStrings enforces this), so a new field cannot
// skip escaping by accident. Map keys are not escaped: in this package
// they are always constants.

// lit is a literal string value, such as config- or user-derived text,
// that tofu must read as is, never as a template.
type lit string

// MarshalJSON writes s with its template sequences escaped.
func (s lit) MarshalJSON() ([]byte, error) { return json.Marshal(Escape(string(s))) }

// expr is a tofu template or keyword this package writes itself, such as
// an output's "${resource.attr}" or a lifecycle attribute name. Never
// build one from config- or user-derived text.
type expr string

var escaper = strings.NewReplacer("${", "$${", "%{", "%%{")

// Escape makes s a literal inside a tofu JSON string: "${" becomes "$${"
// and "%{" becomes "%%{", so tofu reads neither as an interpolation or a
// directive.
func Escape(s string) string { return escaper.Replace(s) }

// lits converts config-derived strings to literals.
func lits(ss []string) []lit {
	if ss == nil {
		return nil
	}
	out := make([]lit, len(ss))
	for i, s := range ss {
		out[i] = lit(s)
	}
	return out
}
