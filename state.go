package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

func checkPrivateDir(path string, uid int) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || !ok || int(stat.Uid) != uid || st.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("refusing unowned or writable state directory %s", path)
	}
	return nil
}
func privateFile(path string, uid int, mask os.FileMode) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || !ok || int(stat.Uid) != uid || st.Mode().Perm()&mask != 0 {
		return fmt.Errorf("invalid ownership, type or permissions: %s", path)
	}
	return nil
}
func (a *app) init() error {
	if a.uid == 0 {
		return errors.New("run nsl as your normal host user")
	}
	for _, p := range []string{a.home, filepath.Join(a.home, "environments"), filepath.Join(a.home, "images"), filepath.Join(a.home, "removing"), a.runtimeDir} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return err
		}
		if err := checkPrivateDir(p, a.uid); err != nil {
			return err
		}
	}
	return nil
}
func (a *app) owned(name string) (*environment, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	if err := a.init(); err != nil {
		return nil, err
	}
	return a.ownedAt(name, a.dir(name))
}
func (a *app) ownedAt(name, dir string) (*environment, error) {
	if err := checkPrivateDir(dir, a.uid); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "environment.json")
	if err := privateFile(path, a.uid, 0077); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e environment
	if err = json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	if e.Schema != 2 || e.Name != name || e.Owner != a.uid || e.GID != a.gid || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(e.ID) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(e.Digest) || e.CPUs < 1 || e.CPUs > 64 || e.Memory < 1 || e.Memory > 128 || e.Disk < 4 || e.Disk > 4096 {
		return nil, errors.New("unsupported metadata schema or environment identity/resource mismatch")
	}
	if e.Project != "" && (!filepath.IsAbs(e.Project) || strings.ContainsAny(e.Project, ":\r\n\x00")) {
		return nil, errors.New("invalid project in metadata")
	}
	if e.GuestID != "" && !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(e.GuestID) {
		return nil, errors.New("invalid guest binding in metadata")
	}
	if e.ResizeTarget != 0 && (!e.Prepared || e.ResizeTarget <= e.Disk || e.ResizeTarget > 4096) {
		return nil, errors.New("invalid pending disk growth")
	}
	return &e, nil
}
func fileLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (a *app) lock(name string) (*os.File, error) {
	return fileLock(filepath.Join(a.dir(name), "lock"))
}

// A waiter must not act on a new environment created under the old name.
func (a *app) lockOwned(expected *environment) (*os.File, *environment, error) {
	l, err := a.lock(expected.Name)
	if err != nil {
		return nil, nil, err
	}
	e, err := a.owned(expected.Name)
	if err == nil && e.ID != expected.ID {
		err = errors.New("environment was replaced while waiting; retry explicitly")
	}
	if err != nil {
		unlock(l)
		return nil, nil, err
	}
	return l, e, nil
}
func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (a *app) save(e *environment) error {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(a.dir(e.Name), "environment.json"), append(b, '\n'), 0600)
}
func (a *app) imagePath(e *environment) string {
	return filepath.Join(a.home, "images", e.Digest+".raw")
}
func (a *app) importImage(source, digest string) error {
	destination := filepath.Join(a.home, "images", digest+".raw")
	if _, err := os.Lstat(destination); err == nil {
		if err = privateFile(destination, a.uid, 0022); err != nil {
			return err
		}
		source = destination
	} else if !os.IsNotExist(err) {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	st, err := input.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("image must be a regular raw disk file")
	}
	f, err := os.CreateTemp(filepath.Join(a.home, "images"), ".import-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	var target io.Writer = hash
	if source != destination {
		target = io.MultiWriter(f, hash)
	}
	if _, err = io.Copy(target, input); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("image SHA256 mismatch")
	}
	if source == destination {
		return nil
	}
	if err = f.Chmod(0444); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), destination) // serialized by the manager lock
}
func (a *app) create(name string, args []string) error {
	if err := checkName(name); err != nil {
		return err
	}
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(a.err)
	project := fs.String("project", "", "host project mounted at /work")
	desktop := fs.Bool("desktop", false, "allow Waypipe sessions")
	cpus := fs.Int("cpus", 2, "VM CPUs")
	memory := fs.Int("memory", 2, "RAM in GiB")
	disk := fs.Int("disk", 16, "disk in GiB")
	image := fs.String("image", "", "local nsl raw image")
	digest := fs.String("digest", "", "sha256:HEX")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *cpus < 1 || *cpus > 64 || *memory < 1 || *memory > 128 || *disk < 4 || *disk > 4096 {
		return errors.New("invalid create arguments or resource limits")
	}
	if runtime.GOARCH != "amd64" {
		return errors.New("VM creation currently supports x86_64 only")
	}
	if *image == "" || !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(*digest) {
		return errors.New("create requires --image FILE --digest sha256:HEX")
	}
	projectDir, err := projectPath(*project)
	if err != nil {
		return err
	}
	if err = a.init(); err != nil {
		return err
	}
	manager, err := fileLock(filepath.Join(a.home, "lock"))
	if err != nil {
		return err
	}
	defer unlock(manager)
	if err = a.nameAvailable(name); err != nil {
		return err
	}
	if err = a.importImage(*image, strings.TrimPrefix(*digest, "sha256:")); err != nil {
		return err
	}
	e := &environment{Schema: 2, Name: name, Owner: a.uid, GID: a.gid, Project: projectDir, Desktop: *desktop, CPUs: *cpus, Memory: *memory, Disk: *disk, Digest: strings.TrimPrefix(*digest, "sha256:")}
	if err = a.allocateID(e); err != nil {
		return err
	}
	if err = os.Mkdir(a.dir(name), 0700); err != nil {
		return err
	}
	if err = a.save(e); err != nil {
		return err
	}
	l, err := a.lock(name)
	if err != nil {
		return err
	}
	defer unlock(l)
	if err = a.prepare(e); err != nil {
		return fmt.Errorf("creation incomplete; use nsl recover %s (state retained): %w", name, err)
	}
	fmt.Fprintf(a.out, "Created %s; use nsl shell %s\n", name, name)
	return nil
}

