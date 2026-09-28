package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

func vmUnit(v *vmRecord) string        { return fmt.Sprintf("nsl-%d-vm-%s.service", v.Owner, v.ID) }
func vmDescription(v *vmRecord) string { return "nsl VM " + v.ID }
func cid(v *vmRecord) uint32 {
	b, _ := hex.DecodeString(v.ID)
	return 0x40000000 | (binary.BigEndian.Uint32(b[:4]) & 0x3fffffff)
}

func (a *app) capture(timeout time.Duration, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out, stderr bytes.Buffer
	err := a.r.run(ctx, nil, &out, &stderr, os.Environ(), bin, args...)
	if err != nil {
		return out.Bytes(), fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// unitState returns a user unit's ActiveState after checking that the unit
// belongs to this VM.
func (a *app) unitState(name, description string) (string, error) {
	b, err := a.capture(5*time.Second, "systemctl", "--user", "show", name, "--property=LoadState,ActiveState,Description")
	values := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			values[k] = v
		}
	}
	if values["LoadState"] == "not-found" {
		return "inactive", nil
	}
	if err != nil {
		return "", err
	}
	if values["Description"] != description {
		return "", errors.New("refusing foreign systemd unit " + name)
	}
	return values["ActiveState"], nil
}

func (a *app) vmState(v *vmRecord) (string, error) {
	state, err := a.unitState(vmUnit(v), vmDescription(v))
	switch {
	case err != nil:
		return "", err
	case state == "active" || state == "activating":
		return "running", nil
	case state == "deactivating":
		return "stopping", nil
	case state == "failed":
		return "failed", nil
	}
	return "stopped", nil
}

func (a *app) sshArgs(v *vmRecord, tty bool) []string {
	mode := "-T"
	if tty {
		mode = "-tt"
	}
	return []string{"-F", filepath.Join(v.dir, "ssh.config"), mode, "vm"}
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		n, _ := b.buffer.Write(p[:remaining])
		return n, io.ErrShortBuffer
	}
	return b.buffer.Write(p)
}

