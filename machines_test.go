package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frostyard/nsl/internal/protocol"
)

func machineImage(t *testing.T, content string) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rootfs.tar.zst")
	os.WriteFile(p, []byte(content), 0600)
	h := sha256.Sum256([]byte(content))
	return []string{"--image", p, "--digest", "sha256:" + hex.EncodeToString(h[:])}
}

func (f *fakeRunner) ops() []string {
	var ops []string
	for _, r := range f.requests {
		ops = append(ops, r.Op)
	}
	return ops
}

func withMachine(t *testing.T) (*app, *fakeRunner, *machineRecord) {
	t.Helper()
	a, f, _ := startedVM(t)
	if err := a.create(append([]string{"debian"}, machineImage(t, "rootfs")...)); err != nil {
		t.Fatal(err)
	}
	m, err := a.machine("debian")
	if err != nil {
		t.Fatal(err)
	}
	f.requests = nil
	return a, f, m
}

func TestCreateFromALocalImage(t *testing.T) {
	a, f, m := withMachine(t)
	if !m.Prepared || m.BuildID != "nsl-machine-debian-trixie-x86-64-r1" || m.User != "u" || m.UID != a.uid || m.Tier != "shared" {
		t.Fatalf("%+v", m)
	}
	if st, err := os.Stat(filepath.Join(a.machineImages(), m.Image+".tar.zst")); err != nil || st.Mode().Perm() != 0444 {
		t.Fatal(err)
	}
	if current, _ := a.defaultMachine(); current != "debian" {
		t.Fatal("the first machine is not the default:", current)
	}
	if err := a.create(append([]string{"fedora", "--user", "dev"}, machineImage(t, "other rootfs")...)); err != nil {
		t.Fatal(err)
	}
	create := f.requests[len(f.requests)-1]
	if create.Op != "create" || create.Account.User != "dev" || create.Account.UID != a.uid || create.Image.BuildID != "" ||
		create.Image.Path != protocol.ImageShare+"/"+create.Image.Digest[7:]+".tar.zst" || create.Image.Size != int64(len("other rootfs")) {
		t.Fatalf("%+v %+v", create, create.Image)
	}
	if current, _ := a.defaultMachine(); current != "debian" {
		t.Fatal("a later machine took the default")
	}
	if err := a.create(append([]string{"debian"}, machineImage(t, "rootfs")...)); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"Bad"}, {"x", "--distro", "debian:13", "--image", "f", "--digest", "sha256:" + strings.Repeat("0", 64)},
		{"x", "--image", "f"}, {"x", "--user", "Root", "--image", "f", "--digest", "sha256:" + strings.Repeat("0", 64)},
	} {
		if err := a.create(args); err == nil {
			t.Fatal("accepted", args)
		}
	}
}

func TestFailedCreateLeavesNothing(t *testing.T) {
	a, f, _ := startedVM(t)
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "create" {
			return &protocol.Error{Code: protocol.CodeFailed, Message: "invalid root filesystem: path leaves the tree"}
		}
		return nil
	}
	err := a.create(append([]string{"broken"}, machineImage(t, "bad rootfs")...))
	var agentErr *protocol.Error
	if !errors.As(err, &agentErr) || !strings.Contains(err.Error(), "leaves the tree") {
		t.Fatal(err)
	}
	if ops := f.ops(); len(ops) < 2 || ops[len(ops)-2] != "create" || ops[len(ops)-1] != "remove" {
		t.Fatal(ops)
	}
	if _, err := os.Lstat(a.machinePath("broken")); !os.IsNotExist(err) {
		t.Fatal("kept the record of a failed machine")
	}
	if current, _ := a.defaultMachine(); current != "" {
		t.Fatal("a failed machine became the default")
	}
}

func TestStartStopDefaultAndList(t *testing.T) {
	a, f, m := withMachine(t)
	if err := a.execute([]string{"start", "debian"}); err != nil {
		t.Fatal(err)
	}
	start := f.requests[len(f.requests)-1]
	if start.Op != "start" || start.ID != m.ID || start.IdleTimeout == nil || *start.IdleTimeout != 15 {
		t.Fatalf("%+v", start)
	}
	if err := a.execute([]string{"stop", "debian"}); err != nil || f.requests[len(f.requests)-1].Op != "stop" {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"list"}); err != nil || !strings.Contains(out.String(), "debian   stopped  nsl-machine-debian-trixie-x86-64-r1  shared  *") {
		t.Fatal(err, out.String())
	}
	if err := a.execute([]string{"default", "ghost"}); err == nil {
		t.Fatal("made a missing machine the default")
	}
}

func TestRemovePreviewsThenRemoves(t *testing.T) {
	a, f, m := withMachine(t)
	var out bytes.Buffer
	a.out = &out
	if err := a.remove([]string{"debian"}); err != nil || !strings.Contains(out.String(), "--yes") || len(f.requests) != 0 {
		t.Fatal(err, out.String())
	}
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "remove" {
			return &protocol.Error{Code: protocol.CodeBusy, Message: "debian is running; stop it first"}
		}
		return nil
	}
	if err := a.remove([]string{"debian", "--yes"}); err == nil || !strings.Contains(err.Error(), "stop debian first") {
		t.Fatal(err)
	}
	if _, err := a.machine("debian"); err != nil {
		t.Fatal("a refused removal lost the machine:", err)
	}
	// An unreachable VM leaves the removal pending under removing/.
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "remove" {
			return errors.New("connection refused")
		}
		return nil
	}
	if err := a.remove([]string{"debian", "--yes"}); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.removingPath("debian")); err != nil {
		t.Fatal("no tombstone:", err)
	}
	if err := a.create(append([]string{"debian"}, machineImage(t, "rootfs")...)); err == nil {
		t.Fatal("reused a name whose removal is pending")
	}
	f.agent = nil
	if err := a.remove([]string{"debian", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if last := f.requests[len(f.requests)-1]; last.Op != "remove" || last.ID != m.ID {
		t.Fatalf("%+v", last)
	}
	for _, p := range []string{a.machinePath("debian"), a.removingPath("debian"), a.defaultPath()} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatal("left", p)
		}
	}
}

