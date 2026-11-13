package main

import (
	"bytes"
	"testing"
)

// TestProxyQuote guards the quoting of a ProxyCommand word, which ssh hands to
// a shell after expanding its % sequences: the word is shell-quoted, and every
// % is doubled so that ssh leaves it alone.
func TestProxyQuote(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/plain/path", "'/plain/path'"},
		{"/a b/c", "'/a b/c'"},
		{"/it's", "'/it'\"'\"'s'"},
		{"/x%h", "'/x%%h'"},
		{"/a'%b", "'/a'\"'\"'%%b'"},
	} {
		if got := proxyQuote(c.in); got != c.want {
			t.Errorf("proxyQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSSHConfigRefusesInvalidName guards the unquoted machine name that the
// printed ProxyCommand carries: a.machine refuses every name that
// protocol.ValidName refuses, before it reads a record or prints anything.
func TestSSHConfigRefusesInvalidName(t *testing.T) {
	a, _ := testApp(t)
	var out bytes.Buffer
	a.out = &out
	// a.machine's own refusal, so a later "no machine named" does not pass.
	const refused = "machine names are 1–24 lowercase letters, digits or interior hyphens, starting with a letter"
	for _, name := range []string{"x;id", "$(id)", "a b", "Bad", "../x", "x%h"} {
		if err := a.sshConfig([]string{name}); err == nil || err.Error() != refused {
			t.Errorf("ssh-config(%q): got %v, want the machine-name refusal", name, err)
		}
		if out.Len() != 0 {
			t.Errorf("ssh-config(%q) printed %q", name, out.String())
			out.Reset()
		}
	}
}
