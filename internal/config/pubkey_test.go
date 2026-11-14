package config

import (
	"encoding/base64"
	"encoding/binary"
	"math/big"
	"strings"
	"testing"
)

// realKeys are public keys made with ssh-keygen (-t ed25519, rsa -b 2048,
// ecdsa -b 256/384/521); their private halves were discarded.
var realKeys = map[string]string{
	"ssh-ed25519":         "AAAAC3NzaC1lZDI1NTE5AAAAIIyWWm+QD6WeV6h1o5OvXB6+p3LzY/XyM1+Js4PIFuSY",
	"ssh-rsa":             "AAAAB3NzaC1yc2EAAAADAQABAAABAQDKYgBXwrkr39fe6+g/P3SYYS8CGcsWh4h9NFgmj04scBqvogaytvih6Z2tdl5mIwBeXgXJ6R8JGTWLQ4qrRIQi2qGGsarQczE1WoZUchyUij9CZz2nxCmjigel4BV9PZtZjeMBpqjvJ20NUmj3Q/t1fd+meZuKTj92O3qJS1SXYiT/8m7h0zyMDgoG1z1F2Ec5TOJWoka0r+iuVTICVV7j9fvftKXvQig+uXYZQ67wlsoc8gkk9FBih/aSE1wUSKxOvP6z0BJ87lVwH/fLo1mwM3NiVm6CtR977Lkv50PbqWudsexi+9obVd49Ct3t36nPyuw24a7AFiKoHJ2TywAR",
	"ecdsa-sha2-nistp256": "AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBODmXVqmD6KPA3R+owcyUIaJGI9wwoKQLVlVbC69p+HhPg2RuFlNj7GX1cY6ZV3zpNpYKRcLB/hFMfXKX/hJJ2c=",
	"ecdsa-sha2-nistp384": "AAAAE2VjZHNhLXNoYTItbmlzdHAzODQAAAAIbmlzdHAzODQAAABhBLkYoCNS/lOUaqWo9xzCK6xE68lYnaIqJXXAh6RE3eJQyO2sU9Erv490eGfslJfhyDHp4rSkuAEPDPQ6cM9DcVpNlCvnNLbT5rhFbPV1u6VnxxK7hhTRWOzOdwQKl0Ad6A==",
	"ecdsa-sha2-nistp521": "AAAAE2VjZHNhLXNoYTItbmlzdHA1MjEAAAAIbmlzdHA1MjEAAACFBACmK/Lp7hOtZShp3AH/3/hpUWBJV7zekYziRH+xWw6zx9I2JUyPk71OR+unBIU2qP7WE9J8nKZYjePl4pV1C76HPgF1ui5Cdd5LypTUNC8Gr40nflBGdugfuHV52xFwAhOcxwXOGXI6PJXxDjNM1rGsxIdjKmCTXp2b4g93iXmTe+s3JA==",
}

// enc encodes fields as SSH wire strings, the way key blobs are built.
func enc(fields ...[]byte) []byte {
	var b []byte
	for _, f := range fields {
		b = binary.BigEndian.AppendUint32(b, uint32(len(f)))
		b = append(b, f...)
	}
	return b
}

// fields decodes the wire strings of realKeys[typ].
func fields(t *testing.T, typ string) [][]byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(realKeys[typ])
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for len(raw) > 0 {
		n := binary.BigEndian.Uint32(raw)
		out = append(out, raw[4:4+n])
		raw = raw[4+n:]
	}
	return out
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// keyBlobs is a structurally valid blob of every accepted type. Security
// keys cannot be made without a device, so their blobs are built from an
// ed25519 key and a P-256 point and an application string.
func keyBlobs(t *testing.T) map[string]string {
	t.Helper()
	m := map[string]string{}
	for typ, b := range realKeys {
		m[typ] = b
	}
	ed, p256 := fields(t, "ssh-ed25519"), fields(t, "ecdsa-sha2-nistp256")
	m["sk-ssh-ed25519@openssh.com"] = b64(enc([]byte("sk-ssh-ed25519@openssh.com"), ed[1], []byte("ssh:")))
	m["sk-ecdsa-sha2-nistp256@openssh.com"] = b64(enc([]byte("sk-ecdsa-sha2-nistp256@openssh.com"), p256[1], p256[2], []byte("ssh:")))
	return m
}

func TestCheckPublicKeyAccepts(t *testing.T) {
	blobs := keyBlobs(t)
	blob := func(typ string) string { return blobs[typ] }
	for _, typ := range []string{
		"ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com",
	} {
		for _, line := range []string{
			typ + " " + blob(typ),
			typ + " " + blob(typ) + " dev@laptop",
			typ + " " + blob(typ) + " ci key, rotated 2026",
			typ + "\t" + blob(typ) + "\tdev@laptop",
		} {
			if err := CheckPublicKey(line); err != nil {
				t.Errorf("CheckPublicKey(%q) = %v", line, err)
			}
		}
	}
}

