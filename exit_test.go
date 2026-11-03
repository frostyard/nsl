package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// exitError runs a command that exits 3 and returns its *exec.ExitError.
func exitError(t *testing.T) *exec.ExitError {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 3").Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("sh exit 3 gave %v, not an *exec.ExitError", err)
	}
	return exit
}

func TestExitStatusBareExitError(t *testing.T) {
	var stderr bytes.Buffer
	if got := exitStatus(exitError(t), &stderr); got != 3 {
		t.Fatalf("exitStatus of a bare exit error = %d, want 3", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("a bare exit error printed %q, want nothing", stderr.String())
	}
}

func TestExitStatusWrappedExitError(t *testing.T) {
	err := fmt.Errorf("systemctl: %w: Failed to connect to bus", exitError(t))
	var stderr bytes.Buffer
	if got := exitStatus(err, &stderr); got != 1 {
		t.Fatalf("exitStatus of a wrapped exit error = %d, want 1", got)
	}
	for _, want := range []string{"nsl: systemctl:", "Failed to connect to bus"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr %q does not hold %q", stderr.String(), want)
		}
	}
}

func TestExitStatusPlainError(t *testing.T) {
	var stderr bytes.Buffer
	if got := exitStatus(errors.New("x"), &stderr); got != 1 {
		t.Fatalf("exitStatus of a plain error = %d, want 1", got)
	}
	if want := "nsl: x\n"; stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func socketDir(t *testing.T, names ...string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nsl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
	}
	return dir
}

func TestUserSystemdHint(t *testing.T) {
	t.Run("both unset", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", "")
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
		hint := userSystemdHint()
		for _, want := range []string{"XDG_RUNTIME_DIR is unset", "DBUS_SESSION_BUS_ADDRESS is unset", "set XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(os.Getuid())} {
			if !strings.Contains(hint, want) {
				t.Fatalf("hint %q does not hold %q", hint, want)
			}
		}
	})
	t.Run("systemd private socket", func(t *testing.T) {
		dir := socketDir(t, "systemd/private")
		t.Setenv("XDG_RUNTIME_DIR", dir)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
		if hint := userSystemdHint(); hint != "" {
			t.Fatalf("hint = %q with systemd/private socket, want none", hint)
		}
	})
	t.Run("bus socket", func(t *testing.T) {
		dir := socketDir(t, "bus")
		t.Setenv("XDG_RUNTIME_DIR", dir)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
		if hint := userSystemdHint(); hint != "" {
			t.Fatalf("hint = %q with bus socket, want none", hint)
		}
	})
	t.Run("socket absent", func(t *testing.T) {
		dir := socketDir(t)
		t.Setenv("XDG_RUNTIME_DIR", dir)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
		hint := userSystemdHint()
		if !strings.Contains(hint, "no user bus socket") || !strings.Contains(hint, dir) {
			t.Fatalf("hint %q does not hold no user bus socket and %q", hint, dir)
		}
	})
	t.Run("DBUS address set", func(t *testing.T) {
		dir := socketDir(t)
		t.Setenv("XDG_RUNTIME_DIR", dir)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
		if hint := userSystemdHint(); hint != "" {
			t.Fatalf("hint = %q with DBUS_SESSION_BUS_ADDRESS set, want none", hint)
		}
	})
}

// busFailRunner fails every call the way systemctl does on a shell that
// cannot reach the user manager.
type busFailRunner struct{}

const busMessage = "Failed to connect to bus: No medium found"

func (busFailRunner) run(ctx context.Context, in io.Reader, out, stderr io.Writer, env []string, bin string, args ...string) error {
	fmt.Fprintln(stderr, busMessage)
	return errors.New("exit status 1")
}

// passRunner returns one error value, to see whether call wraps it.
type passRunner struct{ err error }

func (r passRunner) run(ctx context.Context, in io.Reader, out, stderr io.Writer, env []string, bin string, args ...string) error {
	return r.err
}

func TestCaptureUserSystemdHint(t *testing.T) {
	a, _ := testApp(t)
	a.r = busFailRunner{}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")

	t.Run("unset", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", "")
		_, err := a.capture(5*time.Second, "systemctl", "--user", "show", "nsl-1-vm-x.service")
		if err == nil {
			t.Fatal("capture of a failing systemctl --user succeeded")
		}
		for _, want := range []string{busMessage, "XDG_RUNTIME_DIR is unset", "set XDG_RUNTIME_DIR=/run/user/"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not hold %q", err, want)
			}
		}
	})

	t.Run("set", func(t *testing.T) {
		dir := socketDir(t, "systemd/private")
		t.Setenv("XDG_RUNTIME_DIR", dir)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
		_, err := a.capture(5*time.Second, "systemctl", "--user", "show", "nsl-1-vm-x.service")
		if err == nil {
			t.Fatal("capture of a failing systemctl --user succeeded")
		}
		if !strings.Contains(err.Error(), busMessage) {
			t.Fatalf("error %q does not hold the bus message", err)
		}
		if strings.Contains(err.Error(), "no user bus socket") {
			t.Fatalf("error %q holds a socket hint although systemd/private is a socket", err)
		}
	})

	t.Run("socket absent", func(t *testing.T) {
		dir := socketDir(t)
		t.Setenv("XDG_RUNTIME_DIR", dir)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
		_, err := a.capture(5*time.Second, "systemctl", "--user", "show", "nsl-1-vm-x.service")
		if err == nil {
			t.Fatal("capture of a failing systemctl --user succeeded")
		}
		if !strings.Contains(err.Error(), busMessage) {
			t.Fatalf("error %q does not hold the bus message", err)
		}
		if !strings.Contains(err.Error(), "no user bus socket") {
			t.Fatalf("error %q does not hold no user bus socket", err)
		}
	})
}

func TestCallUserSystemdHint(t *testing.T) {
	a, _ := testApp(t)
	a.r = busFailRunner{}
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	err := a.call(nil, io.Discard, "systemd-run", "--user", "--unit=nsl-1-vm-x.service", "true")
	if err == nil {
		t.Fatal("call of a failing systemd-run --user succeeded")
	}
	for _, want := range []string{"systemd-run:", "exit status 1", "XDG_RUNTIME_DIR is unset"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not hold %q", err, want)
		}
	}
}

func TestCallPassesMachineCommandErrorThrough(t *testing.T) {
	a, _ := testApp(t)
	want := errors.New("exit status 255")
	a.r = passRunner{err: want}
	if err := a.call(nil, io.Discard, "ssh", "-F", "config", "vm"); err != want {
		t.Fatalf("call of a machine command gave %v, want the bare %v", err, want)
	}
}
