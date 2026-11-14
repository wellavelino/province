package tfgen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEscape(t *testing.T) {
	cases := map[string]string{
		"":                                    "",
		"plain":                               "plain",
		"${path.root}":                        "$${path.root}",
		"%{if true}x%{endif}":                 "%%{if true}x%%{endif}",
		"$${already}":                         "$$${already}",
		"%%{already}":                         "%%%{already}",
		"$ { % { $} %} {":                     "$ { % { $} %} {",
		"a${b}c%{d}e${f}":                     "a$${b}c%%{d}e$${f}",
		"ends with $ and %":                   "ends with $ and %",
		"dev@laptop ${file(\"/etc/passwd\")}": "dev@laptop $${file(\"/etc/passwd\")}",
	}
	for in, want := range cases {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNoPlainStrings walks the generated document types: every string
// must be a lit (escaped) or an expr (written by this package), so a new
// field cannot reach the output unescaped.
func TestNoPlainStrings(t *testing.T) {
	litT, exprT := reflect.TypeFor[lit](), reflect.TypeFor[expr]()
	seen := map[reflect.Type]bool{}
	var walk func(path string, ty reflect.Type)
	walk = func(path string, ty reflect.Type) {
		if ty == litT || ty == exprT || seen[ty] {
			return
		}
		seen[ty] = true
		switch ty.Kind() {
		case reflect.String:
			t.Errorf("%s is a plain %s; use lit or expr", path, ty)
		case reflect.Struct:
			for i := range ty.NumField() {
				f := ty.Field(i)
				walk(path+"."+f.Name, f.Type)
			}
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(path+"[]", ty.Elem())
		case reflect.Map:
			walk(path+"{}", ty.Elem())
		case reflect.Interface:
			t.Errorf("%s is an interface; its strings would not be escaped", path)
		}
	}
	walk("file", reflect.TypeFor[file]())
	walk("incusFile", reflect.TypeFor[incusFile]())
}

// nasty has no whitespace: the provider rejects it in some fields. Left
// unescaped, either sequence fails tofu validate.
const nasty = "${path.root}%{endif}"

// TestEscapingGolden feeds template sequences through every config-derived
// field and checks that none reaches the output unescaped.
func TestEscapingGolden(t *testing.T) {
	l := base()
	l.Node = "pve1" + nasty
	// The provider checks the endpoint is a URL, where "%{" is not valid.
	l.Endpoint = "https://pve1.example:8006/${path.root}"
	l.Template = "local:vztmpl/debian-13-" + nasty + ".tar.zst"
	l.Storage = "local-lvm" + nasty
	l.Bridge = "vmbr0" + nasty
	l.Keys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAEIDxYdJCsyOUBHTlVcY2pxeH+GjZSboqmwt77FzNPa dev " + nasty}
	lb, err := Generate(l)
	if err != nil {
		t.Fatal(err)
	}
	i := incusBase()
	i.Remote, i.Project, i.Image = "box"+nasty, "dev"+nasty, "images:debian/13"+nasty
	i.Pool, i.Network = "default"+nasty, "incusbr0"+nasty
	ib, err := GenerateIncus(i)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]int{"escaping": 6, "incus-escaping": 5}
	for name, got := range map[string][]byte{"escaping": lb, "incus-escaping": ib} {
		path := filepath.Join("testdata", name+".golden.json")
		if *update {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: output differs from %s (run go test ./internal/tfgen -update after checking)\n%s", name, path, got)
		}
		var doc map[string]any
		if err := json.Unmarshal(got, &doc); err != nil {
			t.Fatal(err)
		}
		n := 0
		walkStrings(doc, func(s string) {
			if !strings.Contains(s, "path.root") {
				return
			}
			n++
			rest := strings.NewReplacer("$${", "", "%%{", "").Replace(s)
			if strings.Contains(rest, "${") || strings.Contains(rest, "%{") {
				t.Errorf("%s: unescaped template in %q", name, s)
			}
		})
		if n != fields[name] {
			t.Errorf("%s: %d escaped values, want %d", name, n, fields[name])
		}
	}
}

func walkStrings(v any, f func(string)) {
	switch v := v.(type) {
	case string:
		f(v)
	case map[string]any:
		for _, e := range v {
			walkStrings(e, f)
		}
	case []any:
		for _, e := range v {
			walkStrings(e, f)
		}
	}
}
