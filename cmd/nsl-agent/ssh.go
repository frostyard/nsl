package main

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/frostyard/nsl/internal/protocol"
)

// The machine's own SSH identity and the host's key for ssh-config live in
// /etc/ssh/nsl, apart from the distro's sshd configuration.
const sshDir = "etc/ssh/nsl"

// sshd serves one SSH connection with sshd -i in the machine, as root, so that
// sshd can authenticate the host's key and switch to the machine account.
func (a *agent) sshd(req *protocol.Request) (int, error) {
	rec, err := a.runnable(req)
	if err != nil {
		return 0, err
	}
	if err = a.sshKeys(req.Machine, req.PublicKey); err != nil {
		return 0, err
	}
	spec := unitSpec{User: "root", Directory: "/root", SearchPath: searchPath("/root"), RuntimeDir: "sshd", Argv: []string{"sshd", "-i",
		"-o", "HostKey=/" + sshDir + "/ssh_host_ed25519_key", "-o", "AuthorizedKeysFile=/" + sshDir + "/authorized_keys",
		"-o", "AllowUsers=" + rec.Account.User, "-o", "PermitRootLogin=no", "-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no", "-o", "PubkeyAuthentication=yes"}}
	return a.runUnit(req.Machine, spec, false)
}

// sshKeys writes the host's key for this connection, and creates the machine's
// host key on first use. Both stay inside the machine's tree.
func (a *agent) sshKeys(name, publicKey string) error {
	t, err := openTree(a.path(machinesDir + "/" + name))
	if err != nil {
		return err
	}
	defer t.close()
	for _, dir := range []string{"etc/ssh", sshDir} {
		if err = t.mkdir(dir, 0755); err != nil {
			return err
		}
	}
	if err = t.writeFile(sshDir+"/authorized_keys", []byte(publicKey+"\n"), 0644); err != nil {
		return err
	}
	if _, err = t.readFile(sshDir + "/ssh_host_ed25519_key.pub"); err == nil {
		return nil
	}
	temporary, err := os.MkdirTemp(a.path(runtimeDir), ".ssh-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	key := filepath.Join(temporary, "key")
	if _, err = a.output("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nsl-"+name, "-f", key); err != nil {
		return err
	}
	private, err := os.ReadFile(key)
	if err != nil {
		return err
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		return err
	}
	// The private half first: a public key without it would stop regeneration.
	if err = t.writeFile(sshDir+"/ssh_host_ed25519_key", private, 0600); err != nil {
		return err
	}
	return t.writeFile(sshDir+"/ssh_host_ed25519_key.pub", bytes.TrimSpace(public), 0644)
}
