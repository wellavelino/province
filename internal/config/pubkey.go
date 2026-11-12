package config

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"unicode"
)

// publicKeyTypes are the OpenSSH public key types accepted in
// ssh_public_keys files.
var publicKeyTypes = map[string]bool{
	"ssh-ed25519":                        true,
	"ssh-rsa":                            true,
	"ecdsa-sha2-nistp256":                true,
	"ecdsa-sha2-nistp384":                true,
	"ecdsa-sha2-nistp521":                true,
	"sk-ssh-ed25519@openssh.com":         true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
}

// CheckPublicKey checks one line of an ssh_public_keys file: a known
// OpenSSH key type, its base64 key blob (which must name the same type),
// and an optional comment, on one line with no control characters.
// authorized_keys options are not accepted. The error never quotes the
// line, which might be a private key: callers name the file and line.
func CheckPublicKey(line string) error {
	if strings.Contains(line, "PRIVATE KEY") {
		return errors.New("this is a private key; list the .pub file instead")
	}
	for _, r := range line {
		if unicode.IsControl(r) && r != '\t' {
			return errors.New("contains a control character or a line break")
		}
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return errors.New("not a public key line (want: type base64-key [comment])")
	}
	typ, blob := fields[0], fields[1]
	if !publicKeyTypes[typ] {
		return errors.New("unknown key type (want ssh-ed25519, ssh-rsa, ecdsa-sha2-nistp256/384/521, sk-ssh-ed25519@openssh.com or sk-ecdsa-sha2-nistp256@openssh.com; authorized_keys options are not accepted)")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(blob)
	if err != nil {
		return errors.New("the key is not valid base64")
	}
	if len(raw) < 4 {
		return errors.New("the key blob is truncated")
	}
	n := binary.BigEndian.Uint32(raw)
	if uint64(len(raw)-4) < uint64(n) || string(raw[4:4+n]) != typ {
		return errors.New("the key blob does not match its key type")
	}
	return nil
}
