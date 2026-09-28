package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/frostyard/nsl/internal/protocol"
	"github.com/klauspost/compress/zstd"
)

const machineID = "4f0c6a1e9b2d4c7f8e3a5b6c7d8e9f01"

type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitError) ExitCode() int { return int(e) }

type fakeRunner struct {
	calls   [][]string
	handler func(argv []string, stdin io.Reader, stdout io.Writer) error
}

func (f *fakeRunner) run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, argv ...string) error {
	f.calls = append(f.calls, argv)
	if f.handler != nil {
		return f.handler(argv, stdin, stdout)
	}
	return nil
}

func (f *fakeRunner) ran(prefix ...string) int {
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && reflect.DeepEqual(c[:len(prefix)], prefix) {
			n++
		}
	}
	return n
}

type fakeManager struct {
	system       string
	sessions     int
	started      []unitSpec
	code, status int32
	discarded    int
}

func (m *fakeManager) SystemState() (string, error) { return m.system, nil }
func (m *fakeManager) Sessions() (int, error)       { return m.sessions, nil }
func (m *fakeManager) Start(unit string, spec unitSpec) error {
	if !strings.HasPrefix(unit, "nsl-run-") || !strings.HasSuffix(unit, ".service") {
		return errors.New("unexpected unit name " + unit)
	}
	m.started = append(m.started, spec)
	return nil
}
func (m *fakeManager) Wait(ctx context.Context, unit string) (int32, int32, error) {
	return m.code, m.status, nil
}
func (m *fakeManager) Discard(unit string) { m.discarded++ }
func (m *fakeManager) Close() error        { return nil }

type fakeSystem struct {
	units   map[string]string
	manager *fakeManager
	killed  []int
	stopped []string
}

func (s *fakeSystem) UnitState(unit string) (string, error) {
	if state, ok := s.units[unit]; ok {
		return state, nil
	}
	return "inactive", nil
}
func (s *fakeSystem) StartUnit(unit string) error { s.units[unit] = "active"; return nil }
func (s *fakeSystem) StopUnit(unit string) error {
	s.stopped = append(s.stopped, unit)
	s.units[unit] = "inactive"
	return nil
}
func (s *fakeSystem) KillMachine(name string, signal int) error {
	s.killed = append(s.killed, signal)
	s.units[unitOf(name)] = "inactive"
	return nil
}
func (s *fakeSystem) TerminateMachine(name string) error { return nil }
func (s *fakeSystem) OpenPTY(name string) (*os.File, *os.File, string, error) {
	return nil, nil, "", errors.New("no PTY in tests")
}
func (s *fakeSystem) Machine(name string) (manager, error) {
	if s.units[unitOf(name)] != "active" {
		return nil, errors.New("not running")
	}
	return s.manager, nil
}

type testAgent struct {
	*agent
	sys             *fakeSystem
	r               *fakeRunner
	stdout, stderr  *os.File
	outPath, errOut string
}

