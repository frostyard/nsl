package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

// A remote editor reaches a machine with a key generated for it and a proxy
// command that runs sshd -i in the machine through the agent: nothing listens.

func (a *app) sshDir(name string) string { return filepath.Join(a.machinesDir(), name+".ssh") }

// machineKey creates the machine's client key on first use and returns its
// public half.
func (a *app) machineKey(m *machineRecord) (string, error) {
	dir := a.sshDir(m.Name)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		temporary, err := os.MkdirTemp(a.machinesDir(), ".ssh-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(temporary)
		if err = a.call(nil, a.err, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nsl-"+m.Name, "-f", filepath.Join(temporary, "id_ed25519")); err != nil {
			return "", err
		}
		if err = os.Rename(temporary, dir); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err := checkPrivateDir(dir, a.uid); err != nil {
		return "", err
	}
	if err := privateFile(filepath.Join(dir, "id_ed25519"), a.uid, 0077); err != nil {
		return "", err
	}
	if err := privateFile(filepath.Join(dir, "id_ed25519.pub"), a.uid, 0022); err != nil {
		return "", err
	}
	public, err := os.ReadFile(filepath.Join(dir, "id_ed25519.pub"))
	return strings.TrimSpace(string(public)), err
}

// proxyQuote quotes a word for the shell that runs a ProxyCommand, whose %
// sequences ssh expands first.
func proxyQuote(s string) string { return strings.ReplaceAll(shellQuote(s), "%", "%%") }

func (a *app) sshConfig(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: ssh-config NAME")
	}
	m, err := a.machine(args[0])
	if err != nil {
		return err
	}
	if _, err = a.machineKey(m); err != nil {
		return err
	}
	if _, err = a.startMachine(m); err != nil {
		return err
	}
	dir := a.sshDir(m.Name)
	fmt.Fprintf(a.out, `Host nsl-%s
    HostName %s
    User %s
    IdentityFile %s
    IdentitiesOnly yes
    UserKnownHostsFile %s
    HostKeyAlias nsl-%s-%s
    StrictHostKeyChecking accept-new
    ProxyCommand env NSL_HOME=%s %s _ssh %s
`, m.Name, m.Name, m.User, sshQuote(filepath.Join(dir, "id_ed25519")), sshQuote(filepath.Join(dir, "known_hosts")), m.Name, m.ID,
		proxyQuote(a.home), proxyQuote(a.self), m.Name)
	return nil
}

// sshProxy is ssh-config's proxy command: its stdio carries the SSH protocol
// to sshd -i in the machine, so it prints nothing else.
func (a *app) sshProxy(args []string) error {
	if len(args) != 1 {
		return errors.New("internal ssh proxy requires a machine")
	}
	m, err := a.machine(args[0])
	if err != nil {
		return err
	}
	public, err := a.machineKey(m)
	if err != nil {
		return err
	}
	v, err := a.startMachine(m)
	if err != nil {
		return err
	}
	idle, err := a.idleTimeout()
	if err != nil {
		return err
	}
	encoded, err := protocol.Encode(protocol.Request{Protocol: protocol.Version, Op: "ssh", Machine: m.Name, ID: m.ID, PublicKey: public, IdleTimeout: idle})
	if err != nil {
		return err
	}
	return a.call(a.in, a.out, "ssh", append(a.sshArgs(v, false), encoded)...)
}

// logs shows recent journal entries of the host units nsl runs.
func (a *app) logs(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: logs [NAME]")
	}
	all, err := a.allVMs()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return errors.New("there is no nsl VM yet")
	}
	var units []string
	for _, v := range all {
		units = append(units, "-u", vmUnit(v), "-u", portsUnit(v), "-u", desktopUnit(v, "*"))
	}
	if len(args) == 1 {
		m, err := a.machine(args[0])
		if err != nil {
			return err
		}
		v, err := a.vmOf(m)
		if err != nil || v == nil {
			return errors.Join(fmt.Errorf("%s has no VM yet", m.Name), err)
		}
		// An isolated machine's VM is its own; a shared machine has its desktop.
		units = []string{"-u", desktopUnit(v, m.Name)}
		if m.Tier == "isolated" {
			units = append(units, "-u", vmUnit(v), "-u", portsUnit(v))
		}
		fmt.Fprintf(a.err, "nsl: the machine's own journal: nsl run -m %s --root journalctl -n 100\n", m.Name)
	}
	out, err := a.capture(30*time.Second, "journalctl", append([]string{"--user", "--no-pager", "-n", "200", "-o", "short-iso"}, units...)...)
	a.out.Write(out)
	return err
}
