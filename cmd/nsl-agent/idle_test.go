package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func idleAgent(t *testing.T, timeout string) (*testAgent, *idleMonitor, *time.Time) {
	t.Helper()
	ta := newTestAgent(t, "shared")
	for _, name := range []string{"debian", "fedora"} {
		ta.addMachine(t, name, machineID)
		ta.sys.units[unitOf(name)] = "active"
	}
	ta.sys.managers = map[string]*fakeManager{"debian": {system: "running"}, "fedora": {system: "running"}}
	os.MkdirAll(ta.root+runtimeDir, 0755)
	os.WriteFile(ta.root+runtimeDir+"/idle-timeout", []byte(timeout+"\n"), 0644)
	os.MkdirAll(ta.root+"/proc/net", 0755)
	os.WriteFile(ta.root+"/proc/net/unix", []byte("Num       RefCount Protocol Flags    Type St Inode Path\n"), 0644)
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	m := &idleMonitor{a: ta.agent, now: func() time.Time { return clock }, idleSince: map[string]time.Time{}, busy: clock}
	return ta, m, &clock
}

func touch(t *testing.T, path string, when time.Time) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	os.Chtimes(path, when, when)
}

func TestIdleMachinesStop(t *testing.T) {
	ta, m, clock := idleAgent(t, "1")
	began := *clock
	m.step()
	ta.sys.managers["debian"].sessions = 1
	*clock = began.Add(61 * time.Second)
	m.step()
	if ta.sys.units[unitOf("fedora")] != "inactive" || ta.sys.units[unitOf("debian")] != "active" {
		t.Fatal("stopped the wrong machines:", ta.sys.units)
	}
	// A start or run restarts the idle clock.
	ta.sys.managers["debian"].sessions = 0
	os.MkdirAll(ta.root+activityDir, 0755)
	touch(t, ta.root+activityDir+"/debian", began.Add(100*time.Second))
	*clock = began.Add(130 * time.Second)
	m.step()
	if ta.sys.units[unitOf("debian")] != "active" {
		t.Fatal("stopped a machine used 30 s ago")
	}
	// A connected Waypipe client keeps the machine running.
	os.WriteFile(ta.root+"/proc/net/unix", []byte("Num       RefCount Protocol Flags    Type St Inode Path\n"+
		"0000000000000000: 00000002 00000000 00010000 0001 01 100 /run/nsl/desktop/debian/wayland-0\n"+
		"0000000000000000: 00000003 00000000 00000000 0001 03 101 /run/nsl/desktop/debian/wayland-0\n"), 0644)
	*clock = began.Add(300 * time.Second)
	m.step()
	if ta.sys.units[unitOf("debian")] != "active" {
		t.Fatal("stopped a machine with a GUI client")
	}
	os.WriteFile(ta.root+"/proc/net/unix", []byte("Num       RefCount Protocol Flags    Type St Inode Path\n"+
		"0000000000000000: 00000002 00000000 00010000 0001 01 100 /run/nsl/desktop/debian/wayland-0\n"), 0644)
	// A lifecycle operation in progress defers the decision.
	release, _ := ta.lockMachine("debian", 0)
	*clock = began.Add(400 * time.Second)
	m.step()
	if ta.sys.units[unitOf("debian")] != "active" {
		t.Fatal("stopped a machine held by another operation")
	}
	release()
	*clock = began.Add(361*time.Second + 300*time.Second)
	m.step()
	if ta.sys.units[unitOf("debian")] != "inactive" || !strings.Contains(ta.errors(), "stopping debian") {
		t.Fatal("kept an idle machine", ta.sys.units)
	}
}

func TestIdleTimeoutZeroKeepsMachines(t *testing.T) {
	for _, timeout := range []string{"0", "garbage"} {
		ta, m, clock := idleAgent(t, timeout)
		m.step()
		*clock = clock.Add(48 * time.Hour)
		m.step()
		if ta.sys.units[unitOf("debian")] != "active" || ta.r.ran("systemctl", "poweroff") != 0 {
			t.Fatal(timeout, ta.sys.units)
		}
	}
}

func TestIdleVMPowersOff(t *testing.T) {
	ta, m, clock := idleAgent(t, "0")
	for _, name := range []string{"debian", "fedora"} {
		ta.sys.units[unitOf(name)] = "inactive"
	}
	began := *clock
	lock := ta.root + requestsLock
	touch(t, lock, began.Add(-time.Hour))
	*clock = began.Add(30 * time.Second)
	m.step()
	if ta.r.ran("systemctl", "poweroff") != 0 {
		t.Fatal("powered off within the grace period")
	}
	// A request in flight keeps the VM, and the grace starts again after it.
	held, _ := os.Open(lock)
	unix.Flock(int(held.Fd()), unix.LOCK_SH)
	*clock = began.Add(61 * time.Second)
	m.step()
	held.Close()
	*clock = began.Add(100 * time.Second)
	m.step()
	if ta.r.ran("systemctl", "poweroff") != 0 {
		t.Fatal("powered off during or just after a request")
	}
	// So does a recent request.
	touch(t, lock, began.Add(150*time.Second))
	*clock = began.Add(200 * time.Second)
	m.step()
	if ta.r.ran("systemctl", "poweroff") != 0 {
		t.Fatal("powered off 50 s after a request")
	}
	*clock = began.Add(211 * time.Second)
	m.step()
	if ta.r.ran("systemctl", "poweroff", "--no-block") != 1 || m.off == nil {
		t.Fatal("kept an idle VM")
	}
	// The lock stays held, so new requests wait for the VM to go.
	probe, _ := os.Open(lock)
	defer probe.Close()
	if err := unix.Flock(int(probe.Fd()), unix.LOCK_SH|unix.LOCK_NB); err == nil {
		t.Fatal("released the requests lock while powering off")
	}
	m.step()
	if ta.r.ran("systemctl", "poweroff") != 1 {
		t.Fatal("powered off twice")
	}
}

func TestRunningMachineKeepsTheVM(t *testing.T) {
	ta, m, clock := idleAgent(t, "0")
	touch(t, ta.root+requestsLock, clock.Add(-time.Hour))
	*clock = clock.Add(time.Hour)
	m.step()
	if ta.r.ran("systemctl", "poweroff") != 0 {
		t.Fatal("powered off with machines running")
	}
}
