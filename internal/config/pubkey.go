package config

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math/big"
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
// OpenSSH key type, its base64 key blob (which must name the same type
// and hold that type's whole key, nothing more), and an optional comment, on one line with no control characters.
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
	r := wire(raw)
	if name, ok := r.str(); !ok || string(name) != typ {
		return errors.New("the key blob does not match its key type")
	}
	if !r.keyFor(typ) || len(r) != 0 {
		return errors.New("the key blob is not a well-formed " + typ + " key")
	}
	return nil
}

// minRSABits is the smallest RSA modulus accepted.
const minRSABits = 1024

// wire reads the SSH wire encoding (RFC 4251) of a public key blob.
type wire []byte

// str reads one string: a uint32 length and that many bytes.
func (w *wire) str() ([]byte, bool) {
	if len(*w) < 4 {
		return nil, false
	}
	n := binary.BigEndian.Uint32(*w)
	if uint64(len(*w)-4) < uint64(n) {
		return nil, false
	}
	s := (*w)[4 : 4+n]
	*w = (*w)[4+n:]
	return s, true
}

// mpint reads one positive multiple-precision integer in minimal form.
func (w *wire) mpint() (*big.Int, bool) {
	b, ok := w.str()
	if !ok || len(b) == 0 || b[0]&0x80 != 0 || (b[0] == 0 && (len(b) == 1 || b[1]&0x80 == 0)) {
		return nil, false
	}
	return new(big.Int).SetBytes(b), true
}

// keyFor reads the key fields of typ, after the type name (RFC 8709,
// RFC 5656, RFC 4253 and OpenSSH's PROTOCOL.u2f).
func (w *wire) keyFor(typ string) bool {
	switch typ {
	case "ssh-ed25519":
		k, ok := w.str()
		return ok && len(k) == 32
	case "sk-ssh-ed25519@openssh.com":
		k, ok := w.str()
		_, app := w.str()
		return ok && len(k) == 32 && app
	case "ssh-rsa":
		e, ok1 := w.mpint()
		n, ok2 := w.mpint()
		return ok1 && ok2 && e.Sign() > 0 && n.BitLen() >= minRSABits
	case "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
		return w.ecPoint(strings.TrimPrefix(typ, "ecdsa-sha2-"))
	case "sk-ecdsa-sha2-nistp256@openssh.com":
		if !w.ecPoint("nistp256") {
			return false
		}
		_, app := w.str()
		return app
	}
	return false
}

// ecPoint reads the curve name, which must be curve, and an uncompressed
// point on it.
func (w *wire) ecPoint(curve string) bool {
	c := map[string]ecdh.Curve{"nistp256": ecdh.P256(), "nistp384": ecdh.P384(), "nistp521": ecdh.P521()}[curve]
	name, ok1 := w.str()
	q, ok2 := w.str()
	if !ok1 || !ok2 || string(name) != curve || len(q) == 0 || q[0] != 4 {
		return false
	}
	_, err := c.NewPublicKey(q) // checks the length and that q is on the curve
	return err == nil
}