func newTestAgent(t *testing.T, role string) *testAgent {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{stateDir + "/machines", machinesDir, "/usr/lib/nsl", "/run"} {
		if err := os.MkdirAll(root+d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	binding := protocol.Binding{Version: 1, ID: strings.Repeat("ab", 16), Role: role, UID: 1000, GID: 1000}
	if role == "isolated" {
		binding.Machine = &protocol.MachineRef{Name: "iso", ID: machineID}
	}
	b, _ := json.Marshal(binding)
	os.WriteFile(root+stateDir+"/identity.json", b, 0644)
	os.WriteFile(root+"/usr/lib/nsl/image.json", []byte(`{"schema":1,"role":"vm","agent_protocol":1}`+"\n"), 0644)
	stdout, _ := os.Create(filepath.Join(t.TempDir(), "stdout"))
	stderr, _ := os.Create(filepath.Join(t.TempDir(), "stderr"))
	t.Cleanup(func() { stdout.Close(); stderr.Close() })
	sys := &fakeSystem{units: map[string]string{}, manager: &fakeManager{system: "running"}}
	r := &fakeRunner{}
	clock := 0.0
	a := &agent{root: root, sys: sys, r: r, stdin: os.Stdin, stdout: stdout, stderr: stderr,
		now: func() float64 { clock += 0.5; return clock }}
	return &testAgent{agent: a, sys: sys, r: r, stdout: stdout, stderr: stderr, outPath: stdout.Name(), errOut: stderr.Name()}
}

func (ta *testAgent) output() string { b, _ := os.ReadFile(ta.outPath); return string(b) }
func (ta *testAgent) errors() string { b, _ := os.ReadFile(ta.errOut); return string(b) }

func (ta *testAgent) addMachine(t *testing.T, name, id string) {
	t.Helper()
	r := protocol.MachineRecord{Schema: 1, Name: name, ID: id, Account: protocol.Account{User: "u", Group: "u", UID: 1000, GID: 1000}, BuildID: "b", Created: "2026-09-27T00:00:00Z"}
	b, _ := json.Marshal(r)
	if err := os.WriteFile(ta.root+recordsDir+"/"+name+".json", b, 0600); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(ta.root+machinesDir+"/"+name, 0755)
}

func request(t *testing.T, r protocol.Request) string {
	t.Helper()
	r.Protocol = protocol.Version
	s, err := protocol.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func minutes(n int) *int { return &n }

func TestServeRefusesBadRequests(t *testing.T) {
	ta := newTestAgent(t, "shared")
	for _, tc := range []struct{ command, code string }{
		{"", protocol.CodeBadRequest},
		{"!!", protocol.CodeBadRequest},
		{request(t, protocol.Request{Op: "stop", Machine: "ghost", ID: machineID}), protocol.CodeUnknownMachine},
	} {
		if status := ta.serve(tc.command); status != protocol.ErrorExit || !strings.Contains(ta.errors(), "nsl-agent: "+tc.code+": ") {
			t.Fatalf("%q: %d %s", tc.command, status, ta.errors())
		}
	}
	ta.addMachine(t, "debian", machineID)
	if status := ta.serve(request(t, protocol.Request{Op: "stop", Machine: "debian", ID: strings.Repeat("0", 32)})); status != 255 || !strings.Contains(ta.errors(), "machine-id") {
		t.Fatal(status, ta.errors())
	}
}

func TestIdentity(t *testing.T) {
	ta := newTestAgent(t, "shared")
	if status := ta.serve(request(t, protocol.Request{Op: "identity"})); status != 0 {
		t.Fatal(ta.errors())
	}
	var id protocol.Identity
	if err := protocol.DecodeStrict([]byte(ta.output()), &id); err != nil || id.Protocol != 1 || id.VM.Role != "shared" || id.VM.UID != 1000 || string(id.Image) != `{"schema":1,"role":"vm","agent_protocol":1}` {
		t.Fatal(err, ta.output())
	}
	os.Remove(ta.root + stateDir + "/identity.json")
	if status := ta.serve(request(t, protocol.Request{Op: "identity"})); status != 255 || !strings.Contains(ta.errors(), "failed") {
		t.Fatal(status)
	}
}

func TestIsolatedVMRefusesOtherMachines(t *testing.T) {
	ta := newTestAgent(t, "isolated")
	ta.addMachine(t, "other", machineID)
	if status := ta.serve(request(t, protocol.Request{Op: "stop", Machine: "other", ID: machineID})); status != 255 || !strings.Contains(ta.errors(), "refused") {
		t.Fatal(status, ta.errors())
	}
}

func TestStartStopAndList(t *testing.T) {
	ta := newTestAgent(t, "shared")
	ta.addMachine(t, "debian", machineID)
	if status := ta.serve(request(t, protocol.Request{Op: "start", Machine: "debian", ID: machineID, IdleTimeout: minutes(5)})); status != 0 {
		t.Fatal(ta.errors())
	}
	var started protocol.StartResult
	if json.Unmarshal([]byte(ta.output()), &started); started.State != "running" || ta.sys.units[unitOf("debian")] != "active" {
		t.Fatal(ta.output())
	}
	settings, err := os.ReadFile(ta.root + settingsDir + "/debian.nspawn")
	if err != nil || !strings.Contains(string(settings), "PrivateUsers=no") || !strings.Contains(string(settings), "Bind=/mnt/host") {
		t.Fatal(err, string(settings))
	}
	if b, _ := os.ReadFile(ta.root + runtimeDir + "/idle-timeout"); string(b) != "5\n" {
		t.Fatal(string(b))
	}
	ta.stdout.Truncate(0)
	ta.stdout.Seek(0, 0)
	ta.sys.manager.sessions = 2
	if status := ta.serve(request(t, protocol.Request{Op: "machines"})); status != 0 {
		t.Fatal(ta.errors())
	}
	var list []protocol.MachineStatus
	if err = json.Unmarshal([]byte(ta.output()), &list); err != nil || len(list) != 1 || list[0].State != "running" || list[0].Sessions != 2 {
		t.Fatal(err, ta.output())
	}
	ta.sys.units[unitOf("debian")] = "deactivating"
	if status := ta.serve(request(t, protocol.Request{Op: "start", Machine: "debian", ID: machineID, IdleTimeout: minutes(5)})); status != 255 || !strings.Contains(ta.errors(), "busy") {
		t.Fatal(status)
	}
	ta.sys.units[unitOf("debian")] = "active"
	if status := ta.serve(request(t, protocol.Request{Op: "stop", Machine: "debian", ID: machineID})); status != 0 || !reflect.DeepEqual(ta.sys.killed, []int{38}) {
		t.Fatal(status, ta.sys.killed)
	}
}

func TestIsolatedSettingsHaveNoHostFiles(t *testing.T) {
	if strings.Contains(nspawnSettings("isolated"), "/mnt/host") || !strings.Contains(nspawnSettings("shared"), "Bind=/mnt/host\n") {
		t.Fatal("wrong settings")
	}
}

func TestRun(t *testing.T) {
	ta := newTestAgent(t, "shared")
	ta.addMachine(t, "debian", machineID)
	run := protocol.Request{Op: "run", Machine: "debian", ID: machineID, Argv: []string{"printf", "$HOME", "%h"},
		Env: map[string]string{"TERM": "xterm", "LANG": "C.UTF-8"}, IdleTimeout: minutes(15)}
	if status := ta.serve(request(t, run)); status != 255 || !strings.Contains(ta.errors(), "not-running") || len(ta.sys.manager.started) != 0 {
		t.Fatal(status, ta.errors())
	}
	ta.sys.units[unitOf("debian")] = "active"
	ta.sys.manager.code, ta.sys.manager.status = 2, 15
	if status := ta.serve(request(t, run)); status != 143 {
		t.Fatal(status, ta.errors())
	}
	spec := ta.sys.manager.started[0]
	if !reflect.DeepEqual(spec.Argv, []string{"printf", "$HOME", "%h"}) || spec.User != "u" || !spec.PAM || spec.Directory != "/home/u" ||
		!reflect.DeepEqual(spec.Env, []string{"LANG=C.UTF-8", "TERM=xterm"}) || spec.SearchPath[0] != "/home/u/.local/bin" || spec.TTY != "" {
		t.Fatalf("%+v", spec)
	}
	if ta.sys.manager.discarded == 0 {
		t.Fatal("unit not collected")
	}
	run.Root, run.Directory = true, "/srv"
	ta.sys.manager.code, ta.sys.manager.status = 1, 203
	if status := ta.serve(request(t, run)); status != 203 || !strings.Contains(ta.errors(), "printf could not start (systemd 203/EXEC)") {
		t.Fatal(status, ta.errors())
	}
	if spec = ta.sys.manager.started[1]; spec.User != "root" || spec.PAM || spec.Directory != "/srv" || spec.SearchPath[0] != "/root/.local/bin" {
		t.Fatalf("%+v", spec)
	}
}

func TestRemove(t *testing.T) {
	ta := newTestAgent(t, "shared")
	ta.addMachine(t, "debian", machineID)
	ta.writeSettings("debian", "shared")
	ta.sys.units[unitOf("debian")] = "active"
	remove := request(t, protocol.Request{Op: "remove", Machine: "debian", ID: machineID})
	if status := ta.serve(remove); status != 255 || !strings.Contains(ta.errors(), "busy") {
		t.Fatal(status)
	}
	ta.sys.units[unitOf("debian")] = "inactive"
	ta.r.handler = func(argv []string, stdin io.Reader, stdout io.Writer) error {
		if _, err := os.Stat(ta.root + recordsDir + "/debian.json"); err != nil {
			t.Fatal("record removed before the subvolume")
		}
		return os.RemoveAll(argv[len(argv)-1])
	}
	if status := ta.serve(remove); status != 0 || ta.r.ran("btrfs", "subvolume", "delete", "--recursive", ta.root+machinesDir+"/debian") != 1 {
		t.Fatal(status, ta.errors(), ta.r.calls)
	}
	for _, p := range []string{recordsDir + "/debian.json", settingsDir + "/debian.nspawn"} {
		if _, err := os.Stat(ta.root + p); !errors.Is(err, os.ErrNotExist) {
			t.Fatal(p, err)
		}
	}
}

type entry struct {
	name, link string
	kind       byte
	body       string
	mode       int64
}

func rootfs(t *testing.T, entries []entry, trailing []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	w := tar.NewWriter(&raw)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0644
			if e.kind == tar.TypeDir {
				mode = 0755
			}
		}
		h := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.kind, Mode: mode, Size: int64(len(e.body)), Uid: 0, Gid: 0, Format: tar.FormatPAX}
		if e.kind == tar.TypeChar {
			h.Devmajor, h.Devminor = 1, 3
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, e.body)
	}
	w.Close()
	raw.Write(trailing)
	var out bytes.Buffer
	enc, _ := zstd.NewWriter(&out)
	enc.Write(raw.Bytes())
	enc.Close()
	return out.Bytes()
}

