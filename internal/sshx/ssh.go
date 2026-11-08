// Package sshx builds ssh invocations of remote commands.
package sshx

import (
	"strings"

	"github.com/wellavelino/province/internal/run"
)

// Target is a host reached with the system ssh binary, so ~/.ssh/config
// aliases apply. Sudo prefixes remote commands with "sudo -n".
type Target struct {
	Dest string
	Sudo bool
}

// safe reports whether s needs no quoting for a POSIX shell.
func safe(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("@%+=:,./_-", r):
		default:
			return false
		}
	}
	return true
}

// Quote returns s as one word for a POSIX shell. Words made only of safe
// characters are returned unchanged; anything else is single-quoted, with
// each embedded single quote written as: close quote, backslash-quote, reopen.
func Quote(s string) string {
	if safe(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Command returns the ssh invocation that runs remote on t. ssh joins the
// remote words with spaces and the remote shell splits them again, so every
// word is quoted here.
func Command(t Target, remote ...string) run.Cmd {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=30",
		"--", t.Dest,
	}
	if t.Sudo {
		args = append(args, "sudo", "-n")
	}
	for _, w := range remote {
		args = append(args, Quote(w))
	}
	return run.Cmd{Name: "ssh", Args: args}
}
