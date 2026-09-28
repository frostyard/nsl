package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

func TestVMStartRunsTheForwarder(t *testing.T) {
	_, f, v := startedVM(t)
	unit := portsUnit(v)
	var launch []string
	for _, c := range f.calls {
		if c.Bin == "systemd-run" && strings.Contains(strings.Join(c.Args, " "), "--unit="+unit) {
			launch = c.Args
		}
	}
	args := strings.Join(launch, " ")
	for _, want := range []string{"--description=nsl ports " + v.ID, "--property=BindsTo=" + vmUnit(v), "--property=Restart=on-failure", "-- /test/nsl _forward"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %s in %s", want, args)
		}
	}
	if f.states[unit] != "active" {
		t.Fatal("forwarder not started")
	}
}

func TestForwarderFollowsListeners(t *testing.T) {
	a, f, v := startedVM(t)
	listeners := []protocol.Listener{{Port: 3000, Address: "0.0.0.0", Machine: "fedora"}, {Port: 5173, Address: "::1", Machine: "debian"}, {Port: 8000, Address: "127.0.0.1", Machine: "debian"}, {Port: 8000, Address: "::1", Machine: "debian"}}
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "listeners" {
			return json.NewEncoder(stdout).Encode(listeners)
		}
		return nil
	}
	f.forwardFail = map[string]bool{"127.0.0.1:3000:127.0.0.1:3000": true}
	fw := &forwarder{a: a, v: v, forwarded: map[int]string{}}
	report := fw.step()
	if report.Error != "" || len(report.Ports) != 3 {
		t.Fatalf("%+v", report)
	}
	want := []portStatus{{3000, "fedora", "127.0.0.1", "conflict", ""}, {5173, "debian", "[::1]", "forwarded", ""}, {8000, "debian", "127.0.0.1", "forwarded", ""}}
	for i, p := range report.Ports {
		p.Error = ""
		if p != want[i] {
			t.Fatalf("%d: %+v", i, report.Ports[i])
		}
	}
	if !strings.Contains(report.Ports[0].Error, "in use") {
		t.Fatal(report.Ports[0].Error)
	}
	// A conflict retries; a vanished listener's forward is cancelled.
	f.forwards = nil
	listeners = listeners[:2]
	delete(f.forwardFail, "127.0.0.1:3000:127.0.0.1:3000")
	report = fw.step()
	if strings.Join(f.forwards, ",") != "cancel 127.0.0.1:8000:127.0.0.1:8000,forward 127.0.0.1:3000:127.0.0.1:3000" || report.Ports[0].State != "forwarded" {
		t.Fatalf("%v %+v", f.forwards, report)
	}
	b, _ := json.Marshal(report)
	os.WriteFile(filepath.Join(v.dir, "ports.json"), b, 0600)
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"ports"}); err != nil {
		t.Fatal(err)
	}
	listing := strings.Join(strings.Fields(out.String()), " ")
	if !strings.Contains(listing, "127.0.0.1:3000 fedora forwarded 127.0.0.1:5173 debian forwarded") {
		t.Fatal(out.String())
	}
}

func TestPortsForOneMachine(t *testing.T) {
	a, _, _ := withMachine(t)
	v, _ := a.loadVM()
	b, _ := json.Marshal(portReport{Updated: time.Now(), Ports: []portStatus{{3000, "fedora", "127.0.0.1", "forwarded", ""}, {8000, "debian", "127.0.0.1", "conflict", "in use"}}})
	os.WriteFile(filepath.Join(v.dir, "ports.json"), b, 0600)
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"ports", "debian"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "3000") || !strings.Contains(out.String(), "conflict: in use") {
		t.Fatal(out.String())
	}
	a.shutdown()
	out.Reset()
	if err := a.execute([]string{"ports"}); err != nil || !strings.Contains(out.String(), "stopped") {
		t.Fatal(err, out.String())
	}
}

func TestBrokerTargets(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "var/home/u")
	os.MkdirAll(filepath.Join(home, "project"), 0755)
	os.WriteFile(filepath.Join(home, "project/index.html"), nil, 0644)
	os.WriteFile(filepath.Join(root, "secret"), nil, 0600)
	os.Symlink(filepath.Join(root, "secret"), filepath.Join(home, "escape"))
	os.Symlink("var/home", filepath.Join(root, "home"))
	shares := []protocol.Share{{Source: home}}
	for target, want := range map[string]string{
		"https://example.com/a?b=c#d":                         "https://example.com/a?b=c#d",
		"http://localhost:8000/":                              "http://localhost:8000/",
		"HTTPS://example.com":                                 "HTTPS://example.com",
		"/mnt/host" + home + "/project/index.html":            home + "/project/index.html",
		"/mnt/host" + root + "/home/u/project":                home + "/project",
		"/mnt/host" + home + "/project/../project/index.html": home + "/project/index.html",
	} {
		if got, err := brokerTarget(target, shares); err != nil || got != want {
			t.Fatalf("%s: %q %v", target, got, err)
		}
	}
	for _, target := range []string{
		"ftp://example.com", "file:///etc/passwd", "javascript:alert(1)", "mailto:x@y", "https://", "https:opaque",
		"/etc/passwd", "/mnt/host", "/mnt/host/", "/mnt/host" + root + "/secret", "/mnt/host" + home + "/escape",
		"/mnt/host" + home + "/../../../secret", "/mnt/host" + home + "/missing", "https://example.com/\nX", "relative/path", "-x",
	} {
		if got, err := brokerTarget(target, shares); err == nil {
			t.Fatalf("accepted %q as %q", target, got)
		}
	}
}