// Caller holds the manager lock while allocating and publishing the ID.
func (a *app) allocateID(e *environment) error {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	e.ID = hex.EncodeToString(id)
	entries, err := os.ReadDir(filepath.Join(a.home, "environments"))
	if err != nil {
		return err
	}
	used := map[uint32]bool{}
	for _, entry := range entries {
		other, err := a.owned(entry.Name())
		if err != nil {
			return fmt.Errorf("inspect existing environment %s before creating another: %w", entry.Name(), err)
		}
		used[cid(other)] = true
	}
	for used[cid(e)] {
		if _, err = rand.Read(id); err != nil {
			return err
		}
		e.ID = hex.EncodeToString(id)
	}
	return nil
}
func sshQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(s) + `"`
}
func (a *app) socket(e *environment) string { return filepath.Join(a.runtimeDir, e.ID, "ssh.sock") }
func guestID(e *environment) string {
	if e.GuestID != "" {
		return e.GuestID
	}
	return e.ID
}
func (a *app) prepare(e *environment) error {
	dir := a.dir(e.Name)
	var err error
	keys := filepath.Join(dir, "keys")
	if _, err = os.Lstat(keys); os.IsNotExist(err) {
		temporary, err := os.MkdirTemp(dir, ".keys-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		if err = a.call(nil, a.err, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nsl-"+e.ID, "-f", filepath.Join(temporary, "identity")); err != nil {
			return err
		}
		if err = os.Rename(temporary, keys); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err = checkPrivateDir(keys, a.uid); err != nil {
		return err
	}
	if err = privateFile(filepath.Join(keys, "identity"), a.uid, 0077); err != nil {
		return err
	}
	if err = privateFile(filepath.Join(keys, "identity.pub"), a.uid, 0022); err != nil {
		return err
	}
	public, err := os.ReadFile(filepath.Join(keys, "identity.pub"))
	if err != nil {
		return err
	}
	credential, _ := json.Marshal(map[string]any{"version": 1, "id": guestID(e), "uid": e.Owner, "gid": e.GID, "public_key": strings.TrimSpace(string(public))})
	if err = atomicWrite(filepath.Join(dir, "boot.json"), credential, 0600); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(a.socket(e)), 0700); err != nil {
		return err
	}
	if err = checkPrivateDir(filepath.Dir(a.socket(e)), a.uid); err != nil {
		return err
	}
	config := fmt.Sprintf(`Host guest
    Hostname vsock/%d
    User nsl
    IdentityFile %s
    IdentitiesOnly yes
    BatchMode yes
    StrictHostKeyChecking accept-new
    UserKnownHostsFile %s
    HostKeyAlias nsl-%s
    ProxyCommand /usr/lib/systemd/systemd-ssh-proxy %%h %%p
    ProxyUseFdpass yes
    ConnectTimeout 2
    ControlMaster auto
    ControlPath %s
    ControlPersist 60
    ServerAliveInterval 10
    ServerAliveCountMax 3
`, cid(e), sshQuote(filepath.Join(keys, "identity")), sshQuote(filepath.Join(dir, "known_hosts")), guestID(e), sshQuote(a.socket(e)))
	if err = atomicWrite(filepath.Join(dir, "ssh.config"), []byte(config), 0600); err != nil {
		return err
	}
	disk := filepath.Join(dir, "disk.qcow2")
	if _, err = os.Lstat(disk); os.IsNotExist(err) {
		if err = privateFile(a.imagePath(e), a.uid, 0022); err != nil {
			return err
		}
		st, err := os.Stat(a.imagePath(e))
		if err != nil {
			return err
		}
		if st.Size() > int64(e.Disk)*1024*1024*1024 {
			return errors.New("requested disk is smaller than the image")
		}
		f, err := os.CreateTemp(dir, ".disk-*")
		if err != nil {
			return err
		}
		f.Close()
		defer os.Remove(f.Name())
		if err = a.call(nil, a.err, "qemu-img", "convert", "-f", "raw", "-O", "qcow2", a.imagePath(e), f.Name()); err != nil {
			return err
		}
		if err = a.call(nil, a.err, "qemu-img", "resize", "-f", "qcow2", f.Name(), fmt.Sprintf("%dG", e.Disk)); err != nil {
			return err
		}
		if err = os.Chmod(f.Name(), 0600); err != nil {
			return err
		}
		if err = os.Rename(f.Name(), disk); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if err = privateFile(disk, a.uid, 0077); err != nil {
			return err
		}
		if err = a.call(nil, a.err, "qemu-img", "check", "-f", "qcow2", disk); err != nil {
			return fmt.Errorf("disk check failed; preserved disk needs inspection: %w", err)
		}
	}
	e.Prepared = true
	return a.save(e)
}
