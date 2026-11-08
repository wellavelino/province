package sshx

import (
	"slices"
	"testing"
)

func TestQuote(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk+/9Qx= dev@laptop"
	cases := []struct{ in, want string }{
		{"", "''"},
		{"pct", "pct"},
		{"/var/lib/vz/dump/x.tar.zst", "/var/lib/vz/dump/x.tar.zst"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{"$HOME", "'$HOME'"},
		{"`id`", "'`id`'"},
		{"a\nb", "'a\nb'"},
		{"a;rm -rf /", "'a;rm -rf /'"},
		{"*", "'*'"},
		{key, "'" + key + "'"},
	}
	for _, c := range cases {
		if got := Quote(c.in); got != c.want {
			t.Errorf("Quote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCommandArgv(t *testing.T) {
	c := Command(Target{Dest: "pve1"}, "pct", "exec", "105", "--", "sh", "-c", "echo $X")
	want := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=30",
		"--", "pve1", "pct", "exec", "105", "--", "sh", "-c", "'echo $X'"}
	if !slices.Equal(c.Argv(), want) {
		t.Fatalf("argv = %q\nwant   %q", c.Argv(), want)
	}
	if c.Env != nil || c.Stdin != nil {
		t.Fatalf("Command must not set Env or Stdin: %+v", c)
	}
}

func TestCommandSudoOnlyWhenAsked(t *testing.T) {
	plain := Command(Target{Dest: "pve1"}, "pct", "list")
	if slices.Contains(plain.Args, "sudo") {
		t.Fatalf("sudo without Target.Sudo: %q", plain.Args)
	}
	s := Command(Target{Dest: "pve1", Sudo: true}, "pct", "list")
	i := slices.Index(s.Args, "pve1")
	if got := s.Args[i+1:]; !slices.Equal(got, []string{"sudo", "-n", "pct", "list"}) {
		t.Fatalf("remote words = %q", got)
	}
}

func TestCommandDestCannotBeAnOption(t *testing.T) {
	// "--" ends ssh's options, so a hostile Dest is never parsed as one.
	c := Command(Target{Dest: "-oProxyCommand=x"}, "true")
	i := slices.Index(c.Args, "--")
	if i < 0 || c.Args[i+1] != "-oProxyCommand=x" {
		t.Fatalf("dest not after --: %q", c.Args)
	}
}
