// Package secrets reads secrets from the environment and scrubs them from
// any text province prints.
package secrets

import (
	"fmt"
	"regexp"
	"strings"
)

// Environment variable names.
const (
	EnvAPIToken = "PROXMOX_VE_API_TOKEN" // user@realm!id=secret
	EnvAuthKey  = "TS_AUTHKEY"           // Tailscale auth key for new guests
	EnvAPIKey   = "TS_API_KEY"           // optional: Tailscale API key, deletes nodes on destroy
	EnvTailnet  = "TS_TAILNET"           // optional, default "-" (the key's tailnet)
)

// Names lists every variable holding a secret value.
var Names = []string{EnvAPIToken, EnvAuthKey, EnvAPIKey}

// Secrets are the Tailscale values province needs. Proxmox API tokens are
// per host and read with Token.
type Secrets struct {
	AuthKey string
	APIKey  string
	Tailnet string
}

var tokenRE = regexp.MustCompile(`^[^@\s]+@[^!\s]+![^=\s]+=\S+$`)

// FromEnv reads the Tailscale secrets; all are optional here.
func FromEnv(getenv func(string) string) Secrets {
	s := Secrets{
		AuthKey: strings.TrimSpace(getenv(EnvAuthKey)),
		APIKey:  strings.TrimSpace(getenv(EnvAPIKey)),
		Tailnet: strings.TrimSpace(getenv(EnvTailnet)),
	}
	if s.Tailnet == "" {
		s.Tailnet = "-"
	}
	return s
}

// Token returns the API token held by env var name for host, which must be
// set and look like user@realm!id=secret. Errors name the variable and the
// host, never the value.
func Token(getenv func(string) string, name, host string) (string, error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return "", fmt.Errorf("%s (API token for host %s) is not set (format user@realm!tokenid=secret)", name, host)
	}
	if !tokenRE.MatchString(v) {
		return "", fmt.Errorf("%s (API token for host %s) is malformed: want user@realm!tokenid=secret", name, host)
	}
	return v, nil
}

// TokenSecret returns the part of an API token after the first "=", so the
// bare secret is scrubbed too.
func TokenSecret(token string) string {
	if _, after, ok := strings.Cut(token, "="); ok {
		return after
	}
	return ""
}
