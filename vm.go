package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func unit(e *environment) string     { return fmt.Sprintf("nsl-%d-%s.service", e.Owner, e.ID) }
func portUnit(e *environment) string { return fmt.Sprintf("nsl-%d-%s-ports.service", e.Owner, e.ID) }
func cid(e *environment) uint32 {
	b, _ := hex.DecodeString(e.ID)
	return 0x40000000 | (binary.BigEndian.Uint32(b[:4]) & 0x3fffffff)
}
func description(e *environment) string { return "nsl environment " + e.ID }
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
func (a *app) unitState(e *environment, name string) (string, error) {
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
	if values["Description"] != description(e) {
		return "", errors.New("refusing foreign systemd unit " + name)
	}
	return values["ActiveState"], nil
}
func (a *app) status(e *environment) (string, error) {
	if e.ResizeTarget != 0 {
		return "Resizing", nil
	}
	if !e.Prepared {
		return "Incomplete", nil
	}
	state, err := a.unitState(e, unit(e))
	if err != nil {
		return "", err
	}
	switch state {
	case "active", "activating":
		return "Running", nil
	case "failed":
		return "Failed", nil
	case "deactivating":
		return "Stopping", nil
	default:
		return "Stopped", nil
	}
}
func (a *app) runtimeFiles(e *environment) error {
	for _, p := range []string{"disk.qcow2", "boot.json", "ssh.config", "keys/identity"} {
		if err := privateFile(filepath.Join(a.dir(e.Name), p), a.uid, 0077); err != nil {
			return err
		}
	}
	if err := checkPrivateDir(filepath.Join(a.dir(e.Name), "keys"), a.uid); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.socket(e)), 0700); err != nil {
		return err
	}
	return checkPrivateDir(filepath.Dir(a.socket(e)), a.uid)
}
func (a *app) ready(e *environment) error {
	request, _ := encodeRequest([]string{"cat", "/var/lib/nsl/identity.json"}, "")
	args := append(a.sshArgs(e, false), "/usr/local/libexec/nsl-exec", request)
	b, err := a.capture(5*time.Second, "ssh", args...)
	if err != nil {
		return err
	}
	var identity struct {
		Version int    `json:"version"`
		ID      string `json:"id"`
		UID     int    `json:"uid"`
		GID     int    `json:"gid"`
	}
	if err = json.Unmarshal(b, &identity); err != nil {
		return err
	}
	if identity.Version != 1 || identity.ID != guestID(e) || identity.UID != e.Owner || identity.GID != e.GID {
		return errors.New("guest identity does not match environment")
	}
	return nil
}
func (a *app) start(e *environment) error {
	l, e, err := a.lockOwned(e)
	if err != nil {
		return err
	}
	defer unlock(l)
	return a.startLocked(e)
}
func (a *app) startLocked(e *environment) error {
	if e.ResizeTarget != 0 {
		return fmt.Errorf("disk growth incomplete; run nsl recover %s", e.Name)
	}
	if !e.Prepared {
		return fmt.Errorf("creation incomplete; run nsl recover %s", e.Name)
	}
	if err := a.runtimeFiles(e); err != nil {
		return err
	}
	if e.Project != "" {
		p, err := projectPath(e.Project)
		if err != nil {
			return err
		}
		if p != e.Project {
			return errors.New("project path changed; refusing a different share")
		}
	}
	state, err := a.unitState(e, unit(e))
	if err != nil {
		return err
	}
	if state == "deactivating" {
		return errors.New("VM is stopping; retry after it stops")
	}
	if state != "active" && state != "activating" {
		if state == "failed" {
			if _, err = a.capture(5*time.Second, "systemctl", "--user", "reset-failed", unit(e)); err != nil {
				return err
			}
		}
		script := "exec " + shellQuote(a.self) + " _devices " + shellQuote(e.Name)
		err = a.call(nil, a.err, "systemd-run", "--user", "--unit="+unit(e), "--description="+description(e), "--collect", "--property=Type=exec", "--property=TimeoutStopSec=30", "--property=KillMode=mixed", "--setenv=NSL_HOME="+a.home, "--setenv=NSL_DEBUG="+os.Getenv("NSL_DEBUG"), "--", "sg", "kvm", "-c", script)
		if err != nil {
			return err
		}
	}
	deadline := time.Now().Add(90 * time.Second)
	var readiness error
	for time.Now().Before(deadline) {
		state, err = a.unitState(e, unit(e))
		if err != nil {
			return err
		}
		if state != "active" && state != "activating" {
			return fmt.Errorf("VM exited; run nsl logs %s, then nsl recover %s", e.Name, e.Name)
		}
		if readiness = a.ready(e); readiness == nil {
			if !e.Initialized {
				// First-use identity and SSH host keys must reach stable storage
				// before we report a newly provisioned guest ready.
				request, _ := encodeRequest([]string{"sync"}, "")
				if _, err = a.capture(20*time.Second, "ssh", append(a.sshArgs(e, false), "/usr/local/libexec/nsl-exec", request)...); err != nil {
					return err
				}
				e.Initialized = true
				if err = a.save(e); err != nil {
					return err
				}
			}
			return a.startForwarder(e)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("guest readiness timed out; disk retained; use nsl logs %s or nsl recover %s: %w", e.Name, e.Name, readiness)
}
func (a *app) startForwarder(e *environment) error {
	state, err := a.unitState(e, portUnit(e))
	if err != nil {
		return err
	}
	if state == "active" || state == "activating" {
		return nil
	}
	if state == "failed" {
		if _, err = a.capture(5*time.Second, "systemctl", "--user", "reset-failed", portUnit(e)); err != nil {
			return err
		}
	}
	return a.call(nil, a.err, "systemd-run", "--user", "--unit="+portUnit(e), "--description="+description(e), "--collect", "--property=Type=exec", "--property=BindsTo="+unit(e), "--property=After="+unit(e), "--setenv=NSL_HOME="+a.home, "--", a.self, "_forward", e.Name)
}
func (a *app) stop(e *environment) error {
	l, e, err := a.lockOwned(e)
	if err != nil {
		return err
	}
	defer unlock(l)
	return a.stopLocked(e)
}
func (a *app) stopLocked(e *environment) error {
	state, err := a.unitState(e, portUnit(e))
	if err != nil {
		return err
	}
	if state != "inactive" {
		if _, err = a.capture(40*time.Second, "systemctl", "--user", "stop", portUnit(e)); err != nil {
			return err
		}
	}
	state, err = a.unitState(e, unit(e))
	if err != nil {
		return err
	}
	if state == "active" || state == "activating" {
		request, _ := encodeRequest([]string{"systemctl", "poweroff"}, "")
		_, _ = a.capture(5*time.Second, "ssh", append(a.sshArgs(e, false), "sudo", "-n", "--", "/usr/local/libexec/nsl-exec", request)...)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			state, err = a.unitState(e, unit(e))
			if err != nil {
				return err
			}
			if state == "inactive" || state == "failed" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if state != "inactive" {
		if _, err = a.capture(40*time.Second, "systemctl", "--user", "stop", unit(e)); err != nil {
			return err
		}
	}
	if e.Prepared {
		_, _ = a.capture(5*time.Second, "ssh", "-F", filepath.Join(a.dir(e.Name), "ssh.config"), "-O", "exit", "guest")
	}
	return nil
}
func (a *app) recover(e *environment) error {
	l, e, err := a.lockOwned(e)
	if err != nil {
		return err
	}
	defer unlock(l)
	if err = a.stopLocked(e); err != nil {
		return err
	}
	if e.ResizeTarget != 0 {
		if err = a.finishGrowth(e); err != nil {
			return err
		}
	}
	if err = a.prepare(e); err != nil {
		return err
	}
	if err = a.startLocked(e); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Recovered %s; existing disk and SSH trust preserved\n", e.Name)
	return nil
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (a *app) devices(args []string) error {
	if len(args) != 1 {
		return errors.New("internal device launch requires an environment")
	}
	if err := checkName(args[0]); err != nil {
		return err
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
	script := "exec unshare --user --map-current-user --keep-caps " + shellQuote(a.self) + " _launch " + shellQuote(args[0])
	c := exec.Command("sg", group.Name, "-c", script)
	c.Env = os.Environ()
	c.ExtraFiles = []*os.File{kvm, vsock}
	c.Stdin, c.Stdout, c.Stderr = a.in, a.out, a.err
	return c.Run()
}
func (a *app) launchArgs(e *environment) []string {
	args := []string{"--user", "--no-ask-password", "--keep-unit", "--register=no", "--image=" + filepath.Join(a.dir(e.Name), "disk.qcow2"), "--image-format=qcow2", "--machine=nsl-" + e.ID, "--cpus=" + strconv.Itoa(e.CPUs), "--ram=" + strconv.Itoa(e.Memory) + "G", "--kvm=yes", "--vsock=yes", "--vsock-cid=" + strconv.FormatUint(uint64(cid(e)), 10), "--tpm=no", "--secure-boot=no", "--network-user-mode", "--notify-ready=no", "--pass-ssh-key=no", "--console=read-only", "--load-credential=nsl.config:" + filepath.Join(a.dir(e.Name), "boot.json")}
	if e.Project != "" {
		args = append(args, "--bind="+e.Project+":/work")
	}
	if os.Getenv("NSL_DEBUG") == "1" {
		args = append(args, "systemd.journald.forward_to_console=yes")
	}
	// Request the transport explicitly: Debian's regenerated initramfs may
	// load virtio-vsock after the SSH generator's automatic detection runs.
	return append(args, "rw", "systemd.ssh_auto=no", "systemd.ssh_listen=vsock::22")
}
func (a *app) launch(e *environment) error {
	if err := a.runtimeFiles(e); err != nil {
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
	return syscall.Exec(bin, append([]string{bin}, a.launchArgs(e)...), env)
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
