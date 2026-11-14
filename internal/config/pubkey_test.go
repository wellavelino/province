package config

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

// blob builds a key blob that names typ, followed by some key bytes.
func blob(typ string) string {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(typ)))
	b = append(b, typ...)
	b = binary.BigEndian.AppendUint32(b, 32)
	for i := range 32 {
		b = append(b, byte(i*7+1))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestCheckPublicKeyAccepts(t *testing.T) {
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
	ed := blob("ssh-ed25519")
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
		"type mismatch":         {"ssh-ed25519 " + blob("ssh-rsa") + " " + secret, "does not match"},
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
