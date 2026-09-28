package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/frostyard/nsl/internal/protocol"
	"golang.org/x/sys/unix"
)

const dataLabel = "nsl-data"

func (a *agent) output(argv ...string) (string, error) {
	var out, stderr bytes.Buffer
	if err := a.r.run(context.Background(), nil, &out, &stderr, argv...); err != nil {
		return "", fmt.Errorf("%s: %w: %s", argv[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

type blockDevice struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	RO       json.RawMessage `json:"ro"`
	FSType   *string         `json:"fstype"`
	Label    *string         `json:"label"`
	Children []blockDevice   `json:"children"`
}

func (d blockDevice) readOnly() bool {
	s := string(d.RO)
	return s == "true" || s == `"1"` || s == "1"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// storage prepares the data disk before it is mounted: a blank disk becomes
// btrfs nsl-data with its subvolumes; any other signature is refused.
func (a *agent) storage() error {
	a.output("udevadm", "settle", "--timeout=30")
	source, err := a.output("findmnt", "--noheadings", "--nofsroot", "--output", "SOURCE", "/")
	if err != nil {
		return err
	}
	rootDisk, err := a.output("lsblk", "--noheadings", "--nodeps", "--paths", "--output", "PKNAME", source)
	if err != nil {
		return err
	}
	if rootDisk == "" {
		rootDisk = source
	}
	listing, err := a.output("lsblk", "--json", "--paths", "--output", "NAME,TYPE,RO,FSTYPE,LABEL")
	if err != nil {
		return err
	}
	var devices struct {
		BlockDevices []blockDevice `json:"blockdevices"`
	}
	if err = json.Unmarshal([]byte(listing), &devices); err != nil {
		return fmt.Errorf("lsblk: %w", err)
	}
	var candidates, labelled []blockDevice
	for _, d := range devices.BlockDevices {
		if d.Type == "disk" && !d.readOnly() && d.Name != rootDisk && len(d.Children) == 0 {
			candidates = append(candidates, d)
			if deref(d.Label) == dataLabel {
				labelled = append(labelled, d)
			}
		}
	}
	if len(labelled) > 0 {
		if len(labelled) != 1 || deref(labelled[0].FSType) != "btrfs" {
			return errors.New("ambiguous or non-btrfs nsl data disk")
		}
		return nil
	}
	if len(candidates) != 1 {
		return fmt.Errorf("expected one data disk, found %d", len(candidates))
	}
	disk := candidates[0].Name
	// blkid --probe exits 2 only when it finds no signature at all.
	var probe bytes.Buffer
	err = a.r.run(context.Background(), nil, &probe, io.Discard, "blkid", "--probe", disk)
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		return fmt.Errorf("refusing to format %s: existing signature %s", disk, strings.TrimSpace(probe.String()))
	}
	if _, err = a.output("mkfs.btrfs", "--label", dataLabel, disk); err != nil {
		return err
	}
	mount := a.path(runtimeDir + "/format")
	if err = os.MkdirAll(mount, 0700); err != nil {
		return err
	}
	if _, err = a.output("mount", "-t", "btrfs", disk, mount); err != nil {
		return err
	}
	defer a.output("umount", mount)
	for _, subvolume := range []string{"machines", "state"} {
		if err = a.btrfs("subvolume", "create", mount+"/"+subvolume); err != nil {
			return err
		}
	}
	return nil
}

// setup binds the VM to its boot credential on first boot and checks it on
// every later boot. It also installs the client key and the /mnt/host aliases.
func (a *agent) setup() error {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if dir == "" {
		return errors.New("no credentials; the nsl.vm credential is required")
	}
	b, err := os.ReadFile(filepath.Join(dir, "nsl.vm"))
	if err != nil {
		return err
	}
	var c protocol.Credential
	if err = protocol.DecodeStrict(b, &c); err != nil {
		return fmt.Errorf("nsl.vm: %w", err)
	}
	if err = c.Validate(); err != nil {
		return fmt.Errorf("nsl.vm: %w", err)
	}
	identity := a.path(stateDir + "/identity.json")
	if b, err = os.ReadFile(identity); err == nil {
		var recorded protocol.Binding
		if err = protocol.DecodeStrict(b, &recorded); err != nil || !reflect.DeepEqual(recorded, c.Binding) {
			return errors.New("the boot credential does not match this VM's data disk")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		b, _ = json.MarshalIndent(c.Binding, "", "  ")
		if err = atomicWrite(identity, append(b, '\n'), 0644); err != nil {
			return err
		}
	} else {
		return err
	}
	if err = a.hostKey(); err != nil {
		return err
	}
	ssh := a.path(runtimeDir + "/ssh")
	if err = os.MkdirAll(ssh, 0700); err != nil {
		return err
	}
	// Every session of this key runs the agent; forwarding is limited to VM
	// loopback ports and the Unix sockets sshd_config allows.
	keys := `restrict,pty,port-forwarding,permitopen="127.0.0.1:*",permitopen="[::1]:*",command="` + protocol.AgentPath + `" ` + c.PublicKey + "\n"
	if err = atomicWrite(ssh+"/authorized_keys", []byte(keys), 0600); err != nil {
		return err
	}
	b, _ = json.Marshal(c)
	if err = atomicWrite(a.path(runtimeDir+"/vm.json"), b, 0600); err != nil {
		return err
	}
	if err = a.aliases(c.Aliases); err != nil {
		return err
	}
	return syncFS(a.path(stateDir))
}

func (a *agent) hostKey() error {
	dir := a.path(stateDir + "/ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	key := dir + "/ssh_host_ed25519_key"
	if _, err := os.Lstat(key); err == nil {
		return nil
	}
	temporary := key + ".new"
	os.Remove(temporary)
	os.Remove(temporary + ".pub")
	if _, err := a.output("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nsl-vm", "-f", temporary); err != nil {
		return err
	}
	// The public half first: a key without it would be regenerated next boot.
	if err := os.Rename(temporary+".pub", key+".pub"); err != nil {
		return err
	}
	if err := os.Rename(temporary, key); err != nil {
		return err
	}
	return syncDir(dir)
}

// aliases makes /mnt/host hold exactly the credential's alias symlinks next to
// the share mount points.
func (a *agent) aliases(aliases []protocol.Alias) error {
	root := a.path("/mnt/host")
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	wanted := map[string]string{}
	for _, alias := range aliases {
		wanted[filepath.Base(alias.Path)] = alias.Target
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			if target, _ := os.Readlink(filepath.Join(root, e.Name())); target != wanted[e.Name()] {
				if err = os.Remove(filepath.Join(root, e.Name())); err != nil {
					return err
				}
			}
		}
	}
	for name, target := range wanted {
		p := filepath.Join(root, name)
		if st, err := os.Lstat(p); err == nil {
			if st.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("alias /mnt/host/%s would replace a directory", name)
			}
			continue
		}
		if err = os.Symlink(target, p); err != nil {
			return err
		}
	}
	return nil
}

func syncFS(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Syncfs(int(f.Fd()))
}

// boot writes every machine's nspawn settings and, with autostart, starts it.
func (a *agent) boot() error {
	b, err := os.ReadFile(a.path(runtimeDir + "/vm.json"))
	if err != nil {
		return err
	}
	var c protocol.Credential
	if err = json.Unmarshal(b, &c); err != nil {
		return err
	}
	records, err := a.records()
	if err != nil {
		return err
	}
	a.saveIdleTimeout(c.IdleTimeout)
	for _, r := range records {
		if err = a.writeSettings(r.Name, c.Role); err != nil {
			return err
		}
	}
	if !c.Autostart {
		return nil
	}
	for _, r := range records {
		if _, err = a.output("systemctl", "start", "--no-block", unitOf(r.Name)); err != nil {
			return err
		}
	}
	return nil
}