func TestTranslateByDeviceAndInode(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "var/home/u/projects/x"), 0755)
	os.Symlink("var/home", filepath.Join(root, "home"))
	shares := []protocol.Share{{Source: filepath.Join(root, "var/home/u")}}
	for dir, want := range map[string]string{
		filepath.Join(root, "home/u/projects/x"):     "/mnt/host" + root + "/var/home/u/projects/x",
		filepath.Join(root, "var/home/u/projects/x"): "/mnt/host" + root + "/var/home/u/projects/x",
		filepath.Join(root, "var/home/u"):            "/mnt/host" + root + "/var/home/u",
		filepath.Join(root, "var"):                   "",
		"/usr/share":                                 "",
	} {
		got, ok := translate(dir, shares)
		if got != want || ok != (want != "") {
			t.Fatalf("%s: %q %v", dir, got, ok)
		}
	}
}

func TestRunSendsLiteralArgvFromTheTranslatedDirectory(t *testing.T) {
	a, f, m := withMachine(t)
	project := filepath.Join(a.hostRoot, "home", "u", "project")
	os.MkdirAll(project, 0755)
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(project)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("PATH", "/host/only")
	if err := a.execute([]string{"run", "--root", "printf", "%s", "$HOME", "a b"}); err != nil {
		t.Fatal(err)
	}
	run := f.requests[len(f.requests)-1]
	home, _ := filepath.EvalSymlinks(filepath.Join(a.hostRoot, "home", "u"))
	if run.Op != "run" || run.ID != m.ID || !run.Root || run.TTY || strings.Join(run.Argv, "|") != "printf|%s|$HOME|a b" ||
		run.Directory != "/mnt/host"+home+"/project" || run.Env["TERM"] != "xterm-256color" || run.Env["LC_ALL"] != "C.UTF-8" || run.Env["PATH"] != "" {
		t.Fatalf("%+v", run)
	}
	if ops := f.ops(); strings.Join(ops[len(ops)-3:], ",") != "identity,start,run" {
		t.Fatal(ops)
	}
	// Outside the shared trees, run refuses and a shell falls back to the home.
	os.Chdir(t.TempDir())
	before := len(f.requests)
	if err := a.execute([]string{"run", "pwd"}); err == nil || !strings.Contains(err.Error(), "not shared") || len(f.requests) != before {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	a.err = &stderr
	if err := a.execute(nil); err != nil || !strings.Contains(stderr.String(), "starting in the home directory") {
		t.Fatal(err, stderr.String())
	}
	if shell := f.requests[len(f.requests)-1]; shell.Directory != "" || shell.Argv[0] != "/bin/sh" {
		t.Fatalf("%+v", shell)
	}
	if err := a.execute([]string{"run", "--cd", "relative", "pwd"}); err == nil {
		t.Fatal("accepted a relative --cd")
	}
	if err := a.execute([]string{"run", "--cd", "/srv", "pwd"}); err != nil || f.requests[len(f.requests)-1].Directory != "/srv" {
		t.Fatal(err)
	}
	a.setDefault("")
	if err := a.execute(nil); err == nil || !strings.Contains(err.Error(), "no default machine") || !strings.Contains(err.Error(), "debian") {
		t.Fatal(err)
	}
}

func credentialOf(t *testing.T, v *vmRecord) protocol.Credential {
	t.Helper()
	var c protocol.Credential
	b, _ := os.ReadFile(filepath.Join(v.dir, "nsl.vm"))
	if err := protocol.DecodeStrict(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestOnlyCommandsThatEnterMachinesAutostartThem(t *testing.T) {
	a, f := testApp(t)
	image, digest := localImage(t, "vm image")
	if err := a.execute([]string{"update", "--image", image, "--digest", digest}); err != nil {
		t.Fatal(err)
	}
	f.vm, _ = a.loadVM()
	if err := a.create(append([]string{"debian"}, machineImage(t, "rootfs")...)); err != nil {
		t.Fatal(err)
	}
	if c := credentialOf(t, f.vm); c.Autostart {
		t.Fatal("create started every machine")
	}
	a.shutdown()
	if err := a.execute([]string{"start", "debian"}); err != nil {
		t.Fatal(err)
	}
	if c := credentialOf(t, f.vm); !c.Autostart {
		t.Fatal("start did not autostart machines")
	}
}

func TestStartRelaunchesAVMThatPowersOffWhileIdle(t *testing.T) {
	a, f, v := startedVM(t)
	unit := vmUnit(v)
	launches := f.ran("systemd-run", "--user")
	// The running VM powers itself off as the command arrives.
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "identity" && f.ran("systemd-run", "--user") == launches {
			f.states[unit] = "inactive"
			return errors.New("connection closed")
		}
		return nil
	}
	if _, err := a.runningVM(true); err != nil {
		t.Fatal(err)
	}
	if f.ran("systemd-run", "--user") != launches+1 {
		t.Fatal("did not start the VM again")
	}
}
