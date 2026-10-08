package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frostyard/nsl/internal/protocol"
)

// withIsolated has a shared machine, debian, and an isolated one, iso.
func withIsolated(t *testing.T) (*app, *fakeRunner, *machineRecord, *vmRecord) {
	t.Helper()
	a, f, _ := withMachine(t)
	if err := a.create(append([]string{"iso", "--isolated"}, machineImage(t, "rootfs")...)); err != nil {
		t.Fatal(err)
	}
	m, err := a.machine("iso")
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.vmOf(m)
	if err != nil || v == nil {
		t.Fatal(err)
	}
	return a, f, m, v
}

func TestIsolatedMachineGetsItsOwnVM(t *testing.T) {
	a, f, m, v := withIsolated(t)
	shared, _ := a.loadVM()
	if m.Tier != "isolated" || v.Role != "isolated" || v.Machine.Name != "iso" || v.Machine.ID != m.ID || v.dir != a.isolatedVMDir("iso") ||
		v.Image != shared.Image || v.ID == shared.ID || cid(v) == cid(shared) || v.CPUs != 2 || v.Memory != 2 {
		t.Fatalf("%+v", v)
	}
	create := f.requests[len(f.requests)-1]
	if create.Op != "create" || create.Machine != "iso" || create.ID != m.ID {
		t.Fatalf("%+v", create)
	}
	cred := credentialOf(t, v)
	if cred.Role != "isolated" || cred.Machine == nil || cred.Machine.Name != "iso" || len(cred.Shares) != 0 || len(cred.Aliases) != 0 || cred.Autostart {
		t.Fatalf("%+v", cred)
	}
	args := strings.Join(a.launchArgs(v, &cred), " ")
	if strings.Contains(args, "--bind=") || !strings.Contains(args, "--cpus=2") || !strings.Contains(args, "--ram=2G") ||
		!strings.Contains(args, "--image="+v.dir+"/root.raw") {
		t.Fatal(args)
	}
	launched := 0
	for _, c := range f.calls {
		if c.Bin == "systemd-run" && strings.Contains(strings.Join(c.Args, " "), "--unit="+vmUnit(v)) {
			launched++
			if !strings.HasSuffix(strings.Join(c.Args, " "), "_devices "+v.ID) {
				t.Fatal(c.Args)
			}
		}
	}
	if launched != 1 {
		t.Fatal("isolated VM launched", launched, "times")
	}
	// The isolated VM answers readiness as its own machine's VM.
	if _, err := a.ready(v); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedMachineSeesNoHostFilesOrDesktop(t *testing.T) {
	a, f, _, v := withIsolated(t)
	project := filepath.Join(a.hostRoot, "home", "u", "project")
	os.MkdirAll(project, 0755)
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(project)
	before := len(f.requests)
	if err := a.execute([]string{"run", "-m", "iso", "pwd"}); err == nil || !strings.Contains(err.Error(), "not shared with iso") {
		t.Fatal(err)
	}
	if len(f.requests) != before {
		t.Fatal("ran a command in the wrong directory")
	}
	var stderr bytes.Buffer
	a.err = &stderr
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	a.waypipe = "true"
	if err := a.execute([]string{"-m", "iso"}); err != nil || !strings.Contains(stderr.String(), "starting in the home directory") {
		t.Fatal(err, stderr.String())
	}
	if shell := f.requests[len(f.requests)-1]; shell.Op != "run" || shell.Directory != "" {
		t.Fatalf("%+v", shell)
	}
	if _, ok := f.states[desktopUnit(v, "iso")]; ok {
		t.Fatal("started a desktop session for an isolated machine")
	}
	if err := a.execute([]string{"run", "-m", "iso", "--cd", "/srv", "pwd"}); err != nil {
		t.Fatal(err)
	}
}

func TestVMCommandsCoverEveryVM(t *testing.T) {
	a, f, _, v := withIsolated(t)
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"list"}); err != nil {
		t.Fatal(err)
	}
	listing := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{"shared running", "iso running nsl-vm-trixie-x86-64-r1 2 CPUs, 2 GiB 128 GiB", "iso stopped nsl-machine-debian-trixie-x86-64-r1 isolated"} {
		if !strings.Contains(listing, want) {
			t.Fatalf("missing %q in %s", want, listing)
		}
	}
	image, digest := localImage(t, "newer vm image")
	if err := a.execute([]string{"update", "--image", image, "--digest", digest}); err != nil {
		t.Fatal(err)
	}
	all, _ := a.allVMs()
	for _, vm := range all {
		if vm.PendingImage != strings.TrimPrefix(digest, "sha256:") {
			t.Fatalf("%s: %+v", vm.label(), vm)
		}
	}
	if err := a.execute([]string{"shutdown"}); err != nil {
		t.Fatal(err)
	}
	for _, vm := range all {
		if state, _ := a.vmState(vm); state != "stopped" {
			t.Fatal(vm.label(), state)
		}
	}
	if err := a.execute([]string{"resize", "iso", "--disk", "200"}); err != nil {
		t.Fatal(err)
	}
	if v, _ = a.loadVMAt(v.dir); v.DataGiB != 200 {
		t.Fatalf("%+v", v)
	}
	if shared, _ := a.loadVM(); shared.DataGiB != defaultDataGiB {
		t.Fatal("resized the shared VM")
	}
	if err := a.execute([]string{"recover", "iso"}); err != nil {
		t.Fatal(err)
	}
	if state, _ := a.vmState(v); state != "running" || f.states[vmUnit(all[0])] == "active" {
		t.Fatal("recover iso touched the wrong VM")
	}
	if err := a.execute([]string{"recover", "debian"}); err == nil || !strings.Contains(err.Error(), "shared VM") {
		t.Fatal(err)
	}
}