// agentStream sends one request; the operation's stdin and stdout belong to the
// caller, and an agent error on stderr becomes a *protocol.Error.
func (a *app) agentStream(v *vmRecord, req protocol.Request, stdin io.Reader, stdout io.Writer, timeout time.Duration) error {
	req.Protocol = protocol.Version
	encoded, err := protocol.Encode(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stderr := &boundedBuffer{limit: 16 << 10}
	err = a.r.run(ctx, stdin, stdout, stderr, os.Environ(), "ssh", append(a.sshArgs(v, false), encoded)...)
	if agentErr := protocol.ParseError(stderr.buffer.String()); err != nil && agentErr != nil {
		return agentErr
	}
	if err != nil {
		return fmt.Errorf("ssh: %w: %s", err, strings.TrimSpace(stderr.buffer.String()))
	}
	return nil
}

// agentJSON sends one request whose answer is JSON, and decodes it strictly.
func (a *app) agentJSON(v *vmRecord, req protocol.Request, stdin io.Reader, timeout time.Duration, dst any) error {
	out := &boundedBuffer{limit: 1 << 20}
	if err := a.agentStream(v, req, stdin, out, timeout); err != nil {
		return err
	}
	if dst == nil {
		return nil
	}
	return protocol.DecodeStrict(out.buffer.Bytes(), dst)
}

// vmDescriptor is /usr/lib/nsl/image.json in the VM image.
type vmDescriptor struct {
	Schema            int    `json:"schema"`
	Role              string `json:"role"`
	BuildID           string `json:"build_id"`
	Distribution      string `json:"distribution"`
	Release           string `json:"release"`
	Architecture      string `json:"architecture"`
	Revision          int    `json:"revision"`
	AgentProtocol     int    `json:"agent_protocol"`
	MachineProtocol   int    `json:"machine_protocol"`
	Transport         string `json:"transport"`
	Systemd           string `json:"systemd"`
	Kernel            string `json:"kernel"`
	IntegrationSHA256 string `json:"integration_sha256"`
	RecipesRevision   string `json:"recipes_revision"`
	MkosiRevision     string `json:"mkosi_revision"`
}

func (d *vmDescriptor) compatible() error {
	if d.Schema != 1 || d.Role != "vm" || d.Architecture != "x86-64" || d.Transport != "nsl-vsock-ssh" ||
		d.AgentProtocol != protocol.Version || d.MachineProtocol != protocol.MachineVersion || !imageWord.MatchString(d.BuildID) {
		return fmt.Errorf("%w: need a VM image with agent protocol %d and machine protocol %d on x86-64", errIncompatibleImage, protocol.Version, protocol.MachineVersion)
	}
	return nil
}

// ready authenticates the VM and checks it is the VM this record describes.
func (a *app) ready(v *vmRecord) (*vmDescriptor, error) {
	var id protocol.Identity
	if err := a.agentJSON(v, protocol.Request{Op: "identity"}, nil, 5*time.Second, &id); err != nil {
		return nil, err
	}
	if id.Protocol != protocol.Version {
		return nil, fmt.Errorf("%w: agent protocol %d, CLI protocol %d", errIncompatibleImage, id.Protocol, protocol.Version)
	}
	b := id.VM
	if b.Version != 1 || b.ID != v.ID || b.Role != v.Role || b.UID != v.Owner || b.GID != v.GID || b.Machine != nil {
		return nil, errors.New("VM identity does not match its record")
	}
	var d vmDescriptor
	if err := protocol.DecodeStrict(id.Image, &d); err != nil {
		return nil, fmt.Errorf("%w: %v", errIncompatibleImage, err)
	}
	return &d, d.compatible()
}

// hostShares are the ADR-0016 trees shared with machines: the home,
// /run/media/USER and /mnt, at their canonical paths, when they exist.
func (a *app) hostShares() ([]protocol.Share, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var shares []protocol.Share
	seen := map[string]bool{}
	for _, tree := range []string{home, filepath.Join(a.hostRoot, "run/media", a.user), filepath.Join(a.hostRoot, "mnt")} {
		st, err := os.Stat(tree)
		if err != nil || !st.IsDir() {
			continue
		}
		canonical, err := filepath.EvalSymlinks(tree)
		if err != nil {
			return nil, err
		}
		if !protocol.SafeHostPath(canonical) {
			return nil, fmt.Errorf("cannot share %s: unsupported characters in its path", canonical)
		}
		if !seen[canonical] {
			seen[canonical] = true
			shares = append(shares, protocol.Share{Source: canonical})
		}
	}
	return shares, nil
}

func sameFile(a, b string) bool {
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// hostAliases finds top-level host directories that are another path to an
// ancestor of a shared tree, such as /home for /var/home, whether through a
// symlink or a bind mount.
func (a *app) hostAliases(shares []protocol.Share) []protocol.Alias {
	root := filepath.Clean(a.hostRoot)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var aliases []protocol.Alias
	for _, e := range entries {
		top := filepath.Join(root, e.Name())
	search:
		for _, s := range shares {
			for ancestor := filepath.Dir(s.Source); ancestor != root && strings.HasPrefix(ancestor, root); ancestor = filepath.Dir(ancestor) {
				if ancestor != top && sameFile(top, ancestor) {
					target, _ := filepath.Rel(root, ancestor)
					aliases = append(aliases, protocol.Alias{Path: "/mnt/host/" + e.Name(), Target: target})
					break search
				}
			}
		}
	}
	return aliases
}

func (a *app) writeCredential(v *vmRecord, c *config, autostart bool) error {
	public, err := os.ReadFile(filepath.Join(v.dir, "keys", "identity.pub"))
	if err != nil {
		return err
	}
	shares, err := a.hostShares()
	if err != nil {
		return err
	}
	cred := protocol.Credential{
		Binding:   protocol.Binding{Version: 1, ID: v.ID, Role: v.Role, UID: v.Owner, GID: v.GID},
		PublicKey: strings.TrimSpace(string(public)), Autostart: autostart && c.autostart.value, IdleTimeout: c.idleTimeout.value,
		Shares: shares, Aliases: a.hostAliases(shares),
	}
	if cred.Shares == nil {
		cred.Shares = []protocol.Share{}
	}
	if cred.Aliases == nil {
		cred.Aliases = []protocol.Alias{}
	}
	if err = cred.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(cred)
	return atomicWrite(filepath.Join(v.dir, "nsl.vm"), b, 0600)
}

func (a *app) runtimeFiles(v *vmRecord) error {
	for _, p := range []string{"data.qcow2", "ssh.config", "keys/identity"} {
		if err := privateFile(filepath.Join(v.dir, p), a.uid, 0077); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(a.socket(v)), 0700); err != nil {
		return err
	}
	return checkPrivateDir(filepath.Dir(a.socket(v)), a.uid)
}

func (a *app) vmImagePath(digest string) string {
	return filepath.Join(a.home, "images", "vm", digest+".raw")
}

// rootOverlay gives the VM a fresh root from the selected image when the
// image changed or no root exists. The root holds no user state.
func (a *app) rootOverlay(v *vmRecord) error {
	root := filepath.Join(v.dir, "root.qcow2")
	selected := v.PendingImage
	if selected == "" {
		selected = v.Image
	}
	if selected == "" {
		return errors.New("no VM image selected; run nsl update --image FILE --digest sha256:HEX")
	}
	_, err := os.Lstat(root)
	if err == nil && selected == v.Image {
		return privateFile(root, a.uid, 0077)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	image := a.vmImagePath(selected)
	if err = privateFile(image, a.uid, 0222); err != nil {
		return fmt.Errorf("selected VM image: %w", err)
	}
	temporary := filepath.Join(v.dir, ".root.qcow2")
	os.Remove(temporary)
	if err = a.call(nil, a.err, "qemu-img", "create", "-q", "-f", "qcow2", "-F", "raw", "-b", image, temporary, "16G"); err != nil {
		return err
	}
	if err = os.Chmod(temporary, 0600); err != nil {
		return err
	}
	if err = os.Rename(temporary, root); err != nil {
		return err
	}
	v.Image, v.PendingImage, v.ImageBuild = selected, "", ""
	return a.saveVM(v)
}

// startVM launches the VM if needed and waits for authenticated readiness. A
// VM that powers off while idle is started again. autostart lets the VM start
// every machine; commands that only manage machines start it without.
// The caller holds the VM lock.
func (a *app) startVM(v *vmRecord, autostart bool) error {
	if v.ResizeTarget != 0 {
		return errors.New("data disk growth is incomplete; run nsl recover")
	}
	if err := a.runtimeFiles(v); err != nil {
		return err
	}
	launched := false
	deadline := time.Now().Add(90 * time.Second)
	var readiness error
	for {
		state, err := a.vmState(v)
		if err != nil {
			return err
		}
		switch {
		case state == "running":
		case launched:
			return errors.New("the VM exited; see journalctl --user -u " + vmUnit(v) + ", then run nsl recover")
		case state == "stopping":
			// An idle VM powering itself off: wait, then start it again.
		default:
			if err = a.launchVM(v, state, autostart); err != nil {
				return err
			}
			launched, deadline = true, time.Now().Add(90*time.Second)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("VM readiness timed out; disks retained; see journalctl --user -u %s or run nsl recover: %w", vmUnit(v), readiness)
		}
		if state == "running" {
			var d *vmDescriptor
			if d, readiness = a.ready(v); readiness == nil {
				if !v.Initialized || v.ImageBuild != d.BuildID {
					v.Initialized, v.ImageBuild = true, d.BuildID
					if err = a.saveVM(v); err != nil {
						return err
					}
				}
				return nil
			}
			if errors.Is(readiness, errIncompatibleImage) {
				return readiness
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (a *app) launchVM(v *vmRecord, state string, autostart bool) error {
	c, err := a.loadConfig()
	if err != nil {
		return err
	}
	if state == "failed" {
		if _, err = a.capture(5*time.Second, "systemctl", "--user", "reset-failed", vmUnit(v)); err != nil {
			return err
		}
	}
	if err = a.rootOverlay(v); err != nil {
		return err
	}
	if err = a.writeCredential(v, c, autostart); err != nil {
		return err
	}
	v.CPUs, v.Memory = c.vmCPUs.value, c.vmMemory.value
	if err = a.saveVM(v); err != nil {
		return err
	}
	script := "exec " + shellQuote(a.self) + " _devices shared"
	return a.call(nil, a.err, "systemd-run", "--user", "--unit="+vmUnit(v), "--description="+vmDescription(v), "--collect",
		"--property=Type=exec", "--property=TimeoutStopSec=30", "--property=KillMode=mixed",
		"--setenv=NSL_HOME="+a.home, "--setenv=NSL_DEBUG="+os.Getenv("NSL_DEBUG"), "--", "sg", "kvm", "-c", script)
}

// runningVM starts the shared VM if needed and returns it ready.
func (a *app) runningVM(autostart bool) (*vmRecord, error) {
	v, err := a.ensureVM()
	if err != nil {
		return nil, err
	}
	l, v, err := a.lockVM(v)
	if err != nil {
		return nil, err
	}
	defer unlock(l)
	return v, a.startVM(v, autostart)
}

func (a *app) stopVM(v *vmRecord) error {
	state, err := a.vmState(v)
	if err != nil || state == "stopped" {
		return err
	}
	if state == "running" {
		poweroff := protocol.Request{Op: "vm", Argv: []string{"systemctl", "poweroff"}}
		_ = a.agentJSON(v, poweroff, nil, 5*time.Second, nil)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if state, err = a.vmState(v); err != nil {
				return err
			}
			if state == "stopped" || state == "failed" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if state != "stopped" {
		if _, err = a.capture(40*time.Second, "systemctl", "--user", "stop", vmUnit(v)); err != nil {
			return err
		}
	}
	_, _ = a.capture(5*time.Second, "ssh", "-F", filepath.Join(v.dir, "ssh.config"), "-O", "exit", "vm")
	return nil
}

func (a *app) shutdown() error {
	v, err := a.loadVM()
	if err != nil || v == nil {
		return err
	}
	l, v, err := a.lockVM(v)
	if err != nil {
		return err
	}
	defer unlock(l)
	return a.stopVM(v)
}

// recover restarts the VM from a fresh root, completing interrupted growth
// and checking the data disk. Machines and keys are kept.
func (a *app) recover() error {
	v, err := a.loadVM()
	if err != nil {
		return err
	}
	if v == nil {
		return errors.New("there is no nsl VM yet")
	}
	l, v, err := a.lockVM(v)
	if err != nil {
		return err
	}
	defer unlock(l)
	if err = a.stopVM(v); err != nil {
		return err
	}
	if err = a.prepareVM(v); err != nil {
		return err
	}
	if v.ResizeTarget != 0 {
		if err = a.finishGrowth(v); err != nil {
			return err
		}
	}
	if err = a.checkDataDisk(v); err != nil {
		return err
	}
	// The root holds no user state, so a fresh one repairs it.
	if err = os.Remove(filepath.Join(v.dir, "root.qcow2")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if v.PendingImage == "" {
		v.PendingImage = v.Image
	}
	if err = a.startVM(v, true); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "Recovered the VM with a fresh root; its data disk, machines and keys are unchanged")
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (a *app) devices(args []string) error {
	if len(args) != 1 || args[0] != "shared" {
		return errors.New("internal device launch requires a VM")
	}
	account, err := user.LookupId(strconv.Itoa(a.uid))
	if err != nil {
		return err
	}
	group, err := user.LookupGroupId(account.Gid)
	if err != nil {
		return err
	}
	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer kvm.Close()
	vsock, err := os.OpenFile("/dev/vhost-vsock", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer vsock.Close()
	script := "exec unshare --user --map-current-user --keep-caps " + shellQuote(a.self) + " _launch shared"
	c := exec.Command("sg", group.Name, "-c", script)
	c.Env = os.Environ()
	c.ExtraFiles = []*os.File{kvm, vsock}
	c.Stdin, c.Stdout, c.Stderr = a.in, a.out, a.err
	return c.Run()
}

func (a *app) launchArgs(v *vmRecord, cred *protocol.Credential) []string {
	args := []string{"--user", "--no-ask-password", "--keep-unit", "--register=no",
		"--image=" + filepath.Join(v.dir, "root.qcow2"), "--image-format=qcow2", "--machine=nsl-" + v.ID,
		"--cpus=" + strconv.Itoa(v.CPUs), "--ram=" + strconv.Itoa(v.Memory) + "G",
		"--kvm=yes", "--vsock=yes", "--vsock-cid=" + strconv.FormatUint(uint64(cid(v)), 10), "--tpm=no", "--secure-boot=no",
		"--network-user-mode", "--notify-ready=no", "--pass-ssh-key=no", "--console=read-only",
		"--load-credential=nsl.vm:" + filepath.Join(v.dir, "nsl.vm"),
		"--extra-drive=qcow2:virtio-blk:" + filepath.Join(v.dir, "data.qcow2"),
		"--bind-ro=" + a.machineImages() + ":" + protocol.ImageShare}
	for _, s := range cred.Shares {
		args = append(args, "--bind="+s.Source+":/mnt/host"+s.Source)
	}
	if os.Getenv("NSL_DEBUG") == "1" {
		args = append(args, "systemd.journald.forward_to_console=yes")
	}
	// vmspawn creates bind mount points on the root, so it must be writable.
	return append(args, "rw")
}

func (a *app) launch(args []string) error {
	if len(args) != 1 || args[0] != "shared" {
		return errors.New("internal launch requires a VM")
	}
	v, err := a.loadVM()
	if err != nil {
		return err
	}
	if v == nil {
		return errors.New("there is no nsl VM")
	}
	if err = a.runtimeFiles(v); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(v.dir, "nsl.vm"))
	if err != nil {
		return err
	}
	var cred protocol.Credential
	if err = protocol.DecodeStrict(b, &cred); err != nil {
		return err
	}
	if err = cred.Validate(); err != nil {
		return err
	}
	// ExtraFiles from _devices are 3/4, inherited through sg/unshare. Verify the
	// actual devices before advertising them to vmspawn's socket activation API.
	for i, path := range []string{"/dev/kvm", "/dev/vhost-vsock"} {
		var got, want syscall.Stat_t
		if err := syscall.Fstat(3+i, &got); err != nil {
			return err
		}
		if err := syscall.Stat(path, &want); err != nil {
			return err
		}
		if got.Rdev != want.Rdev || got.Mode&syscall.S_IFMT != syscall.S_IFCHR {
			return errors.New("unexpected VM device descriptor")
		}
	}
	bin, err := exec.LookPath("systemd-vmspawn")
	if err != nil {
		return err
	}
	env := []string{}
	for _, s := range os.Environ() {
		if !strings.HasPrefix(s, "LISTEN_") {
			env = append(env, s)
		}
	}
	env = append(env, "LISTEN_FDS=2", "LISTEN_FDNAMES=kvm:vhost-vsock", "LISTEN_PID="+strconv.Itoa(os.Getpid()))
	return syscall.Exec(bin, append([]string{bin}, a.launchArgs(v, &cred)...), env)
}

func (a *app) doctor() error {
	failed := false
	for _, tool := range []string{"systemd-vmspawn", "systemd-run", "systemctl", "qemu-system-x86_64", "qemu-img", "ssh", "ssh-keygen", "sg", "unshare", "/usr/libexec/virtiofsd", "/usr/lib/systemd/systemd-ssh-proxy"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			failed = true
			fmt.Fprintln(a.out, "MISSING", tool)
		} else {
			fmt.Fprintln(a.out, "OK", p)
		}
	}
	for _, path := range []string{"/dev/kvm", "/dev/vhost-vsock"} {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			fmt.Fprintf(a.out, "SESSION %s: %v (launch uses existing kvm membership through sg)\n", path, err)
		} else {
			f.Close()
			fmt.Fprintln(a.out, "OK", path)
		}
	}
	// Check refreshed group access without changing membership or device modes.
	if _, err := a.capture(5*time.Second, "sg", "kvm", "-c", "test -r /dev/kvm && test -w /dev/kvm && test -r /dev/vhost-vsock && test -w /dev/vhost-vsock"); err != nil {
		failed = true
		fmt.Fprintln(a.out, "KVM/vsock group access:", err)
	}
	if _, err := a.capture(5*time.Second, "unshare", "--user", "--map-current-user", "true"); err != nil {
		failed = true
		fmt.Fprintln(a.out, "User namespaces:", err)
	}
	if _, err := a.capture(5*time.Second, "systemctl", "--user", "show-environment"); err != nil {
		failed = true
		fmt.Fprintln(a.out, "User systemd:", err)
	}
	if p, err := exec.LookPath(a.waypipe); err == nil {
		fmt.Fprintln(a.out, "GUI", p)
	} else {
		fmt.Fprintln(a.out, "GUI: Waypipe unavailable (optional)")
	}
	if failed {
		return errors.New("missing VM prerequisites")
	}
	return nil
}
