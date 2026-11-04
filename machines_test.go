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

func TestListJSON(t *testing.T) {
	fresh, _ := testApp(t)
	var out bytes.Buffer
	fresh.out = &out
	if err := fresh.execute([]string{"list", "--json"}); err != nil || out.String() != "{\n  \"vms\": [],\n  \"machines\": []\n}\n" {
		t.Fatalf("%v %q", err, out.String())
	}

	a, _, _ := withMachine(t)
	v, _ := a.loadVM()
	// An unfinished removal lists without an image, tier or default.
	os.MkdirAll(filepath.Dir(a.removingPath("old")), 0700)
	os.WriteFile(a.removingPath("old"), []byte("{}"), 0600)
	a.out = &out
	out.Reset()
	if err := a.execute([]string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}
	var list vmsAndMachines
	jsonDocument(t, out.Bytes(), &list)
	if len(list.VMs) != 1 {
		t.Fatalf("%+v", list.VMs)
	}
	got := list.VMs[0]
	if got.Name != "shared" || got.Role != "shared" || got.State != "running" || got.Image != v.ImageBuild ||
		got.CPUs != v.CPUs || got.MemoryGiB != v.Memory || got.DataDiskGiB != v.DataGiB || got.Pending == nil || len(got.Pending) != 0 {
		t.Fatalf("%+v", got)
	}
	if len(list.Machines) != 2 {
		t.Fatalf("%+v", list.Machines)
	}
	if m := list.Machines[0]; m.Name != "debian" || m.State != "stopped" || m.Image != "nsl-machine-debian-trixie-x86-64-r1" || m.Tier != "shared" || !m.Default {
		t.Fatalf("%+v", m)
	}
	if m := list.Machines[1]; m.Name != "old" || m.State != "removing" || m.Image != "" || m.Tier != "" || m.Default {
		t.Fatalf("%+v", m)
	}
	// The table reports the same machines.
	out.Reset()
	if err := a.execute([]string{"list"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"debian   stopped   nsl-machine-debian-trixie-x86-64-r1  shared  *", "old      removing"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in\n%s", want, out.String())
		}
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

func TestTranslateHostFiles(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	shares := []protocol.Share{{Source: home}}
	argv := []string{"tool", filepath.Join(project, "a b.pdf"), "relative.txt", "file://" + filepath.Join(project, "My%20File.pdf") + "?x=1#page=2", "https://example.test/a", "tail"}
	got, err := translateHostFiles(argv, "U", 1, 1, project, shares, false)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := "/mnt/host" + project
	want := []string{"tool", wantPrefix + "/a b.pdf", wantPrefix + "/relative.txt", "file://" + wantPrefix + "/My%20File.pdf?x=1#page=2", "https://example.test/a", "tail"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv %d: got %q, want %q in %v", i, got[i], want[i], got)
		}
	}

	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	outsideLink := filepath.Join(home, "outside-link.pdf")
	if err := os.Symlink(outside, outsideLink); err != nil {
		t.Fatal(err)
	}
	t.Run("a symlink outside the shared home is refused", func(t *testing.T) {
		if _, err := translateHostFiles([]string{"tool", outsideLink}, "f", 1, 0, home, shares, false); err == nil {
			t.Fatal("accepted a host symlink that resolves outside the shared tree")
		}
	})

	target := filepath.Join(home, "target.pdf")
	if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	insideLink := filepath.Join(home, "inside-link.pdf")
	if err := os.Symlink(target, insideLink); err != nil {
		t.Fatal(err)
	}
	t.Run("a symlink inside the shared home uses the target path", func(t *testing.T) {
		got, err := translateHostFiles([]string{"tool", insideLink}, "f", 1, 0, home, shares, false)
		if err != nil {
			t.Fatal(err)
		}
		want := "/mnt/host" + target
		if len(got) != 2 || got[1] != want {
			t.Fatalf("got %v, want tool %q", got, want)
		}
	})

	t.Run("a relative path with no cwd is refused", func(t *testing.T) {
		if _, err := translateHostFiles([]string{"tool", "relative.txt"}, "f", 1, 0, "", shares, false); err == nil {
			t.Fatal("accepted a relative host path without a working directory")
		}
	})

	t.Run("a missing file behind a symlink outside the shared home is refused", func(t *testing.T) {
		outsideDir := t.TempDir()
		link := filepath.Join(home, "outside-dir-link")
		if err := os.Symlink(outsideDir, link); err != nil {
			t.Fatal(err)
		}
		if _, err := translateHostFiles([]string{"tool", "outside-dir-link/missing.pdf"}, "f", 1, 0, home, shares, false); err == nil {
			t.Fatal("accepted a missing file behind an outside symlink")
		}
	})

	t.Run("a dangling symlink in the shared home is refused", func(t *testing.T) {
		link := filepath.Join(home, "dangling-link")
		if err := os.Symlink("/nonexistent/target", link); err != nil {
			t.Fatal(err)
		}
		if _, err := translateHostFiles([]string{"tool", "dangling-link"}, "f", 1, 0, home, shares, false); err == nil {
			t.Fatal("accepted a dangling symlink")
		}
	})

	t.Run("a missing file in an existing home directory is translated", func(t *testing.T) {
		dir := filepath.Join(home, "existing")
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		got, err := translateHostFiles([]string{"tool", "existing/missing.pdf"}, "f", 1, 0, home, shares, false)
		if err != nil {
			t.Fatal(err)
		}
		want := "/mnt/host" + filepath.Join(dir, "missing.pdf")
		if len(got) != 2 || got[1] != want {
			t.Fatalf("got %v, want tool %q", got, want)
		}
	})

	t.Run("a missing file behind an inside symlink uses the target directory", func(t *testing.T) {
		targetDir := filepath.Join(home, "target-directory")
		if err := os.Mkdir(targetDir, 0755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(home, "inside-directory-link")
		if err := os.Symlink(targetDir, link); err != nil {
			t.Fatal(err)
		}
		got, err := translateHostFiles([]string{"tool", "inside-directory-link/missing.pdf"}, "f", 1, 0, home, shares, false)
		if err != nil {
			t.Fatal(err)
		}
		want := "/mnt/host" + filepath.Join(targetDir, "missing.pdf")
		if len(got) != 2 || got[1] != want {
			t.Fatalf("got %v, want tool %q", got, want)
		}
	})

	t.Run("relative link dotdot follows symlinks instead of cleaning", func(t *testing.T) {
		outsideBase := t.TempDir()
		outsideSubdir := filepath.Join(outsideBase, "subdir")
		if err := os.Mkdir(outsideSubdir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outsideBase, "document"), []byte("outside"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "document"), []byte("inside"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideSubdir, filepath.Join(home, "link")); err != nil {
			t.Fatal(err)
		}
		if _, err := translateHostFiles([]string{"tool", "link/../document"}, "f", 1, 0, home, shares, false); err == nil {
			t.Fatal("accepted link/../document after it resolved outside the shared tree")
		}
	})

	t.Run("a missing file whose parent does not exist is refused", func(t *testing.T) {
		if _, err := translateHostFiles([]string{"tool", "missing-parent/missing.pdf"}, "f", 1, 0, home, shares, false); err == nil {
			t.Fatal("accepted a file below a missing parent")
		}
	})

	for _, tc := range []struct {
		name          string
		argv          []string
		code          string
		before, after int
		isolated      bool
	}{
		{"bad outside", []string{"tool", "/outside/file"}, "f", 1, 0, false},
		{"bad isolated", []string{"tool", filepath.Join(project, "a")}, "f", 1, 0, true},
		{"bad two f", []string{"tool", filepath.Join(project, "a"), filepath.Join(project, "b")}, "f", 1, 0, false},
		{"bad two u", []string{"tool", "/tmp/a", "/tmp/b"}, "u", 1, 0, false},
		{"bad other file host", []string{"tool", "file://otherhost/x"}, "u", 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := translateHostFiles(tc.argv, tc.code, tc.before, tc.after, project, shares, tc.isolated); err == nil {
				t.Fatalf("accepted %+v", tc)
			}
		})
	}
}

func TestRunTranslatesFHostFile(t *testing.T) {
	a, f, m := withMachine(t)
	file := filepath.Join(a.hostRoot, "home", "u", "document.pdf")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.execute([]string{"run", "--cd", "/srv", "--host-files", "f:1:0", "--", "evince", file}); err != nil {
		t.Fatal(err)
	}
	home, _ := filepath.EvalSymlinks(filepath.Join(a.hostRoot, "home", "u"))
	wantFile := "/mnt/host" + home + "/document.pdf"
	run := f.requests[len(f.requests)-1]
	if run.Op != "run" || run.ID != m.ID || len(run.Argv) != 2 || run.Argv[0] != "evince" || run.Argv[1] != wantFile {
		t.Fatalf("%+v", run)
	}

	outside := filepath.Join(t.TempDir(), "outside.pdf")
	before := len(f.requests)
	if err := a.execute([]string{"run", "--cd", "/srv", "--host-files", "f:1:0", "--", "evince", outside}); err == nil {
		t.Fatal("accepted a file outside the shared trees")
	}
	if len(f.requests) != before {
		t.Fatalf("outside file sent requests: before %d after %d", before, len(f.requests))
	}
}

func TestRunTranslatesUHostFiles(t *testing.T) {
	a, f, m := withMachine(t)
	path := filepath.Join(a.hostRoot, "home", "u", "document with spaces.pdf")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	home, _ := filepath.EvalSymlinks(filepath.Join(a.hostRoot, "home", "u"))
	wantPath := "/mnt/host" + home + "/document with spaces.pdf"
	if err := a.execute([]string{"run", "--cd", "/srv", "--host-files", "U:1:1", "--", "viewer", path, "https://example.test/x", "--tail"}); err != nil {
		t.Fatal(err)
	}
	run := f.requests[len(f.requests)-1]
	want := []string{"viewer", wantPath, "https://example.test/x", "--tail"}
	if run.Op != "run" || run.ID != m.ID || len(run.Argv) != len(want) {
		t.Fatalf("%+v", run)
	}
	for i := range want {
		if run.Argv[i] != want[i] {
			t.Fatalf("argv %d: got %q, want %q in %v", i, run.Argv[i], want[i], run.Argv)
		}
	}
}

func TestRunHostFilesRefusesBadFlagsAndIsolatedFiles(t *testing.T) {
	a, f, m := withMachine(t)
	for _, value := range []string{"x:1:0", "f:0:0", "f:1234567:0", "f:1:-1", "f:1", "f:xx:0"} {
		before := len(f.requests)
		if err := a.execute([]string{"run", "--host-files", value, "--", "tool", "file"}); err == nil {
			t.Fatalf("accepted bad --host-files %q", value)
		}
		if len(f.requests) != before {
			t.Fatalf("bad flag sent requests: %q", value)
		}
	}
	if err := a.execute([]string{"run", "--host-files", "f:1:0", "--", "tool", "one", "two"}); err == nil {
		t.Fatal("accepted two files for f")
	}
	m.Tier = "isolated"
	if err := a.saveMachine(m); err != nil {
		t.Fatal(err)
	}
	if err := a.execute([]string{"run", "--host-files", "f:1:0", "--", "tool", filepath.Join(a.hostRoot, "home", "u", "file")}); err == nil {
		t.Fatal("isolated machine accepted a file")
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
