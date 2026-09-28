package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
	"golang.org/x/sys/unix"
)

const (
	stateDir    = "/var/lib/nsl"
	recordsDir  = stateDir + "/machines"
	machinesDir = "/var/lib/machines"
	settingsDir = "/run/systemd/nspawn"
	runtimeDir  = "/run/nsl"
	// systemd's poweroff request to a container's PID 1 is SIGRTMIN+4, with
	// glibc's SIGRTMIN of 34.
	signalPoweroff = 38
)

func unitOf(name string) string { return "systemd-nspawn@" + name + ".service" }

func monotonic() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func (a *agent) binding() (*protocol.Binding, error) {
	b, err := os.ReadFile(a.path(stateDir + "/identity.json"))
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeFailed, Message: "VM identity is not set up: " + err.Error()}
	}
	var binding protocol.Binding
	if err = protocol.DecodeStrict(b, &binding); err == nil {
		err = binding.Validate()
	}
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeFailed, Message: "invalid VM identity: " + err.Error()}
	}
	return &binding, nil
}

func (a *agent) identity(binding *protocol.Binding) error {
	descriptor, err := os.ReadFile(a.path("/usr/lib/nsl/image.json"))
	if err != nil {
		return err
	}
	return json.NewEncoder(a.stdout).Encode(protocol.Identity{Protocol: protocol.Version, VM: *binding, Image: json.RawMessage(bytes.TrimSpace(descriptor))})
}