func TestCheckPublicKeyRejects(t *testing.T) {
	ed := realKeys["ssh-ed25519"]
	edF, p256, p384, rsaF := fields(t, "ssh-ed25519"), fields(t, "ecdsa-sha2-nistp256"), fields(t, "ecdsa-sha2-nistp384"), fields(t, "ssh-rsa")
	typ := func(s string) []byte { return []byte(s) }
	offCurve := append([]byte(nil), p256[2]...)
	offCurve[len(offCurve)-1] ^= 1
	compressed := append([]byte{2}, p256[2][1:33]...)
	small := new(big.Int).Lsh(big.NewInt(1), 1022) // 1023 bits
	smallN := append([]byte{0}, small.Bytes()...)
	malformed := map[string]string{
		"type string only":        "ssh-ed25519 " + b64(enc(typ("ssh-ed25519"))),
		"ed25519 key of 31 bytes": "ssh-ed25519 " + b64(enc(typ("ssh-ed25519"), edF[1][:31])),
		"ed25519 trailing bytes":  "ssh-ed25519 " + b64(append(enc(typ("ssh-ed25519"), edF[1]), 0)),
		"ed25519 extra field":     "ssh-ed25519 " + b64(enc(typ("ssh-ed25519"), edF[1], typ("x"))),
		"sk-ed25519 no app":       "sk-ssh-ed25519@openssh.com " + b64(enc(typ("sk-ssh-ed25519@openssh.com"), edF[1])),
		"ecdsa other curve":       "ecdsa-sha2-nistp256 " + b64(enc(typ("ecdsa-sha2-nistp256"), p384[1], p384[2])),
		"ecdsa curve name only":   "ecdsa-sha2-nistp256 " + b64(enc(typ("ecdsa-sha2-nistp256"), p256[1])),
		"ecdsa empty point":       "ecdsa-sha2-nistp256 " + b64(enc(typ("ecdsa-sha2-nistp256"), p256[1], nil)),
		"ecdsa compressed point":  "ecdsa-sha2-nistp256 " + b64(enc(typ("ecdsa-sha2-nistp256"), p256[1], compressed)),
		"ecdsa short point":       "ecdsa-sha2-nistp256 " + b64(enc(typ("ecdsa-sha2-nistp256"), p256[1], p256[2][:64])),
		"ecdsa point off curve":   "ecdsa-sha2-nistp256 " + b64(enc(typ("ecdsa-sha2-nistp256"), p256[1], offCurve)),
		"ecdsa trailing bytes":    "ecdsa-sha2-nistp256 " + b64(append(enc(typ("ecdsa-sha2-nistp256"), p256[1], p256[2]), 0)),
		"sk-ecdsa no app":         "sk-ecdsa-sha2-nistp256@openssh.com " + b64(enc(typ("sk-ecdsa-sha2-nistp256@openssh.com"), p256[1], p256[2])),
		"rsa no modulus":          "ssh-rsa " + b64(enc(typ("ssh-rsa"), rsaF[1])),
		"rsa empty exponent":      "ssh-rsa " + b64(enc(typ("ssh-rsa"), nil, rsaF[2])),
		"rsa negative modulus":    "ssh-rsa " + b64(enc(typ("ssh-rsa"), rsaF[1], rsaF[2][1:])),
		"rsa 1023-bit modulus":    "ssh-rsa " + b64(enc(typ("ssh-rsa"), rsaF[1], smallN[1:])),
		"rsa non-minimal mpint":   "ssh-rsa " + b64(enc(typ("ssh-rsa"), append([]byte{0}, rsaF[1]...), rsaF[2])),
		"rsa trailing bytes":      "ssh-rsa " + b64(append(enc(typ("ssh-rsa"), rsaF[1], rsaF[2]), 0)),
	}
	for name, line := range malformed {
		if err := CheckPublicKey(line); err == nil || !strings.Contains(err.Error(), "not a well-formed") {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// secret stands in for key material that must never be echoed back.
	const secret = "b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAAB"
	cases := map[string]struct{ line, want string }{
		"private key header":    {"-----BEGIN OPENSSH PRIVATE KEY-----", "private key"},
		"pem private key":       {"-----BEGIN RSA PRIVATE KEY----- " + secret, "private key"},
		"private key body line": {secret, "not a public key line"},
		"multi-line":            {"ssh-ed25519 " + ed + "\n" + secret, "line break"},
		"carriage return":       {"ssh-ed25519 " + ed + "\r" + secret, "control character"},
		"nul":                   {"ssh-ed25519 " + ed + " dev\x00" + secret, "control character"},
		"escape":                {"ssh-ed25519 " + ed + " \x1b[2J" + secret, "control character"},
		"type only":             {"ssh-ed25519", "not a public key line"},
		"empty":                 {"", "not a public key line"},
		"unknown type":          {"ssh-dss " + secret, "unknown key type"},
		"options prefix":        {`command="sh" ssh-ed25519 ` + ed + " " + secret, "unknown key type"},
		"bad base64":            {"ssh-ed25519 " + secret + "!!", "not valid base64"},
		"cut base64":            {"ssh-ed25519 " + ed[:len(ed)-1], "not valid base64"},
		"truncated blob":        {"ssh-ed25519 AAA=", "truncated"},
		"type mismatch":         {"ssh-ed25519 " + realKeys["ssh-rsa"] + " " + secret, "does not match"},
		"length past the end":   {"ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte{0, 0, 1, 0, 's'}), "does not match"},
	}
	for name, c := range cases {
		err := CheckPublicKey(c.line)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), ed) {
			t.Errorf("%s: error echoes the value: %v", name, err)
		}
	}
}