func TestRemovingAnIsolatedMachineDeletesItsVM(t *testing.T) {
	a, f, m, v := withIsolated(t)
	state := "running"
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "machines" {
			json.NewEncoder(stdout).Encode([]protocol.MachineStatus{{Machine: "iso", ID: m.ID, State: state}})
			return errAnswered
		}
		return nil
	}
	if err := a.remove([]string{"iso", "--yes"}); err == nil || !strings.Contains(err.Error(), "stop iso first") {
		t.Fatal(err)
	}
	if _, err := a.machine("iso"); err != nil {
		t.Fatal("a refused removal lost the machine:", err)
	}
	state = "stopped"
	before := len(f.requests)
	if err := a.remove([]string{"iso", "--yes"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests[before:] {
		if r.Op == "remove" {
			t.Fatal("asked a VM to remove a machine whose VM is deleted anyway")
		}
	}
	if state, _ := a.vmState(v); state != "stopped" {
		t.Fatal("left the isolated VM running")
	}
	entries, _ := os.ReadDir(a.isolatedDir())
	if len(entries) != 0 {
		t.Fatal("left the isolated VM's files:", entries)
	}
	if shared, err := a.loadVM(); err != nil || shared == nil {
		t.Fatal("removed the shared VM", err)
	}
}

func TestFailedIsolatedCreateLeavesNoVM(t *testing.T) {
	a, f, _ := startedVM(t)
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "create" {
			return &protocol.Error{Code: protocol.CodeFailed, Message: "invalid root filesystem"}
		}
		return nil
	}
	if err := a.create(append([]string{"iso", "--isolated"}, machineImage(t, "rootfs")...)); err == nil {
		t.Fatal("created a broken machine")
	}
	if entries, _ := os.ReadDir(a.isolatedDir()); len(entries) != 0 {
		t.Fatal("left an isolated VM behind:", entries)
	}
	if _, err := os.Lstat(a.machinePath("iso")); !os.IsNotExist(err) {
		t.Fatal("kept the record")
	}
}

func TestIsolatedRemovalRequiresConfirmedStatus(t *testing.T) {
	for _, scenario := range []string{"transport", "missing", "replacement", "stopping", "foreign-unit"} {
		t.Run(scenario, func(t *testing.T) {
			a, f, m, v := withIsolated(t)
			if scenario == "stopping" {
				f.states[vmUnit(v)] = "deactivating"
			}
			if scenario == "foreign-unit" {
				f.descriptions[vmUnit(v)] = "someone else's VM"
			}
			f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
				if req.Op != "machines" {
					return nil
				}
				if scenario == "transport" {
					return errors.New("connection lost")
				}
				statuses := []protocol.MachineStatus{}
				if scenario == "replacement" {
					statuses = append(statuses, protocol.MachineStatus{Machine: m.Name, ID: randomID(), State: "stopped"})
				}
				if err := json.NewEncoder(stdout).Encode(statuses); err != nil {
					return err
				}
				return errAnswered
			}
			if err := a.remove([]string{m.Name, "--yes"}); err == nil {
				t.Fatal("removed a machine without confirming its state")
			}
			if _, err := os.Stat(filepath.Join(v.dir, "data.raw")); err != nil {
				t.Fatal("lost data disk:", err)
			}
			for _, req := range f.requests {
				if req.Op == "vm" {
					t.Fatal("powered off a VM whose machine status was not confirmed")
				}
			}
			// A later confirmed VM shutdown allows the pending removal to resume.
			f.states[vmUnit(v)], f.descriptions[vmUnit(v)] = "inactive", vmDescription(v)
			if err := a.remove([]string{m.Name, "--yes"}); err != nil {
				t.Fatal("could not resume removal:", err)
			}
		})
	}
}

func TestIsolatedRemovalOfStoppedVMNeedsNoAgent(t *testing.T) {
	a, f, m, v := withIsolated(t)
	f.states[vmUnit(v)] = "inactive"
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		t.Fatal("contacted stopped VM")
		return nil
	}
	if err := a.remove([]string{m.Name, "--yes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(v.dir); !os.IsNotExist(err) {
		t.Fatal("kept VM:", err)
	}
}