func goodRootfs() []entry {
	return []entry{
		{name: "./", kind: tar.TypeDir},
		{name: "./etc/", kind: tar.TypeDir},
		{name: "./etc/hosts", kind: tar.TypeReg, body: "127.0.0.1\tlocalhost"},
		{name: "./etc/resolv.conf", kind: tar.TypeSymlink, link: "/run/systemd/resolve/stub-resolv.conf"},
		{name: "./usr/", kind: tar.TypeDir},
		{name: "./usr/lib/", kind: tar.TypeDir},
		{name: "./usr/lib/nsl/", kind: tar.TypeDir},
		{name: "./usr/lib/nsl/machine.json", kind: tar.TypeReg, body: `{"schema":1,"role":"machine","build_id":"nsl-machine-debian-trixie-x86-64-r1","architecture":"x86-64","machine_protocol":1}`},
		{name: "./usr/bin/", kind: tar.TypeDir},
		{name: "./usr/bin/sh", kind: tar.TypeReg, body: "#!", mode: 0755},
		{name: "./usr/bin/dash", kind: tar.TypeLink, link: "./usr/bin/sh"},
		{name: "./run/initctl", kind: tar.TypeFifo},
	}
}

func TestValidateRootfs(t *testing.T) {
	dir := t.TempDir()
	check := func(name string, data []byte, limit int64) error {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0600)
		return validateRootfs(p, limit)
	}
	if err := check("good", rootfs(t, goodRootfs(), nil), 1<<20); err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string]entry{
		"absolute":        {name: "/etc/passwd", kind: tar.TypeReg},
		"dotdot":          {name: "./../outside", kind: tar.TypeReg},
		"nested dotdot":   {name: "usr/../../outside", kind: tar.TypeReg},
		"duplicate":       {name: "etc/hosts", kind: tar.TypeReg},
		"whiteout":        {name: "./etc/.wh.hosts", kind: tar.TypeReg},
		"below symlink":   {name: "./etc/resolv.conf/x", kind: tar.TypeReg},
		"dangling hard":   {name: "./usr/bin/ls", kind: tar.TypeLink, link: "./usr/bin/missing"},
		"escaping hard":   {name: "./usr/bin/ls", kind: tar.TypeLink, link: "../../etc/shadow"},
		"hard to dir":     {name: "./usr/bin/ls", kind: tar.TypeLink, link: "./etc"},
		"device":          {name: "./dev/null", kind: tar.TypeChar},
		"root as file":    {name: ".", kind: tar.TypeReg},
		"empty symlink":   {name: "./etc/x", kind: tar.TypeSymlink},
		"symlink as root": {name: ".", kind: tar.TypeSymlink, link: "/"},
	} {
		err := check(name, rootfs(t, append(goodRootfs(), extra), nil), 1<<20)
		var e *protocol.Error
		if !errors.As(err, &e) || !strings.HasPrefix(e.Message, "invalid root filesystem") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := check("trailing", rootfs(t, goodRootfs(), []byte("\x00\x00junk")), 1<<20); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatal(err)
	}
	if err := check("padding", rootfs(t, goodRootfs(), make([]byte, 10240)), 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := check("large", rootfs(t, goodRootfs(), nil), 2048); err == nil {
		t.Fatal("accepted an archive over the limit")
	}
	if err := check("garbage", []byte("not zstd"), 1<<20); err == nil {
		t.Fatal("accepted garbage")
	}
}

