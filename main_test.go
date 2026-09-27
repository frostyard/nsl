package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type call struct {
	Bin       string
	Args, Env []string
}
type fakeRunner struct {
	calls                []call
	states, descriptions map[string]string
	failConvert          bool
	failCheck            bool
	failResize           bool
	wrongKey             bool
	wrongIdentity        bool
}

func (f *fakeRunner) run(ctx context.Context, in io.Reader, out, stderr io.Writer, env []string, bin string, args ...string) error {
	f.calls = append(f.calls, call{bin, append([]string{}, args...), env})
	if bin == "ssh-keygen" {
		if args[0] == "-y" {
			if f.wrongKey {
				_, err := io.WriteString(out, "ssh-ed25519 BBBB\n")
				return err
			}
			_, err := io.WriteString(out, "ssh-ed25519 AAAA\n")
			return err
		}
		path := args[len(args)-1]
		if err := os.WriteFile(path, []byte("private-key"), 0600); err != nil {
			return err
		}
		return os.WriteFile(path+".pub", []byte("ssh-ed25519 AAAA fixture\n"), 0644)
	}
	if bin == "qemu-img" && args[0] == "check" && f.failCheck {
		return errors.New("injected disk check failure")
	}
	if bin == "qemu-img" && args[0] == "resize" {
		if f.failResize {
			return errors.New("injected resize failure")
		}
		path := args[len(args)-2]
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(b) >= 32 && string(b[:4]) == "QFI\xfb" {
			size, err := strconv.Atoi(strings.TrimSuffix(args[len(args)-1], "G"))
			if err != nil {
				return err
			}
			binary.BigEndian.PutUint64(b[24:], uint64(int64(size)*gib))
			return os.WriteFile(path, b, 0600)
		}
	}
	if bin == "qemu-img" && args[0] == "convert" {
		if f.failConvert {
			return errors.New("injected disk preparation failure")
		}
		if args[2] == "qcow2" {
			b, err := os.ReadFile(args[len(args)-2])
			if err != nil {
				return err
			}
			return os.WriteFile(args[len(args)-1], b, 0600)
		}
		return os.WriteFile(args[len(args)-1], []byte("persistent disk"), 0600)
	}
	if bin == "systemctl" && len(args) > 2 && args[1] == "show" {
		name := args[2]
		state := f.states[name]
		if state == "" {
			io.WriteString(out, "LoadState=not-found\nActiveState=inactive\n")
		} else {
			fmt.Fprintf(out, "LoadState=loaded\nActiveState=%s\nDescription=%s\n", state, f.descriptions[name])
		}
	}
	if bin == "systemd-run" {
		var name, desc string
		for _, s := range args {
			if strings.HasPrefix(s, "--unit=") {
				name = strings.TrimPrefix(s, "--unit=")
			}
			if strings.HasPrefix(s, "--description=") {
				desc = strings.TrimPrefix(s, "--description=")
			}
		}
		f.states[name] = "active"
		f.descriptions[name] = desc
	}
	if bin == "ssh" && len(args) > 3 {
		b, _ := base64.StdEncoding.DecodeString(args[len(args)-1])
		var req guestRequest
		_ = json.Unmarshal(b, &req)
		if len(req.Argv) > 0 {
			config := ""
			for i, s := range args {
				if s == "-F" && i+1 < len(args) {
					config = args[i+1]
				}
			}
			metadata, _ := os.ReadFile(filepath.Join(filepath.Dir(config), "environment.json"))
			var e environment
			_ = json.Unmarshal(metadata, &e)
			if reflect.DeepEqual(req.Argv, []string{"cat", "/var/lib/nsl/identity.json"}) {
				id := guestID(&e)
				if f.wrongIdentity {
					id = strings.Repeat("f", 32)
				}
				return json.NewEncoder(out).Encode(map[string]any{"version": 1, "id": id, "uid": e.Owner, "gid": e.GID})
			}
			if reflect.DeepEqual(req.Argv, []string{"systemctl", "poweroff"}) {
				f.states[unit(&e)] = "inactive"
			}
		}
	}
	if bin == "systemctl" && len(args) > 2 && args[1] == "stop" {
		f.states[args[2]] = "inactive"
	}
	return nil
}
func testApp(t *testing.T) (*app, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{states: map[string]string{}, descriptions: map[string]string{}}
	a := &app{home: t.TempDir(), runtimeDir: t.TempDir(), self: "/test/nsl", waypipe: "waypipe", uid: os.Getuid(), gid: os.Getgid(), r: f, in: strings.NewReader(""), out: io.Discard, err: io.Discard}
	os.Chmod(a.home, 0700)
	os.Chmod(a.runtimeDir, 0700)
	if a.uid == 0 {
		t.Skip("manager requires a normal user")
	}
	return a, f
}
func imageArgs(t *testing.T) []string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "base.raw")
	b := []byte("raw test image")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return []string{"--image", p, "--digest", "sha256:" + hex.EncodeToString(h[:])}
}
func fixture(t *testing.T) (*app, *fakeRunner, *environment) {
	t.Helper()
	a, f := testApp(t)
	if err := a.create("dev", imageArgs(t)); err != nil {
		t.Fatal(err)
	}
	e, err := a.owned("dev")
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	return a, f, e
}
func running(f *fakeRunner, e *environment) {
	for _, name := range []string{unit(e), portUnit(e)} {
		f.states[name] = "active"
		f.descriptions[name] = description(e)
	}
}
func TestNameValidation(t *testing.T) {
	for _, s := range []string{"../other", "-opt", "a/b", "A", "x-", strings.Repeat("a", 25), ""} {
		if checkName(s) == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"dev", "debian-13", "a"} {
		if err := checkName(s); err != nil {
			t.Fatal(err)
		}
	}
}
func TestOwnershipAndDuplicateCreate(t *testing.T) {
	a, f, e := fixture(t)
	if err := a.create("dev", imageArgs(t)); err == nil {
		t.Fatal("overwrote existing state")
	}
	if len(f.calls) != 0 {
		t.Fatal("called backend for duplicate")
	}
	e.Owner++
	if err := a.save(e); err != nil {
		t.Fatal(err)
	}
	if err := a.execute([]string{"stop", "dev"}); err == nil {
		t.Fatal("accepted foreign owner")
	}
	if len(f.calls) != 0 {
		t.Fatal("called backend for foreign owner")
	}
}
func TestRejectSymlinkAndWritableMetadata(t *testing.T) {
	a, _, _ := fixture(t)
	file := filepath.Join(a.dir("dev"), "environment.json")
	os.Chmod(file, 0666)
	if _, err := a.owned("dev"); err == nil {
		t.Fatal("accepted writable metadata")
	}
	os.Rename(file, file+".original")
	os.Symlink(file+".original", file)
	if _, err := a.owned("dev"); err == nil {
		t.Fatal("accepted symlink")
	}
}
func TestForeignUnitRefused(t *testing.T) {
	a, f, e := fixture(t)
	running(f, e)
	f.descriptions[unit(e)] = "someone else's service"
	if err := a.stop(e); err == nil {
		t.Fatal("adopted foreign VM unit")
	}
	for _, c := range f.calls {
		if c.Bin == "systemctl" && len(c.Args) > 2 && c.Args[1] == "stop" && c.Args[2] == unit(e) {
			t.Fatal("stopped foreign unit")
		}
	}
}
func TestIndependentEnvironments(t *testing.T) {
	a, _, e := fixture(t)
	if err := a.create("peer", imageArgs(t)); err != nil {
		t.Fatal(err)
	}
	peer, err := a.owned("peer")
	if err != nil {
		t.Fatal(err)
	}
	if peer.ID == e.ID || unit(peer) == unit(e) || cid(peer) == cid(e) || a.socket(peer) == a.socket(e) {
		t.Fatal("shared runtime identity")
	}
	for _, x := range []*environment{e, peer} {
		if !x.Prepared {
			t.Fatal("not prepared")
		}
		b, _ := os.ReadFile(filepath.Join(a.dir(x.Name), "boot.json"))
		var c map[string]any
		json.Unmarshal(b, &c)
		if c["id"] != x.ID {
			t.Fatal("wrong boot credential")
		}
	}
}
func TestOnlyExplicitShares(t *testing.T) {
	a, _, e := fixture(t)
	for _, arg := range a.launchArgs(e) {
		if strings.HasPrefix(arg, "--bind") {
			t.Fatal("implicit share")
		}
	}
	e.Project = "/project with spaces"
	found := false
	for _, arg := range a.launchArgs(e) {
		if arg == "--bind=/project with spaces:/work" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing explicit share")
	}
}
func TestLaunchRequestsSSHTransportExplicitly(t *testing.T) {
	a, _, e := fixture(t)
	args := a.launchArgs(e)
	joined := strings.Join(args, "\n")
	for _, required := range []string{"systemd.ssh_auto=no", "systemd.ssh_listen=vsock::22"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("boot depends on early automatic vsock detection: %v", args)
		}
	}
}
func TestInvalidRequestDoesNotStartVM(t *testing.T) {
	a, f, e := fixture(t)
	for _, tc := range []struct {
		args []string
		dir  string
	}{{nil, ""}, {[]string{"echo", "bad\x00arg"}, ""}, {[]string{"pwd"}, "relative"}, {[]string{"echo", strings.Repeat("x", 65000)}, ""}} {
		if a.executeGuest(e, tc.args, false, false, false, tc.dir) == nil {
			t.Fatal("accepted invalid request")
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("backend touched")
	}
}
func TestImageDigestValidationBeforeCreation(t *testing.T) {
	a, f := testApp(t)
	for _, args := range [][]string{nil, {"--digest", "sha256:abc"}, {"--image", "https://example.invalid/image"}} {
		if err := a.create("dev", args); err == nil {
			t.Fatal("accepted unpinned image")
		}
	}
	args := imageArgs(t)
	args[3] = "sha256:" + strings.Repeat("0", 64)
	if err := a.create("dev", args); err == nil {
		t.Fatal("accepted hash mismatch")
	}
	if len(f.calls) != 0 {
		t.Fatal("backend called")
	}
	if _, err := os.Stat(a.dir("dev")); !os.IsNotExist(err) {
		t.Fatal("invalid image reserved environment")
	}
}
func TestRecoverInterruptedPreparation(t *testing.T) {
	a, f := testApp(t)
	f.failConvert = true
	if err := a.create("dev", imageArgs(t)); err == nil {
		t.Fatal("expected failure")
	}
	e, err := a.owned("dev")
	if err != nil {
		t.Fatal(err)
	}
	if e.Prepared {
		t.Fatal("marked incomplete disk ready")
	}
	id := e.ID
	key, _ := os.ReadFile(filepath.Join(a.dir(e.Name), "keys/identity"))
	f.failConvert = false
	if err = a.recover(e); err != nil {
		t.Fatal(err)
	}
	e, _ = a.owned("dev")
	after, _ := os.ReadFile(filepath.Join(a.dir(e.Name), "keys/identity"))
	if e.ID != id || !e.Prepared || !bytes.Equal(key, after) {
		t.Fatal("recovery replaced identity")
	}
	disk := filepath.Join(a.dir(e.Name), "disk.qcow2")
	os.WriteFile(disk, []byte("user data"), 0600)
	os.Remove(a.imagePath(e)) // Existing standalone disks do not depend on the cache.
	f.states[unit(e)] = "inactive"
	f.states[portUnit(e)] = "inactive"
	if err = a.recover(e); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(disk)
	if string(b) != "user data" {
		t.Fatal("replaced existing disk")
	}
}
func TestExecUsesEncodedArgvAndPassesStdin(t *testing.T) {
	a, f, e := fixture(t)
	running(f, e)
	args := []string{"printf", "%s", "space and 'quote'", "$(touch /tmp/oops); &", "line\nbreak"}
	if err := a.executeGuest(e, args, false, false, false, "/work"); err != nil {
		t.Fatal(err)
	}
	c := f.calls[len(f.calls)-1]
	if c.Bin != "ssh" || c.Args[len(c.Args)-2] != "/usr/local/libexec/nsl-exec" {
		t.Fatal(c)
	}
	b, err := base64.StdEncoding.DecodeString(c.Args[len(c.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	var r guestRequest
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Argv, args) || r.Directory != "/work" {
		t.Fatal(r)
	}
	for _, arg := range c.Args {
		if strings.Contains(arg, "touch") {
			t.Fatal("raw argument in transport shell")
		}
	}
}
func TestRootExplicitAndGUIOptIn(t *testing.T) {
	a, f, e := fixture(t)
	running(f, e)
	if err := a.executeGuest(e, []string{"id"}, true, false, false, ""); err != nil {
		t.Fatal(err)
	}
	got := f.calls[len(f.calls)-1].Args
	if !reflect.DeepEqual(got[len(got)-5:len(got)-1], []string{"sudo", "-n", "--", "/usr/local/libexec/nsl-exec"}) {
		t.Fatal(got)
	}
	if a.executeGuest(e, []string{"galculator"}, false, false, true, "") == nil {
		t.Fatal("implicit desktop")
	}
}
func TestStartsStoppedAndChecksRunningIdentity(t *testing.T) {
	a, f, e := fixture(t)
	if err := a.executeGuest(e, []string{"true"}, false, false, false, ""); err != nil {
		t.Fatal(err)
	}
	launches := 0
	for _, c := range f.calls {
		if c.Bin == "systemd-run" {
			launches++
		}
	}
	if launches != 2 {
		t.Fatal("expected VM and forwarder", f.calls)
	}
	f.wrongIdentity = true
	if a.ready(e) == nil {
		t.Fatal("trusted wrong running guest")
	}
}
func TestPortFiltering(t *testing.T) {
	got := listenerPorts("LISTEN 0 128 127.0.0.1:8080 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:22 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:5353 0.0.0.0:*\nmalformed\n")
	if !reflect.DeepEqual(got, map[int]bool{8080: true}) {
		t.Fatal(got)
	}
}
func TestGuestHelperBinaryStreamsAndExit(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "exec.py")
	os.WriteFile(helper, []byte(guestHelper), 0700)
	payload, err := encodeRequest([]string{python, "-c", "import sys; sys.stdout.buffer.write(sys.stdin.buffer.read()); sys.stderr.write('error-stream'); sys.exit(37)"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte{0, 1, 2, 255, 10, 13}
	cmd := exec.Command(python, helper, payload)
	cmd.Stdin = bytes.NewReader(input)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err = cmd.Run()
	var ex *exec.ExitError
	if !errors.As(err, &ex) || ex.ExitCode() != 37 {
		t.Fatalf("exit: %v", err)
	}
	if !bytes.Equal(out.Bytes(), input) || stderr.String() != "error-stream" {
		t.Fatalf("streams: %v %q", out.Bytes(), stderr.String())
	}
}
func TestGuestHelperPreservesSpecialArguments(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "exec.py")
	os.WriteFile(helper, []byte(guestHelper), 0700)
	args := []string{"a b", "'quoted'", "$(false);echo bad", "line\nbreak", ""}
	argv := append([]string{python, "-c", "import json,sys; print(json.dumps(sys.argv[1:]))"}, args...)
	payload, _ := encodeRequest(argv, dir)
	out, err := exec.Command(python, helper, payload).Output()
	if err != nil {
		t.Fatal(err)
	}
	var actual []string
	if err = json.Unmarshal(out, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, args) {
		t.Fatal(actual)
	}
}
func TestProjectPath(t *testing.T) {
	if _, err := projectPath("/"); err == nil {
		t.Fatal("accepted host root")
	}
	p := filepath.Join(t.TempDir(), "with spaces")
	os.Mkdir(p, 0700)
	if got, err := projectPath(p); err != nil || got != p {
		t.Fatal(got, err)
	}
}

func TestFirstReadinessFlushesOnlyOnce(t *testing.T) {
	a, f, e := fixture(t)
	if err := a.start(e); err != nil {
		t.Fatal(err)
	}
	saved, err := a.owned(e.Name)
	if err != nil || !saved.Initialized {
		t.Fatal("first readiness not recorded", err)
	}
	if err = a.start(e); err != nil {
		t.Fatal(err)
	}
	flushes := 0
	for _, c := range f.calls {
		if c.Bin == "ssh" && len(c.Args) > 0 {
			b, _ := base64.StdEncoding.DecodeString(c.Args[len(c.Args)-1])
			var r guestRequest
			_ = json.Unmarshal(b, &r)
			if reflect.DeepEqual(r.Argv, []string{"sync"}) {
				flushes++
			}
		}
	}
	if flushes != 1 {
		t.Fatalf("first-use sync count %d", flushes)
	}
}
