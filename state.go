package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/frostyard/nsl/internal/protocol"
)

const gib = int64(1 << 30)

// defaultDataGiB is the data disk's initial virtual size; it is sparse on the host.
const defaultDataGiB = 128

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
	for _, p := range []string{a.home, filepath.Join(a.home, "images"), filepath.Join(a.home, "images", "vm"),
		a.machineImages(), a.runtimeDir} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return err
		}
		if err := checkPrivateDir(p, a.uid); err != nil {
			return err
		}
	}
	return nil
}

// machineImages is the verified machine-image cache that VMs read through a
// read-only share. Nothing else may live there.
func (a *app) machineImages() string { return filepath.Join(a.home, "images", "machines") }

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
	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// vmRecord is one nsl VM: its identity, the image its root came from, the
// resources in effect since its last start, and its data disk. The shared VM
// lives in NSL_HOME/vm; an isolated machine's VM in NSL_HOME/isolated/NAME.
type vmRecord struct {
	Schema       int                  `json:"schema"`
	ID           string               `json:"id"`
	Owner        int                  `json:"owner"`
	GID          int                  `json:"gid"`
	Role         string               `json:"role"`
	Machine      *protocol.MachineRef `json:"machine,omitempty"`
	Image        string               `json:"image,omitempty"`
	ImageBuild   string               `json:"image_build,omitempty"`
	PendingImage string               `json:"pending_image,omitempty"`
	CPUs         int                  `json:"cpus,omitempty"`
	Memory       int                  `json:"memory,omitempty"`
	DataGiB      int                  `json:"data_gib"`
	ResizeTarget int                  `json:"resize_target_gib,omitempty"`
	Initialized  bool                 `json:"initialized"`

	dir string
}

func (v *vmRecord) validate(uid, gid int) error {
	digest := func(s string) bool { return s == "" || len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }
	role := v.Role == "shared" && v.Machine == nil ||
		v.Role == "isolated" && v.Machine != nil && protocol.ValidName(v.Machine.Name) && protocol.ValidID(v.Machine.ID)
	if v.Schema != 1 || v.Owner != uid || v.GID != gid || !protocol.ValidID(v.ID) || !role ||
		!digest(v.Image) || !digest(v.PendingImage) || v.CPUs < 0 || v.CPUs > 64 || v.Memory < 0 || v.Memory > 128 ||
		v.DataGiB < 1 || v.DataGiB > 4096 || (v.ResizeTarget != 0 && (v.ResizeTarget <= v.DataGiB || v.ResizeTarget > 4096)) {
		return errors.New("unsupported VM record or identity mismatch")
	}
	return nil
}

// label names a VM for people: shared, or the isolated machine's name.
func (v *vmRecord) label() string {
	if v.Machine != nil {
		return v.Machine.Name
	}
	return "shared"
}

func (a *app) vmDir() string                    { return filepath.Join(a.home, "vm") }
func (a *app) isolatedDir() string              { return filepath.Join(a.home, "isolated") }
func (a *app) isolatedVMDir(name string) string { return filepath.Join(a.isolatedDir(), name) }

// loadVM returns the shared VM's record, or nil when there is none yet.
func (a *app) loadVM() (*vmRecord, error) { return a.loadVMAt(a.vmDir()) }

// loadVMAt returns the VM record in dir, or nil when there is none.
func (a *app) loadVMAt(dir string) (*vmRecord, error) {
	if err := a.init(); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err := checkPrivateDir(dir, a.uid); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "vm.json")
	if err := privateFile(path, a.uid, 0077); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v := &vmRecord{dir: dir}
	if err = protocol.DecodeStrict(b, v); err != nil {
		return nil, fmt.Errorf("VM record: %w", err)
	}
	if err = v.validate(a.uid, a.gid); err != nil {
		return nil, err
	}
	if (v.Role == "shared") != (dir == a.vmDir()) || v.Machine != nil && dir != a.isolatedVMDir(v.Machine.Name) {
		return nil, errors.New("VM record in the wrong place: " + dir)
	}
	return v, nil
}