func (a *agent) loadRecord(name string) (*protocol.MachineRecord, error) {
	b, err := os.ReadFile(a.path(recordsDir + "/" + name + ".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, &protocol.Error{Code: protocol.CodeUnknownMachine, Message: "no machine " + name + " in this VM"}
	}
	if err != nil {
		return nil, err
	}
	var r protocol.MachineRecord
	if err = protocol.DecodeStrict(b, &r); err != nil {
		return nil, fmt.Errorf("invalid record for %s: %w", name, err)
	}
	if r.Schema != 1 || r.Name != name || !protocol.ValidID(r.ID) {
		return nil, fmt.Errorf("invalid record for %s", name)
	}
	return &r, nil
}

// record loads a machine's record and checks it is the one the host means.
func (a *agent) record(req *protocol.Request) (*protocol.MachineRecord, error) {
	r, err := a.loadRecord(req.Machine)
	if err != nil {
		return nil, err
	}
	if r.ID != req.ID {
		return nil, &protocol.Error{Code: protocol.CodeMachineID, Message: req.Machine + " in this VM has another ID"}
	}
	return r, nil
}

func (a *agent) records() ([]*protocol.MachineRecord, error) {
	entries, err := os.ReadDir(a.path(recordsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*protocol.MachineRecord
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !protocol.ValidName(name) {
			continue
		}
		r, err := a.loadRecord(name)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// state maps the machine's nspawn unit to running, starting, stopping or stopped.
func (a *agent) state(name string) (string, error) {
	s, err := a.sys.UnitState(unitOf(name))
	switch {
	case err != nil:
		return "", err
	case s == "active" || s == "reloading":
		return "running", nil
	case s == "activating":
		return "starting", nil
	case s == "deactivating":
		return "stopping", nil
	}
	return "stopped", nil
}

func (a *agent) machines() error {
	records, err := a.records()
	if err != nil {
		return err
	}
	out := []protocol.MachineStatus{}
	for _, r := range records {
		s := protocol.MachineStatus{Machine: r.Name, ID: r.ID, BuildID: r.BuildID}
		if s.State, err = a.state(r.Name); err != nil {
			return err
		}
		if s.State == "running" {
			if m, err := a.sys.Machine(r.Name); err == nil {
				s.Sessions, _ = m.Sessions()
				m.Close()
			}
		}
		out = append(out, s)
	}
	return json.NewEncoder(a.stdout).Encode(out)
}

func (a *agent) saveIdleTimeout(minutes int) {
	// The idle monitor reads the latest value; losing it only delays idle stop.
	_ = os.MkdirAll(a.path(runtimeDir), 0755)
	_ = os.WriteFile(a.path(runtimeDir+"/idle-timeout"), []byte(strconv.Itoa(minutes)+"\n"), 0644)
}

func (a *agent) start(req *protocol.Request, binding *protocol.Binding) error {
	if _, err := a.record(req); err != nil {
		return err
	}
	// Settings live in /run; rewrite them so nspawn never falls back to its defaults.
	if err := a.writeSettings(req.Machine, binding.Role); err != nil {
		return err
	}
	a.saveIdleTimeout(*req.IdleTimeout)
	began := a.now()
	state, err := a.state(req.Machine)
	if err != nil {
		return err
	}
	if state == "stopping" {
		return &protocol.Error{Code: protocol.CodeBusy, Message: req.Machine + " is stopping; retry after it stops"}
	}
	if state == "stopped" {
		if err = a.sys.StartUnit(unitOf(req.Machine)); err != nil {
			return err
		}
	}
	system, err := a.waitRunning(req.Machine, began+60)
	if err != nil {
		return err
	}
	return json.NewEncoder(a.stdout).Encode(protocol.StartResult{State: system, Seconds: float64(int((a.now()-began)*1000)) / 1000})
}

// waitRunning waits for the machine's manager to finish starting.
func (a *agent) waitRunning(name string, deadline float64) (string, error) {
	last := "unreachable"
	began := a.now()
	for a.now() < deadline {
		unit, err := a.sys.UnitState(unitOf(name))
		if err != nil {
			return "", err
		}
		// A queued start job leaves the unit inactive for a moment.
		if unit == "failed" || unit == "deactivating" || (unit == "inactive" && a.now()-began > 5) {
			return "", fmt.Errorf("%s exited while starting; see journalctl -u %s in the VM", name, unitOf(name))
		}
		if m, err := a.sys.Machine(name); err == nil {
			last, err = m.SystemState()
			m.Close()
			if err == nil && (last == "running" || last == "degraded") {
				return last, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("%s did not finish starting within 60 s (system state %s)", name, last)
}

func (a *agent) stop(req *protocol.Request) error {
	if _, err := a.record(req); err != nil {
		return err
	}
	state, err := a.state(req.Machine)
	if err != nil || state == "stopped" {
		return err
	}
	if state == "running" {
		// A failed request still falls back to terminating the machine below.
		_ = a.sys.KillMachine(req.Machine, signalPoweroff)
	}
	deadline := a.now() + 30
	for a.now() < deadline {
		if state, err = a.state(req.Machine); err != nil || state == "stopped" {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = a.sys.TerminateMachine(req.Machine)
	return a.sys.StopUnit(unitOf(req.Machine))
}

func (a *agent) requireStopped(name string) error {
	state, err := a.state(name)
	if err == nil && state != "stopped" {
		err = &protocol.Error{Code: protocol.CodeBusy, Message: name + " is " + state + "; stop it first"}
	}
	return err
}

// nspawnSettings are the per-machine settings nspawn reads from /run.
func nspawnSettings(role string) string {
	s := "[Exec]\nBoot=yes\nPrivateUsers=no\n# Creation links /etc/localtime to the host's zone.\nTimezone=off\n\n" +
		"[Network]\nVirtualEthernet=no\n\n" +
		"[Files]\n# Machines share the VM's network namespace, so they use its resolver.\nBindReadOnly=/run/systemd/resolve\n"
	if role == "shared" {
		s += "Bind=/mnt/host\n"
	}
	return s
}

func (a *agent) writeSettings(name, role string) error {
	if err := os.MkdirAll(a.path(settingsDir), 0755); err != nil {
		return err
	}
	return atomicWrite(a.path(settingsDir+"/"+name+".nspawn"), []byte(nspawnSettings(role)), 0644)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".nsl-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		if _, err = f.Write(data); err == nil {
			err = f.Sync()
		}
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err == nil {
		err = syncDir(filepath.Dir(path))
	}
	return err
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (a *agent) create(req *protocol.Request, binding *protocol.Binding) (err error) {
	name, staging := req.Machine, a.path(machinesDir+"/.nsl-create-"+req.ID)
	for _, p := range []string{recordsDir + "/" + name + ".json", machinesDir + "/" + name} {
		if _, err := os.Lstat(a.path(p)); !errors.Is(err, os.ErrNotExist) {
			return &protocol.Error{Code: protocol.CodeBusy, Message: p + " already exists in the VM"}
		}
	}
	copied := staging + ".tar.zst"
	published := false
	defer func() {
		// Leave nothing behind on failure; the name stays free for a retry.
		_ = os.Remove(copied)
		if err != nil && !published {
			a.deleteSubvolume(staging)
		}
	}()
	if err = a.copyVerified(req.Image, copied); err != nil {
		return err
	}
	if err = validateRootfs(copied, rootfsLimit); err != nil {
		return err
	}
	if err = a.btrfs("subvolume", "create", staging); err != nil {
		return err
	}
	if err = a.extract(copied, staging); err != nil {
		return err
	}
	buildID, err := checkMachineDescriptor(staging, req.Image.BuildID)
	if err != nil {
		return err
	}
	if err = a.personalize(staging, name, *req.Account, req.TimeZone, true); err != nil {
		return err
	}
	record := protocol.MachineRecord{Schema: 1, Name: name, ID: req.ID, Account: *req.Account, BuildID: buildID,
		Created: time.Now().UTC().Format(time.RFC3339)}
	if err = a.publish(staging, record, binding.Role, &published); err != nil {
		return err
	}
	return json.NewEncoder(a.stdout).Encode(protocol.CreateResult{BuildID: buildID})
}

// publish records the machine, then moves its tree under its name.
func (a *agent) publish(staging string, record protocol.MachineRecord, role string, published *bool) error {
	b, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(a.path(recordsDir), 0700); err != nil {
		return err
	}
	path := a.path(recordsDir + "/" + record.Name + ".json")
	if err = atomicWrite(path, append(b, '\n'), 0600); err != nil {
		return err
	}
	if err = unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, a.path(machinesDir+"/"+record.Name), unix.RENAME_NOREPLACE); err != nil {
		_ = os.Remove(path)
		return err
	}
	*published = true
	return a.writeSettings(record.Name, role)
}

func (a *agent) remove(req *protocol.Request) error {
	if _, err := a.record(req); err != nil {
		return err
	}
	if err := a.requireStopped(req.Machine); err != nil {
		return err
	}
	for _, p := range []string{machinesDir + "/" + req.Machine, machinesDir + "/.nsl-create-" + req.ID} {
		if _, err := os.Lstat(a.path(p)); err == nil {
			if err = a.btrfs("subvolume", "delete", "--recursive", a.path(p)); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(a.path(settingsDir + "/" + req.Machine + ".nspawn")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// The record goes last, so an interrupted removal can resume.
	return os.Remove(a.path(recordsDir + "/" + req.Machine + ".json"))
}

func (a *agent) btrfs(args ...string) error {
	var stderr bytes.Buffer
	if err := a.r.run(context.Background(), nil, io.Discard, &stderr, append([]string{"btrfs"}, args...)...); err != nil {
		return fmt.Errorf("btrfs %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (a *agent) deleteSubvolume(path string) {
	if _, err := os.Lstat(path); err == nil {
		if err = a.btrfs("subvolume", "delete", "--recursive", path); err != nil {
			fmt.Fprintln(a.stderr, "nsl-agent: cleanup:", err)
		}
	}
}

// copyVerified copies the image from the read-only share into private storage,
// checking its size and digest, so later reads cannot see different bytes.
func (a *agent) copyVerified(image *protocol.Image, destination string) error {
	source, err := os.Open(a.path(image.Path))
	if err != nil {
		return err
	}
	defer source.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(source, image.Size+1))
	if err != nil {
		return err
	}
	if n != image.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != image.Digest {
		return &protocol.Error{Code: protocol.CodeFailed, Message: "machine image does not match its verified digest and size"}
	}
	return out.Sync()
}

// extract unpacks a validated root filesystem, keeping numeric owners, modes,
// xattrs (including file capabilities) and ACLs.
func (a *agent) extract(archive, destination string) error {
	var stderr bytes.Buffer
	err := a.r.run(context.Background(), nil, io.Discard, &stderr, "tar", "--extract", "--zstd", "--file", archive,
		"--directory", destination, "--numeric-owner", "--same-owner", "--same-permissions",
		"--xattrs", "--xattrs-include=*", "--acls")
	if err != nil {
		return fmt.Errorf("extracting the root filesystem: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// personalize applies per-machine data before anything runs in the tree.
func (a *agent) personalize(dir, name string, account protocol.Account, zone string, newAccount bool) error {
	t, err := openTree(dir)
	if err != nil {
		return err
	}
	defer t.close()
	if err = t.symlink("../usr/share/zoneinfo/"+zone, "etc/localtime"); err != nil {
		return err
	}
	if err = t.writeFile("etc/hostname", []byte(name+"\n"), 0644); err != nil {
		return err
	}
	hosts, err := t.readFile("etc/hosts")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !hasHost(string(hosts), name) {
		if len(hosts) > 0 && !bytes.HasSuffix(hosts, []byte("\n")) {
			hosts = append(hosts, '\n')
		}
		hosts = append(hosts, []byte("127.0.1.1\t"+name+"\n")...)
		if err = t.writeFile("etc/hosts", hosts, 0644); err != nil {
			return err
		}
	}
	if newAccount {
		if err = a.addAccount(dir, account); err != nil {
			return err
		}
	}
	if err = t.mkdir("etc/sudoers.d", 0750); err != nil {
		return err
	}
	return t.writeFile("etc/sudoers.d/nsl", []byte(account.User+" ALL=(ALL) NOPASSWD: ALL\n"), 0440)
}

func hasHost(hosts, name string) bool {
	for _, line := range strings.Split(hosts, "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		for _, f := range fields[min(1, len(fields)):] {
			if f == name {
				return true
			}
		}
	}
	return false
}

// offline runs a command inside a stopped machine's tree without booting it.
func (a *agent) offline(dir string, stdout io.Writer, argv ...string) error {
	var stderr bytes.Buffer
	args := append([]string{"systemd-nspawn", "--quiet", "--register=no", "--pipe", "--timezone=off", "--resolv-conf=off",
		"--link-journal=no", "--machine=nsl-staging", "--directory=" + dir, "--"}, argv...)
	if err := a.r.run(context.Background(), nil, stdout, &stderr, args...); err != nil {
		return fmt.Errorf("%s in the new machine: %v: %s", argv[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (a *agent) addAccount(dir string, account protocol.Account) error {
	var passwd bytes.Buffer
	if err := a.offline(dir, &passwd, "getent", "passwd", account.User, strconv.Itoa(account.UID)); err == nil || passwd.Len() > 0 {
		return &protocol.Error{Code: protocol.CodeFailed, Message: fmt.Sprintf("the image already has an account named %s or with UID %d", account.User, account.UID)}
	}
	var group bytes.Buffer
	if a.offline(dir, &group, "getent", "group", strconv.Itoa(account.GID)) != nil || group.Len() == 0 {
		if err := a.offline(dir, io.Discard, "groupadd", "--gid", strconv.Itoa(account.GID), account.Group); err != nil {
			return err
		}
	}
	return a.offline(dir, io.Discard, "useradd", "--uid", strconv.Itoa(account.UID), "--gid", strconv.Itoa(account.GID),
		"--create-home", "--shell", "/bin/bash", account.User)
}

// checkMachineDescriptor refuses an image built for another machine layer, or
// another build than the host selected, and returns the image's build ID.
func checkMachineDescriptor(dir, expected string) (string, error) {
	t, err := openTree(dir)
	if err != nil {
		return "", err
	}
	defer t.close()
	b, err := t.readFile("usr/lib/nsl/machine.json")
	if err != nil {
		return "", &protocol.Error{Code: protocol.CodeRefused, Message: "not an nsl machine image: " + err.Error()}
	}
	var d struct {
		Role            string `json:"role"`
		BuildID         string `json:"build_id"`
		Architecture    string `json:"architecture"`
		MachineProtocol int    `json:"machine_protocol"`
	}
	if err = json.Unmarshal(b, &d); err != nil || d.Role != "machine" || d.Architecture != "x86-64" || d.MachineProtocol != protocol.MachineVersion || !protocol.ValidBuildID(d.BuildID) {
		return "", &protocol.Error{Code: protocol.CodeRefused, Message: fmt.Sprintf("machine image needs machine protocol %d on x86-64", protocol.MachineVersion)}
	}
	if expected != "" && d.BuildID != expected {
		return "", &protocol.Error{Code: protocol.CodeRefused, Message: "the image is " + d.BuildID + ", not the selected " + expected}
	}
	return d.BuildID, nil
}
