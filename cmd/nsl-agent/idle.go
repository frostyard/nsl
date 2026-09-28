package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Idle stop (docs/specs/vm-image.md#machines). A running machine with no nsl
// command sessions and no Waypipe clients for idle_timeout minutes powers off.
// The VM powers off once no machine has run and no request has been in flight
// for vmGrace, which also covers a VM that has just started.
const (
	idlePoll   = 10 * time.Second
	vmGrace    = 60 * time.Second
	displayDir = runtimeDir + "/wayland"
)

// displaySocket is the Waypipe server's socket for a machine, in the VM.
func displaySocket(name string) string { return displayDir + "/" + name + "/wayland-0" }

type idleMonitor struct {
	a         *agent
	now       func() time.Time
	idleSince map[string]time.Time
	busy      time.Time // the latest moment a machine ran or a request was in flight
	off       *os.File  // the requests lock, held once the VM is powering off
}

func (a *agent) idle() error {
	if a.sys == nil {
		var err error
		if a.sys, err = newSystem(); err != nil {
			return err
		}
	}
	m := &idleMonitor{a: a, now: time.Now, idleSince: map[string]time.Time{}, busy: time.Now()}
	for {
		if err := m.step(); err != nil {
			fmt.Fprintln(a.stderr, "nsl-agent idle:", err)
		}
		time.Sleep(idlePoll)
	}
}

// idleTimeout is the latest idle_timeout from a request or the credential; an
// unreadable value disables idle stop rather than guessing.
func (a *agent) idleTimeout() time.Duration {
	b, err := os.ReadFile(a.path(runtimeDir + "/idle-timeout"))
	minutes, parseErr := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || parseErr != nil || minutes < 0 || minutes > 1440 {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}

func modified(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// displayClients counts connections accepted on a machine's display socket:
// /proc/net/unix lists each with the listener's path in state 03, connected.
func (a *agent) displayClients(name string) (int, error) {
	b, err := os.ReadFile(a.path("/proc/net/unix"))
	if err != nil {
		return 0, err
	}
	socket, n := displaySocket(name), 0
	for _, line := range strings.Split(string(b), "\n") {
		// Num RefCount Protocol Flags Type St Inode Path
		if f := strings.Fields(line); len(f) == 8 && f[5] == "03" && f[7] == socket {
			n++
		}
	}
	return n, nil
}

// active reports whether a running machine has command sessions or GUI clients.
func (a *agent) active(name string) (bool, error) {
	m, err := a.sys.Machine(name)
	if err != nil {
		return false, err
	}
	defer m.Close()
	sessions, err := m.Sessions()
	if err != nil {
		return false, err
	}
	clients, err := a.displayClients(name)
	return sessions+clients > 0, err
}

func (a *agent) anyRunning() (bool, error) {
	records, err := a.records()
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if state, err := a.state(r.Name); err != nil || state != "stopped" {
			return true, err
		}
	}
	return false, nil
}

func (m *idleMonitor) step() error {
	if m.off != nil {
		return nil
	}
	now, timeout := m.now(), m.a.idleTimeout()
	records, err := m.a.records()
	if err != nil {
		return err
	}
	running := false
	for _, r := range records {
		state, err := m.a.state(r.Name)
		if err != nil {
			return err
		}
		if state == "stopped" {
			delete(m.idleSince, r.Name)
			continue
		}
		running = true
		if state != "running" {
			continue
		}
		// An unanswered question counts as activity: stopping is the risky choice.
		if active, err := m.a.active(r.Name); err != nil || active {
			m.idleSince[r.Name] = now
			continue
		}
		since, seen := m.idleSince[r.Name]
		if !seen {
			since = now
		}
		if t := modified(m.a.path(activityDir + "/" + r.Name)); t.After(since) {
			since = t
		}
		m.idleSince[r.Name] = since
		if timeout > 0 && now.Sub(since) >= timeout {
			m.stopIdle(r.Name, since)
		}
	}
	if running {
		m.busy = now
	}
	return m.powerOffIdleVM(now)
}

// stopIdle powers an idle machine off unless a lifecycle operation holds it or
// a request arrived since the check.
func (m *idleMonitor) stopIdle(name string, since time.Time) {
	release, err := m.a.lockMachine(name, 0)
	if err != nil {
		return
	}
	defer release()
	if active, err := m.a.active(name); err != nil || active || modified(m.a.path(activityDir+"/"+name)).After(since) {
		return
	}
	fmt.Fprintf(m.a.stderr, "nsl-agent idle: stopping %s, idle since %s\n", name, since.UTC().Format(time.RFC3339))
	if err = m.a.powerOff(name); err != nil {
		fmt.Fprintln(m.a.stderr, "nsl-agent idle:", err)
	}
	delete(m.idleSince, name)
}

// powerOffIdleVM powers the VM off when no machine has run and no request has
// been in flight or arrived for vmGrace. It keeps the requests lock, so a
// request that arrives now waits until the VM is gone and the host starts it
// again.
func (m *idleMonitor) powerOffIdleVM(now time.Time) error {
	path := m.a.path(requestsLock)
	last := m.busy
	if t := modified(path); t.After(last) {
		last = t
	}
	if now.Sub(last) < vmGrace {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		m.busy = now
		return nil
	}
	// Under the lock no request can start a machine; check once more.
	running, err := m.a.anyRunning()
	if err != nil || running {
		m.busy = now
		f.Close()
		return err
	}
	fmt.Fprintln(m.a.stderr, "nsl-agent idle: no machine is running; powering the VM off")
	if _, err = m.a.output("systemctl", "poweroff", "--no-block"); err != nil {
		f.Close()
		return err
	}
	m.off = f
	return nil
}