// allVMs returns the shared VM, when there is one, and every isolated VM.
func (a *app) allVMs() ([]*vmRecord, error) {
	var out []*vmRecord
	v, err := a.loadVM()
	if err != nil {
		return nil, err
	}
	if v != nil {
		out = append(out, v)
	}
	entries, err := os.ReadDir(a.isolatedDir())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !protocol.ValidName(e.Name()) {
			continue // .vm-* staging and removals in progress
		}
		v, err := a.loadVMAt(a.isolatedVMDir(e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// vmByID finds a VM for the internal launch commands.
func (a *app) vmByID(id string) (*vmRecord, error) {
	all, err := a.allVMs()
	if err != nil {
		return nil, err
	}
	for _, v := range all {
		if v.ID == id {
			return v, nil
		}
	}
	return nil, errors.New("no nsl VM " + id)
}

func (a *app) saveVM(v *vmRecord) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(v.dir, "vm.json"), append(b, '\n'), 0600)
}

// newVMID picks an ID whose vsock CID no other VM uses.
func (a *app) newVMID() (string, error) {
	all, err := a.allVMs()
	if err != nil {
		return "", err
	}
	for {
		id := randomID()
		v := &vmRecord{ID: id}
		clash := false
		for _, other := range all {
			clash = clash || cid(other) == cid(v)
		}
		if !clash {
			return id, nil
		}
	}
}

// ensureVM returns the shared VM's record, creating the VM on first use.
func (a *app) ensureVM() (*vmRecord, error) {
	return a.ensureVMAt(a.vmDir(), nil)
}

// ensureIsolatedVM returns an isolated machine's VM, creating it with the VM
// image selected for the shared VM.
func (a *app) ensureIsolatedVM(m *machineRecord) (*vmRecord, error) {
	v, err := a.ensureVMAt(a.isolatedVMDir(m.Name), &protocol.MachineRef{Name: m.Name, ID: m.ID})
	if err == nil && v.Machine.ID != m.ID {
		err = fmt.Errorf("the isolated VM of %s belongs to another machine; run nsl remove %s --yes", m.Name, m.Name)
	}
	return v, err
}

func (a *app) ensureVMAt(dir string, machine *protocol.MachineRef) (*vmRecord, error) {
	if v, err := a.loadVMAt(dir); v != nil || err != nil {
		return v, err
	}
	manager, err := fileLock(filepath.Join(a.home, "lock"))
	if err != nil {
		return nil, err
	}
	defer unlock(manager)
	if v, err := a.loadVMAt(dir); v != nil || err != nil {
		return v, err
	}
	id, err := a.newVMID()
	if err != nil {
		return nil, err
	}
	v := &vmRecord{Schema: 1, ID: id, Owner: a.uid, GID: a.gid, Role: "shared", DataGiB: defaultDataGiB}
	if machine != nil {
		shared, err := a.loadVM()
		if err != nil {
			return nil, err
		}
		if shared == nil || shared.Image == "" && shared.PendingImage == "" {
			return nil, errors.New("no VM image selected; run nsl update")
		}
		v.Role, v.Machine, v.PendingImage = "isolated", machine, shared.PendingImage
		if v.PendingImage == "" {
			v.PendingImage = shared.Image
		}
	}
	parent := filepath.Dir(dir)
	if err = os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	if err = checkPrivateDir(parent, a.uid); err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp(parent, ".vm-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	v.dir = temporary
	if err = a.prepareVM(v); err != nil {
		return nil, err
	}
	// Publish the complete directory at once; a crash leaves only a temporary.
	if err = os.Rename(temporary, dir); err != nil {
		return nil, err
	}
	v.dir = dir
	return v, a.prepareVM(v)
}

// lockVM serializes lifecycle changes and rejects a VM replaced while waiting.
func (a *app) lockVM(expected *vmRecord) (*os.File, *vmRecord, error) {
	l, err := fileLock(filepath.Join(expected.dir, "lock"))
	if err != nil {
		return nil, nil, err
	}
	v, err := a.loadVMAt(expected.dir)
	if err == nil && (v == nil || v.ID != expected.ID) {
		err = errors.New("the VM was replaced while waiting; retry")
	}
	if err != nil {
		unlock(l)
		return nil, nil, err
	}
	return l, v, nil
}

func sshQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(s) + `"`
}

func (a *app) socket(v *vmRecord) string { return filepath.Join(a.runtimeDir, v.ID, "ssh.sock") }

// prepareVM creates or completes the VM's keys, SSH configuration and data
// disk without replacing anything that exists.
func (a *app) prepareVM(v *vmRecord) error {
	keys := filepath.Join(v.dir, "keys")
	if _, err := os.Lstat(keys); errors.Is(err, os.ErrNotExist) {
		temporary, err := os.MkdirTemp(v.dir, ".keys-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		if err = a.call(nil, a.err, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nsl-vm-"+v.ID, "-f", filepath.Join(temporary, "identity")); err != nil {
			return err
		}
		if err = os.Rename(temporary, keys); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := checkPrivateDir(keys, a.uid); err != nil {
		return err
	}
	if err := privateFile(filepath.Join(keys, "identity"), a.uid, 0077); err != nil {
		return err
	}
	if err := privateFile(filepath.Join(keys, "identity.pub"), a.uid, 0022); err != nil {
		return err
	}
	config := fmt.Sprintf(`Host vm
    Hostname vsock/%d
    User root
    IdentityFile %s
    IdentitiesOnly yes
    BatchMode yes
    LogLevel ERROR
    StrictHostKeyChecking accept-new
    UserKnownHostsFile %s
    HostKeyAlias nsl-vm-%s
    ProxyCommand /usr/lib/systemd/systemd-ssh-proxy %%h %%p
    ProxyUseFdpass yes
    ConnectTimeout 2
    ControlMaster auto
    ControlPath %s
    ControlPersist 60
    ServerAliveInterval 10
    ServerAliveCountMax 3
`, cid(v), sshQuote(filepath.Join(keys, "identity")), sshQuote(filepath.Join(v.dir, "known_hosts")), v.ID, sshQuote(a.socket(v)))
	if err := atomicWrite(filepath.Join(v.dir, "ssh.config"), []byte(config), 0600); err != nil {
		return err
	}
	data := filepath.Join(v.dir, "data.qcow2")
	if _, err := os.Lstat(data); errors.Is(err, os.ErrNotExist) {
		f, err := os.CreateTemp(v.dir, ".data-*")
		if err != nil {
			return err
		}
		f.Close()
		defer os.Remove(f.Name())
		if err = a.call(nil, a.err, "qemu-img", "create", "-q", "-f", "qcow2", f.Name(), fmt.Sprintf("%dG", v.DataGiB)); err != nil {
			return err
		}
		if err = os.Chmod(f.Name(), 0600); err != nil {
			return err
		}
		if err = os.Rename(f.Name(), data); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := privateFile(data, a.uid, 0077); err != nil {
		return err
	}
	return a.saveVM(v)
}