func TestTreeWritesStayInside(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	os.Symlink(outside, filepath.Join(root, "etc"))
	os.MkdirAll(filepath.Join(root, outside), 0755)
	tr, err := openTree(root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	if err = tr.writeFile("etc/hostname", []byte("m\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = tr.symlink("../usr/share/zoneinfo/UTC", "etc/localtime"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("wrote outside the tree:", entries)
	}
	if b, err := os.ReadFile(filepath.Join(root, outside, "hostname")); err != nil || string(b) != "m\n" {
		t.Fatal(err, string(b))
	}
	os.Symlink("../../../..", filepath.Join(root, "up"))
	if err = tr.writeFile("up/escape", nil, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "escape")); err != nil {
		t.Fatal("dotdot did not stop at the tree root:", err)
	}
}

func TestHasHost(t *testing.T) {
	if !hasHost("127.0.0.1 localhost\n127.0.1.1\tdebian # me\n", "debian") || hasHost("127.0.0.1 localhost\n# 127.0.1.1 debian\n", "debian") || hasHost("debian\n", "debian") {
		t.Fatal("hasHost")
	}
}

// createAgent fakes btrfs and nspawn and extracts with the real tar.
func createAgent(t *testing.T, role string, image []byte) (*testAgent, protocol.Request) {
	ta := newTestAgent(t, role)
	os.MkdirAll(ta.root+protocol.ImageShare, 0755)
	sum := sha256.Sum256(image)
	digest := hex.EncodeToString(sum[:])
	os.WriteFile(ta.root+protocol.ImageShare+"/"+digest+".tar.zst", image, 0644)
	ta.r.handler = func(argv []string, stdin io.Reader, stdout io.Writer) error {
		switch {
		case argv[0] == "btrfs" && argv[2] == "create":
			return os.Mkdir(argv[3], 0755)
		case argv[0] == "btrfs" && argv[2] == "delete":
			return os.RemoveAll(argv[4])
		case argv[0] == "tar":
			// Unprivileged tests cannot keep owners, xattrs or ACLs.
			var args []string
			for _, a := range argv[1:] {
				if a != "--numeric-owner" && a != "--same-owner" && a != "--acls" && a != "--xattrs" && a != "--xattrs-include=*" {
					args = append(args, a)
				}
			}
			out, err := exec.Command("tar", args...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%v: %s", err, out)
			}
			return nil
		case argv[0] == "systemd-nspawn":
			command := argv[len(argv)-4:]
			for i, a := range argv {
				if a == "--" {
					command = argv[i+1:]
				}
			}
			if command[0] == "getent" {
				return exitError(2)
			}
			return nil
		}
		return nil
	}
	req := protocol.Request{Op: "create", Machine: "debian", ID: machineID, TimeZone: "Europe/Berlin",
		Account: &protocol.Account{User: "bjk", Group: "bjk", UID: 1000, GID: 1000},
		Image:   &protocol.Image{Path: protocol.ImageShare + "/" + digest + ".tar.zst", Digest: "sha256:" + digest, Size: int64(len(image)), BuildID: ""}}
	return ta, req
}

func leftovers(t *testing.T, ta *testAgent) []string {
	t.Helper()
	var names []string
	for _, dir := range []string{machinesDir, recordsDir} {
		entries, _ := os.ReadDir(ta.root + dir)
		for _, e := range entries {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestCreate(t *testing.T) {
	ta, req := createAgent(t, "shared", rootfs(t, goodRootfs(), nil))
	if status := ta.serve(request(t, req)); status != 0 {
		t.Fatal(ta.errors())
	}
	root := ta.root + machinesDir + "/debian"
	if link, err := os.Readlink(root + "/etc/localtime"); err != nil || link != "../usr/share/zoneinfo/Europe/Berlin" {
		t.Fatal(link, err)
	}
	if b, _ := os.ReadFile(root + "/etc/hostname"); string(b) != "debian\n" {
		t.Fatal(string(b))
	}
	if b, _ := os.ReadFile(root + "/etc/hosts"); string(b) != "127.0.0.1\tlocalhost\n127.0.1.1\tdebian\n" {
		t.Fatalf("%q", b)
	}
	if st, err := os.Stat(root + "/etc/sudoers.d/nsl"); err != nil || st.Mode().Perm() != 0440 {
		t.Fatal(err)
	}
	if link, _ := os.Readlink(root + "/etc/resolv.conf"); link != "/run/systemd/resolve/stub-resolv.conf" {
		t.Fatal("image symlink changed:", link)
	}
	r, err := ta.loadRecord("debian")
	if err != nil || r.ID != machineID || r.Account.User != "bjk" || r.BuildID != "nsl-machine-debian-trixie-x86-64-r1" {
		t.Fatal(err, r)
	}
	if !strings.Contains(ta.output(), `"build_id":"nsl-machine-debian-trixie-x86-64-r1"`) {
		t.Fatal(ta.output())
	}
	if ta.r.ran("systemd-nspawn") != 4 {
		t.Fatal(ta.r.calls)
	}
	for _, c := range ta.r.calls {
		if c[0] == "systemd-nspawn" && (!strings.Contains(strings.Join(c, " "), "--timezone=off --resolv-conf=off") || c[len(c)-1] == "root") {
			t.Fatal(c)
		}
	}
	if names := leftovers(t, ta); !reflect.DeepEqual(names, []string{"debian", "debian.json"}) {
		t.Fatal(names)
	}
	if status := ta.serve(request(t, req)); status != 255 || !strings.Contains(ta.errors(), "already exists") {
		t.Fatal("recreated an existing machine")
	}
}

func TestCreateFailuresLeaveNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		entries []entry
		change  func(*testAgent, *protocol.Request)
		message string
	}{
		"digest":      {goodRootfs(), func(ta *testAgent, r *protocol.Request) { os.WriteFile(ta.root+r.Image.Path, []byte("tampered"), 0644) }, "does not match its verified digest"},
		"size":        {goodRootfs(), func(ta *testAgent, r *protocol.Request) { r.Image.Size-- }, "does not match"},
		"traversal":   {append(goodRootfs(), entry{name: "../x", kind: tar.TypeReg}), nil, "leaves the tree"},
		"descriptor":  {goodRootfs()[:7], nil, "not an nsl machine image"},
		"other build": {goodRootfs(), func(ta *testAgent, r *protocol.Request) { r.Image.BuildID = "nsl-machine-fedora-44-x86-64-r1" }, "not the selected"},
		"account": {goodRootfs(), func(ta *testAgent, r *protocol.Request) {
			handler := ta.r.handler
			ta.r.handler = func(argv []string, stdin io.Reader, stdout io.Writer) error {
				if argv[0] == "systemd-nspawn" && strings.Contains(strings.Join(argv, " "), "getent passwd") {
					io.WriteString(stdout, "bjk:x:1000:1000::/home/bjk:/bin/sh\n")
					return nil
				}
				return handler(argv, stdin, stdout)
			}
		}, "already has an account"},
	} {
		ta, req := createAgent(t, "shared", rootfs(t, tc.entries, nil))
		if tc.change != nil {
			tc.change(ta, &req)
		}
		if status := ta.serve(request(t, req)); status != 255 || !strings.Contains(ta.errors(), tc.message) {
			t.Fatalf("%s: %d %s", name, status, ta.errors())
		}
		if names := leftovers(t, ta); len(names) != 0 {
			t.Fatalf("%s: left %v", name, names)
		}
	}
}

func TestCreateInIsolatedVM(t *testing.T) {
	ta, req := createAgent(t, "isolated", rootfs(t, goodRootfs(), nil))
	req.Machine = "iso"
	req.ID = strings.Repeat("0", 32)
	if status := ta.serve(request(t, req)); status != 255 || !strings.Contains(ta.errors(), "refused") {
		t.Fatal(status, ta.errors())
	}
	req.ID = machineID
	if status := ta.serve(request(t, req)); status != 0 {
		t.Fatal(ta.errors())
	}
	if b, _ := os.ReadFile(ta.root + settingsDir + "/iso.nspawn"); strings.Contains(string(b), "/mnt/host") {
		t.Fatal("isolated machine binds host files")
	}
}