// call sends one Varlink call to the broker and returns its reply.
func brokerCall(t *testing.T, a *app, message string) map[string]any {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() { a.broker(server, "debian", []protocol.Share{{Source: t.TempDir()}}); close(done) }()
	client.Write(append([]byte(message), 0))
	reply, err := bufio.NewReader(client).ReadBytes(0)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	var v map[string]any
	if err = json.Unmarshal(bytes.TrimSuffix(reply, []byte{0}), &v); err != nil {
		t.Fatal(err, string(reply))
	}
	return v
}

func TestBrokerSpeaksVarlink(t *testing.T) {
	a, f := testApp(t)
	a.opener = "opener"
	var stderr bytes.Buffer
	a.err = &stderr
	reply := brokerCall(t, a, `{"method":"io.frostyard.nsl.Broker.Open","parameters":{"target":"https://example.com"}}`)
	f.mu.Lock()
	opened := f.ran("opener", "https://example.com")
	f.mu.Unlock()
	if reply["error"] != nil || opened != 1 || !strings.Contains(stderr.String(), "nsl-open from debian: https://example.com") {
		t.Fatal(reply, opened, stderr.String())
	}
	for message, errorName := range map[string]string{
		`{"method":"io.frostyard.nsl.Broker.Open","parameters":{"target":"file:///etc/shadow"}}`:      "io.frostyard.nsl.Broker.Refused",
		`{"method":"io.frostyard.nsl.Broker.Open","parameters":{"target":"https://x","extra":1}}`:     "io.frostyard.nsl.Broker.Refused",
		`{"method":"io.frostyard.nsl.Broker.Open","parameters":{"target":"https://x"},"oneway":true}`: "io.frostyard.nsl.Broker.Refused",
		`{"method":"io.frostyard.nsl.Broker.Open","parameters":{"target":"https://x"},"method":"x"}`:  "io.frostyard.nsl.Broker.Refused",
		`{"method":"org.varlink.service.GetInfo","parameters":{}}`:                                    "org.varlink.service.MethodNotFound",
		`not json`: "io.frostyard.nsl.Broker.Refused",
	} {
		if reply := brokerCall(t, a, message); reply["error"] != errorName {
			t.Fatalf("%s: %v", message, reply)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ran("opener") != 1 {
		t.Fatal("opened a refused target")
	}
	f.agent = nil
	a.opener = "failing"
	f.blocking = func(ctx context.Context, bin string, args []string) (bool, error) {
		return bin == "failing", errors.New("exit status 4")
	}
	f.mu.Unlock()
	reply = brokerCall(t, a, `{"method":"io.frostyard.nsl.Broker.Open","parameters":{"target":"https://example.com"}}`)
	f.mu.Lock()
	if reply["error"] != "io.frostyard.nsl.Broker.Failed" {
		t.Fatal(reply)
	}
}

func TestDesktopSessionStartsWithTheMachine(t *testing.T) {
	a, f, m := withMachine(t)
	if err := a.execute([]string{"start", "debian"}); err != nil {
		t.Fatal(err)
	}
	v, _ := a.loadVM()
	if _, ok := f.states[desktopUnit(v, "debian")]; ok {
		t.Fatal("started a desktop session without a Wayland session")
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("NSL_OPENER", "/usr/bin/opener")
	a.waypipe = "true"
	if err := a.execute([]string{"start", "debian"}); err != nil {
		t.Fatal(err)
	}
	var launch string
	for _, c := range f.calls {
		if c.Bin == "systemd-run" && strings.Contains(strings.Join(c.Args, " "), "desktop-debian") {
			launch = strings.Join(c.Args, " ")
		}
	}
	for _, want := range []string{"--unit=" + desktopUnit(v, "debian"), "--description=nsl desktop " + v.ID + " debian", "--property=BindsTo=" + vmUnit(v),
		"--setenv=WAYLAND_DISPLAY=wayland-0", "--setenv=NSL_OPENER=/usr/bin/opener", "--property=Type=notify", "-- /test/nsl _desktop debian"} {
		if !strings.Contains(launch, want) {
			t.Fatalf("missing %s in %s", want, launch)
		}
	}
	// Removal stops the session and deletes the machine's SSH keys.
	a.machineKey(m)
	if err := a.remove([]string{"debian", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if f.states[desktopUnit(v, "debian")] != "inactive" {
		t.Fatal("left the desktop session running")
	}
	if _, err := os.Lstat(a.sshDir("debian")); !os.IsNotExist(err) {
		t.Fatal("kept the machine's SSH keys")
	}
}

func TestDesktopJoinsWaypipeAndTheBrokerToTheAgent(t *testing.T) {
	a, f, m := withMachine(t)
	a.waypipe = "waypipe"
	// Unix socket paths must stay short.
	short, _ := os.MkdirTemp("", "nsl")
	t.Cleanup(func() { os.RemoveAll(short) })
	a.runtimeDir = short
	var sshArgs []string
	f.mu.Lock()
	f.blocking = func(ctx context.Context, bin string, args []string) (bool, error) {
		switch {
		case bin == "waypipe":
			l, err := net.Listen("unix", args[2])
			if err != nil {
				return true, err
			}
			defer l.Close()
			<-ctx.Done()
			return true, nil
		case bin == "ssh" && strings.Contains(strings.Join(args, " "), " -R "):
			sshArgs = args
			return true, nil // the VM went away
		}
		return false, nil
	}
	f.mu.Unlock()
	err := a.desktop([]string{"debian"})
	if err == nil || !strings.Contains(err.Error(), "the desktop session ended") {
		t.Fatal(err)
	}
	v, _ := a.loadVM()
	dir := filepath.Join(a.runtimeDir, v.ID)
	joined := strings.Join(sshArgs, " ")
	for _, want := range []string{"-o ControlMaster=no", "-o ExitOnForwardFailure=yes",
		"-R /run/nsl/waypipe-debian.sock:" + dir + "/debian-waypipe.sock", "-R /run/nsl/desktop/debian/open.sock:" + dir + "/debian-open.sock"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	req, err := protocol.Decode(sshArgs[len(sshArgs)-1])
	if err != nil || req.Op != "display" || req.ID != m.ID {
		t.Fatal(err, req)
	}
}

func TestSSHConfigReachesTheMachineThroughTheAgent(t *testing.T) {
	a, f, m := withMachine(t)
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"ssh-config", "debian"}); err != nil {
		t.Fatal(err)
	}
	dir := a.sshDir("debian")
	for _, want := range []string{"Host nsl-debian\n", "User u\n", `IdentityFile "` + dir + `/id_ed25519"`, "HostKeyAlias nsl-debian-" + m.ID,
		`UserKnownHostsFile "` + dir + `/known_hosts"`, "ProxyCommand env NSL_HOME='" + a.home + "' '/test/nsl' _ssh debian\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in\n%s", want, out.String())
		}
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0700 {
		t.Fatal(err)
	}
	if last := f.requests[len(f.requests)-1]; last.Op != "start" {
		t.Fatal("did not start the machine", last)
	}
	if err := a.execute([]string{"_ssh", "debian"}); err != nil {
		t.Fatal(err)
	}
	req := f.requests[len(f.requests)-1]
	if req.Op != "ssh" || req.PublicKey != "ssh-ed25519 AAAA nsl-vm" || req.IdleTimeout == nil {
		t.Fatalf("%+v", req)
	}
	if n := f.ran("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nsl-debian"); n != 1 {
		t.Fatal("regenerated the machine's key", n)
	}
	a.self = "/opt/100% nsl/nsl"
	out.Reset()
	a.execute([]string{"ssh-config", "debian"})
	if !strings.Contains(out.String(), "'/opt/100%% nsl/nsl' _ssh debian") {
		t.Fatal(out.String())
	}
}

func TestNotifyReady(t *testing.T) {
	short, _ := os.MkdirTemp("", "nsl")
	defer os.RemoveAll(short)
	socket := filepath.Join(short, "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	t.Setenv("NOTIFY_SOCKET", socket)
	if err = notifyReady(); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 64)
	n, _ := conn.Read(b)
	if string(b[:n]) != "READY=1" {
		t.Fatal(string(b[:n]))
	}
}

func TestLiveHelpersCostNoSystemdCall(t *testing.T) {
	a, f, v := startedVM(t)
	show := func() int { return f.ran("systemctl", "--user", "show", portsUnit(v)) }
	before := show()
	os.WriteFile(filepath.Join(v.dir, "ports.json"), []byte("{}"), 0600)
	if _, err := a.runningVM(true); err != nil {
		t.Fatal(err)
	}
	if show() != before {
		t.Fatal("asked systemd about a forwarder that reported just now")
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(v.dir, "ports.json"), old, old)
	if _, err := a.runningVM(true); err != nil {
		t.Fatal(err)
	}
	if show() != before+1 {
		t.Fatal("did not check a forwarder whose report is stale")
	}
}
