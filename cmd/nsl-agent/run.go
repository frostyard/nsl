package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
	"golang.org/x/sys/unix"
)

// systemd's own exit statuses for failures before the command runs.
var systemdSteps = map[int32]string{200: "CHDIR", 203: "EXEC", 216: "GROUP", 217: "USER", 224: "PAM"}

func (a *agent) runCommand(req *protocol.Request) (int, error) {
	rec, err := a.record(req)
	if err != nil {
		return 0, err
	}
	state, err := a.state(req.Machine)
	if err != nil {
		return 0, err
	}
	if state != "running" {
		return 0, &protocol.Error{Code: protocol.CodeNotRunning, Message: req.Machine + " is " + state}
	}
	a.saveIdleTimeout(*req.IdleTimeout)
	user, home := rec.Account.User, "/home/"+rec.Account.User
	if req.Root {
		user, home = "root", "/root"
	}
	spec := unitSpec{Argv: req.Argv, User: user, PAM: !req.Root, Directory: req.Directory,
		SearchPath: []string{home + "/.local/bin", "/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"},
		Stdio:      [3]int{int(a.stdin.Fd()), int(a.stdout.Fd()), int(a.stderr.Fd())}}
	if spec.Directory == "" {
		spec.Directory = home
	}
	for k, v := range req.Env {
		spec.Env = append(spec.Env, k+"="+v)
	}
	sort.Strings(spec.Env)
	m, err := a.sys.Machine(req.Machine)
	if err != nil {
		return 0, err
	}
	defer m.Close()
	unit := "nsl-run-" + randomHex(16) + ".service"
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGTERM)
	defer cancel()
	var pty *ptyForwarder
	if req.TTY {
		if !isTerminal(a.stdin) {
			return 0, &protocol.Error{Code: protocol.CodeBadRequest, Message: "tty requires a terminal on the session"}
		}
		master, path, err := a.sys.OpenPTY(req.Machine)
		if err != nil {
			return 0, err
		}
		defer master.Close()
		spec.TTY = path
		if pty, err = forwardPTY(a.stdin, a.stdout, master); err != nil {
			return 0, err
		}
		defer pty.restore()
	}
	if err = m.Start(unit, spec); err != nil {
		return 0, &protocol.Error{Code: protocol.CodeFailed, Message: "starting the command: " + err.Error()}
	}
	defer m.Discard(unit)
	code, status, err := m.Wait(ctx, unit)
	if err != nil {
		// The session ended: stop the command rather than leave it behind.
		return 0, &protocol.Error{Code: protocol.CodeFailed, Message: "command interrupted: " + err.Error()}
	}
	if pty != nil {
		m.Discard(unit)
		pty.drain(time.Second)
	}
	if step, ok := systemdSteps[status]; ok && code == 1 {
		fmt.Fprintf(a.stderr, "nsl-agent: %s could not start (systemd %d/%s)\n", req.Argv[0], status, step)
	}
	return protocol.ExitStatus(code, status), nil
}

func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// ptyForwarder copies bytes unchanged between the session's terminal and a
// PTY in the machine. It adds no title, color or other escape sequences.
type ptyForwarder struct {
	in, out, master *os.File
	saved           *unix.Termios
	winch           chan os.Signal
	output          chan struct{}
}

func forwardPTY(in, out, master *os.File) (*ptyForwarder, error) {
	saved, err := unix.IoctlGetTermios(int(in.Fd()), unix.TCGETS)
	if err != nil {
		return nil, err
	}
	raw := *saved
	// The machine's PTY applies the line discipline; this one passes bytes through,
	// so Ctrl-C reaches the command as a byte.
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err = unix.IoctlSetTermios(int(in.Fd()), unix.TCSETS, &raw); err != nil {
		return nil, err
	}
	p := &ptyForwarder{in: in, out: out, master: master, saved: saved, winch: make(chan os.Signal, 1), output: make(chan struct{})}
	p.resize()
	signal.Notify(p.winch, syscall.SIGWINCH)
	go func() {
		for range p.winch {
			p.resize()
		}
	}()
	go func() {
		// Input ends at EOF; the command's output continues until it exits.
		io.Copy(master, in)
	}()
	go func() {
		defer close(p.output)
		io.Copy(out, master)
	}()
	return p, nil
}

func (p *ptyForwarder) resize() {
	if ws, err := unix.IoctlGetWinsize(int(p.in.Fd()), unix.TIOCGWINSZ); err == nil {
		unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, ws)
	}
}

// drain waits for output that is still in the PTY after the command exited.
func (p *ptyForwarder) drain(limit time.Duration) {
	select {
	case <-p.output:
	case <-time.After(limit):
	}
}

func (p *ptyForwarder) restore() {
	signal.Stop(p.winch)
	close(p.winch)
	unix.IoctlSetTermios(int(p.in.Fd()), unix.TCSETS, p.saved)
}
