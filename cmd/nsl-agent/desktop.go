package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

// A machine's desktop session (docs/specs/agent.md#display): the host forwards
// its Waypipe client to /run/nsl/waypipe-NAME.sock and its broker to
// /run/nsl/desktop/NAME/open.sock. Only /run/nsl/desktop/NAME reaches the
// machine, bound at /run/nsl/desktop.
const (
	desktopDir = runtimeDir + "/desktop"
	// machineDesktop is where a machine sees its desktop directory.
	machineDesktop = runtimeDir + "/desktop"
	socketWait     = 10 * time.Second
)

func desktopOf(name string) string     { return desktopDir + "/" + name }
func waypipeSocket(name string) string { return runtimeDir + "/waypipe-" + name + ".sock" }

// desktopEnv is the environment a command gets from a live desktop session.
func (a *agent) desktopEnv(name string) []string {
	var env []string
	if isSocket(a.path(displaySocket(name))) {
		env = append(env, "WAYLAND_DISPLAY="+machineDesktop+"/wayland-0")
	}
	if isSocket(a.path(desktopOf(name) + "/open.sock")) {
		env = append(env, "BROWSER=nsl-open")
	}
	return env
}

func isSocket(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode()&os.ModeSocket != 0
}

// waitSocket waits for a Unix socket to appear, or for done to report.
func waitSocket(path string, limit time.Duration, done <-chan error) error {
	deadline := time.Now().Add(limit)
	for !isSocket(path) {
		select {
		case err := <-done:
			return fmt.Errorf("%s did not appear: %v", path, err)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear within %s", path, limit)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// bindDesktop binds a running machine's desktop directory into it, unless the
// machine already sees that directory there.
func (a *agent) bindDesktop(name string) error {
	source := a.path(desktopOf(name))
	leader, err := a.sys.Leader(name)
	if err != nil {
		return err
	}
	if sameFile(source, a.path("/proc/"+strconv.FormatUint(uint64(leader), 10)+"/root"+machineDesktop)) {
		return nil
	}
	return a.sys.BindMount(name, desktopOf(name), machineDesktop)
}

func sameFile(x, y string) bool {
	a, err := os.Stat(x)
	if err != nil {
		return false
	}
	b, err := os.Stat(y)
	return err == nil && os.SameFile(a, b)
}

// prepareDesktop gives a starting machine its desktop directory, and binds it
// once the machine runs.
func (a *agent) prepareDesktop(name string, running bool) {
	if err := os.MkdirAll(a.path(desktopOf(name)), 0755); err != nil {
		fmt.Fprintln(a.stderr, "nsl-agent: desktop:", err)
		return
	}
	if running {
		// A machine without its desktop still runs commands.
		if err := a.bindDesktop(name); err != nil {
			fmt.Fprintln(a.stderr, "nsl-agent: binding the desktop into "+name+":", err)
		}
	}
}

// display serves a machine's desktop session until the host's SSH session ends.
func (a *agent) display(req *protocol.Request, binding *protocol.Binding) error {
	if binding.Role != "shared" {
		return &protocol.Error{Code: protocol.CodeRefused, Message: "an isolated machine has no desktop session"}
	}
	rec, err := a.record(req)
	if err != nil {
		return err
	}
	name := req.Machine
	transport, open, display := a.path(waypipeSocket(name)), a.path(desktopOf(name)+"/open.sock"), a.path(displaySocket(name))
	for _, socket := range []string{transport, open} {
		if err = waitSocket(socket, socketWait, nil); err != nil {
			return &protocol.Error{Code: protocol.CodeFailed, Message: "the host did not forward its sockets: " + err.Error()}
		}
	}
	defer os.Remove(open)
	if err = os.Remove(display); err != nil && !os.IsNotExist(err) {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := make(chan error, 1)
	go func() {
		server <- a.r.run(ctx, nil, io.Discard, a.stderr, "waypipe", "--no-gpu", "--socket", transport, "--display", display, "server", "--", "sleep", "infinity")
	}()
	defer os.Remove(display)
	if err = waitSocket(display, socketWait, server); err != nil {
		return &protocol.Error{Code: protocol.CodeFailed, Message: "the Waypipe server: " + err.Error()}
	}
	// The server and sshd run as VM root; the machine account connects.
	for _, socket := range []string{display, open} {
		if err = os.Chown(socket, rec.Account.UID, rec.Account.GID); err == nil {
			err = os.Chmod(socket, 0600)
		}
		if err != nil {
			return err
		}
	}
	if state, err := a.state(name); err == nil && state == "running" {
		if err = a.bindDesktop(name); err != nil {
			fmt.Fprintln(a.stderr, "nsl-agent: binding the desktop into "+name+":", err)
		}
	}
	// The host reports its session ready only now, so a command that started
	// the machine gets the display.
	if _, err = io.WriteString(a.stdout, "ready\n"); err != nil {
		return err
	}
	// The host holds stdin open for the session's lifetime; sshd may instead
	// signal the agent. Either way the deferred cleanup removes the sockets, so
	// no command gets a display that is gone.
	ended := make(chan struct{})
	go func() {
		io.Copy(io.Discard, a.stdin)
		close(ended)
	}()
	signals, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGTERM)
	defer stop()
	select {
	case <-ended:
	case <-signals.Done():
	case err = <-server:
		return &protocol.Error{Code: protocol.CodeFailed, Message: fmt.Sprintf("the Waypipe server exited: %v", err)}
	}
	cancel()
	<-server
	return nil
}
