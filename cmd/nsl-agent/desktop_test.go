package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

// tcpLine is one /proc/net/tcp row: local address, state and socket inode.
func tcpLine(local, state string, inode int) string {
	return fmt.Sprintf("   0: %s 00000000:0000 %s 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 100 0 0 10 0\n", local, state, inode)
}

func process(t *testing.T, root string, pid int, cgroup string, inodes ...int) {
	t.Helper()
	dir := fmt.Sprintf("%s/proc/%d", root, pid)
	os.MkdirAll(dir+"/fd", 0755)
	os.WriteFile(dir+"/cgroup", []byte("0::"+cgroup+"\n"), 0644)
	for i, inode := range inodes {
		os.Symlink(fmt.Sprintf("socket:[%d]", inode), fmt.Sprintf("%s/fd/%d", dir, i+3))
	}
}

func TestListeners(t *testing.T) {
	ta := newTestAgent(t, "shared")
	ta.addMachine(t, "debian", machineID)
	ta.addMachine(t, "fedora", strings.Repeat("1", 32))
	os.MkdirAll(ta.root+"/proc/net", 0755)
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	os.WriteFile(ta.root+"/proc/net/tcp", []byte(header+
		tcpLine("0100007F:1F40", "0A", 100)+ // 127.0.0.1:8000, debian
		tcpLine("00000000:0BB8", "0A", 101)+ // 0.0.0.0:3000, fedora
		tcpLine("0F02000A:2328", "0A", 102)+ // 10.0.2.15:9000: not reachable through loopback
		tcpLine("0100007F:0050", "0A", 106)+ // port 80
		tcpLine("0100007F:14E9", "0A", 107)+ // 5353
		tcpLine("0100007F:1F41", "01", 108)+ // established
		tcpLine("0100007F:1F42", "0A", 103)), 0644) // held by a VM service
	os.WriteFile(ta.root+"/proc/net/tcp6", []byte(header+
		tcpLine("00000000000000000000000001000000:1435", "0A", 104)+ // [::1]:5173, debian
		tcpLine("00000000000000000000000000000000:1F90", "0A", 105)), 0644) // [::]:8080, fedora
	process(t, ta.root, 200, "/machine.slice/systemd-nspawn@debian.service/payload/system.slice/app.service", 100, 102, 104, 106, 107, 108)
	process(t, ta.root, 201, "/machine.slice/systemd-nspawn@fedora.service/payload/user.slice/x.scope", 101, 105)
	process(t, ta.root, 300, "/system.slice/nsl-ssh@1.service", 103)
	process(t, ta.root, 301, "/machine.slice/systemd-nspawn@ghost.service/payload", 103)
	if status := ta.serve(request(t, protocol.Request{Op: "listeners"})); status != 0 {
		t.Fatal(ta.errors())
	}
	var got []protocol.Listener
	if err := json.Unmarshal([]byte(ta.output()), &got); err != nil {
		t.Fatal(err, ta.output())
	}
	want := []protocol.Listener{{Port: 3000, Address: "0.0.0.0", Machine: "fedora"}, {Port: 5173, Address: "::1", Machine: "debian"}, {Port: 8000, Address: "127.0.0.1", Machine: "debian"}, {Port: 8080, Address: "::", Machine: "fedora"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	// Watching ports is not activity: the VM may still go idle.
	if _, err := os.Stat(ta.root + requestsLock); !os.IsNotExist(err) {
		t.Fatal("listeners counted as activity")
	}
}

// desktopAgent has a running shared machine whose account is this test's user,
// and a fake Waypipe server that serves the display socket until cancelled.
func desktopAgent(t *testing.T) (*testAgent, *os.File) {
	t.Helper()
	ta := newTestAgent(t, "shared")
	r := protocol.MachineRecord{Schema: 1, Name: "debian", ID: machineID, Account: protocol.Account{User: "u", Group: "u", UID: os.Getuid(), GID: os.Getgid()}, BuildID: "b", Created: "2026-09-28T00:00:00Z"}
	b, _ := json.Marshal(r)
	os.WriteFile(ta.root+recordsDir+"/debian.json", b, 0600)
	ta.sys.units[unitOf("debian")] = "active"
	ta.sys.leaders = map[string]uint32{"debian": 4242}
	ta.sys.bound = make(chan struct{}, 4)
	os.MkdirAll(ta.root+desktopOf("debian"), 0755)
	for _, socket := range []string{ta.root + waypipeSocket("debian"), ta.root + desktopOf("debian") + "/open.sock"} {
		l, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		t.Cleanup(func() { l.Close() })
	}
	ta.r.handler = func(argv []string, stdin io.Reader, stdout io.Writer) error {
		if argv[0] != "waypipe" {
			return nil
		}
		display := argv[5]
		l, err := net.Listen("unix", display)
		if err != nil {
			return err
		}
		defer l.Close()
		<-ta.r.ctx.Done()
		return ta.r.ctx.Err()
	}
	reader, writer, _ := os.Pipe()
	ta.stdin = reader
	t.Cleanup(func() { reader.Close(); writer.Close() })
	return ta, writer
}

func TestDisplayServesTheMachineUntilTheSessionEnds(t *testing.T) {
	ta, session := desktopAgent(t)
	done := make(chan int, 1)
	go func() {
		done <- ta.serve(request(t, protocol.Request{Op: "display", Machine: "debian", ID: machineID}))
	}()
	display := ta.root + displaySocket("debian")
	select {
	case <-ta.sys.bound:
	case <-time.After(5 * time.Second):
		t.Fatal("never bound the desktop:", ta.errors())
	}
	if !reflect.DeepEqual(ta.sys.binds, [][3]string{{"debian", desktopOf("debian"), "/run/nsl/desktop"}}) {
		t.Fatal(ta.sys.binds)
	}
	for _, socket := range []string{display, ta.root + desktopOf("debian") + "/open.sock"} {
		if st, err := os.Stat(socket); err != nil || st.Mode().Perm() != 0600 {
			t.Fatal(socket, err)
		}
	}
	if env := ta.desktopEnv("debian"); !reflect.DeepEqual(env, []string{"WAYLAND_DISPLAY=/run/nsl/desktop/wayland-0", "BROWSER=nsl-open"}) {
		t.Fatal(env)
	}
	session.Close()
	select {
	case status := <-done:
		if status != 0 {
			t.Fatal(status, ta.errors())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session outlived its SSH connection")
	}
	for _, c := range ta.r.calls {
		if c[0] == "waypipe" && strings.Join(c, " ") != "waypipe --no-gpu --socket "+ta.root+waypipeSocket("debian")+" --display "+display+" server -- sleep infinity" {
			t.Fatal(c)
		}
	}
	if env := ta.desktopEnv("debian"); len(env) != 0 {
		t.Fatal("left the desktop's sockets:", env)
	}
	if ta.output() != "ready\n" {
		t.Fatalf("%q", ta.output())
	}
}

func TestDesktopBindsOnce(t *testing.T) {
	ta, _ := desktopAgent(t)
	ta.prepareDesktop("debian", true)
	// The machine now sees the directory, so a second start binds nothing.
	os.MkdirAll(ta.root+"/proc/4242/root/run/nsl", 0755)
	os.Symlink(ta.root+desktopOf("debian"), ta.root+"/proc/4242/root/run/nsl/desktop")
	ta.prepareDesktop("debian", true)
	if len(ta.sys.binds) != 1 {
		t.Fatal(ta.sys.binds)
	}
}

func TestIsolatedVMHasNoDesktop(t *testing.T) {
	ta := newTestAgent(t, "isolated")
	ta.addMachine(t, "iso", machineID)
	if status := ta.serve(request(t, protocol.Request{Op: "display", Machine: "iso", ID: machineID})); status != 255 || !strings.Contains(ta.errors(), "refused") {
		t.Fatal(status, ta.errors())
	}
}

func TestSSH(t *testing.T) {
	ta := newTestAgent(t, "shared")
	ta.addMachine(t, "debian", machineID)
	ssh := protocol.Request{Op: "ssh", Machine: "debian", ID: machineID, PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 nsl-debian", IdleTimeout: minutes(15)}
	if status := ta.serve(request(t, ssh)); status != 255 || !strings.Contains(ta.errors(), "not-running") {
		t.Fatal(status, ta.errors())
	}
	ta.sys.units[unitOf("debian")] = "active"
	ta.sys.manager.code = 1 // exited with status 0
	os.MkdirAll(ta.root+machinesDir+"/debian/etc", 0755)
	ta.r.handler = func(argv []string, stdin io.Reader, stdout io.Writer) error {
		if argv[0] == "ssh-keygen" {
			key := argv[len(argv)-1]
			os.WriteFile(key, []byte("private"), 0600)
			return os.WriteFile(key+".pub", []byte("ssh-ed25519 HOST nsl-debian\n"), 0644)
		}
		return nil
	}
	for range 2 {
		if status := ta.serve(request(t, ssh)); status != 0 {
			t.Fatal(ta.errors())
		}
	}
	if ta.r.ran("ssh-keygen") != 1 {
		t.Fatal("replaced the machine's host key")
	}
	dir := filepath.Join(ta.root, machinesDir, "debian", sshDir)
	if b, _ := os.ReadFile(dir + "/authorized_keys"); string(b) != ssh.PublicKey+"\n" {
		t.Fatal(string(b))
	}
	if st, err := os.Stat(dir + "/ssh_host_ed25519_key"); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal(err)
	}
	spec := ta.sys.manager.started[1]
	if spec.User != "root" || spec.PAM || spec.RuntimeDir != "sshd" || spec.TTY != "" || spec.Argv[0] != "sshd" || spec.Argv[1] != "-i" ||
		!strings.Contains(strings.Join(spec.Argv, " "), "-o AllowUsers=u -o PermitRootLogin=no -o PasswordAuthentication=no") {
		t.Fatalf("%+v", spec)
	}
	if _, err := os.Stat(ta.root + activityDir + "/debian"); err != nil {
		t.Fatal("an ssh session is not activity:", err)
	}
}
