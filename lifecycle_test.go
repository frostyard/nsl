package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

// Wait for real flock waiters so the regression controls lock ordering without
// depending on goroutine scheduling. Linux exposes the waiters in /proc/locks.
func waitForLockWaiters(t *testing.T, paths []string, count int) {
	t.Helper()
	inodes := map[string]bool{}
	for _, path := range paths {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		inodes[strconv.FormatUint(st.Sys().(*syscall.Stat_t).Ino, 10)] = true
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		data, err := os.ReadFile("/proc/locks")
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 8 || fields[1] != "->" || fields[2] != "FLOCK" || fields[5] != strconv.Itoa(os.Getpid()) {
				continue
			}
			parts := strings.Split(fields[6], ":")
			if len(parts) == 3 && inodes[parts[2]] {
				found++
			}
		}
		if found == count {
			return
		}
	}
	t.Fatalf("did not observe %d lock waiters", count)
}

func TestConcurrentIsolatedCreateAndRemove(t *testing.T) {
	if os.Getenv("NSL_TEST_CREATE_REMOVE_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConcurrentIsolatedCreateAndRemove$", "-test.timeout=8s")
		cmd.Env = append(os.Environ(), "NSL_TEST_CREATE_REMOVE_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create/remove did not complete: %v\n%s", err, out)
		}
		return
	}
	a, f, _ := startedVM(t)
	if err := a.initMachines(); err != nil {
		t.Fatal(err)
	}
	machinePath := filepath.Join(a.machinesDir(), ".iso.lock")
	heldMachine, err := fileLock(machinePath)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"iso", "--isolated"}, machineImage(t, "rootfs")...)
	created := make(chan error, 1)
	go func() { created <- a.create(args) }()
	waitForLockWaiters(t, []string{machinePath}, 1)
	m, err := a.machine("iso")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op != "machines" {
			return nil
		}
		if err := json.NewEncoder(stdout).Encode([]protocol.MachineStatus{{Machine: m.Name, ID: m.ID, State: "stopped"}}); err != nil {
			return err
		}
		return errAnswered
	}
	f.mu.Unlock()
	managerPath := filepath.Join(a.home, "lock")
	heldManager, err := fileLock(managerPath)
	if err != nil {
		t.Fatal(err)
	}
	removed := make(chan error, 1)
	go func() { removed <- a.remove([]string{"iso", "--yes"}) }()
	waitForLockWaiters(t, []string{machinePath, managerPath}, 2)
	unlock(heldMachine)
	waitForLockWaiters(t, []string{machinePath, managerPath}, 2)
	unlock(heldManager)
	if err := <-created; err != nil {
		t.Fatal("create:", err)
	}
	if err := <-removed; err != nil {
		t.Fatal("remove:", err)
	}
	if _, err := os.Stat(a.isolatedVMDir("iso")); !os.IsNotExist(err) {
		t.Fatal("VM retained:", err)
	}
}

func TestRemovalRejectsReplacementAfterWaiting(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(strconv.FormatBool(pending), func(t *testing.T) {
			a, f, m := withMachine(t)
			if pending {
				if err := os.Rename(a.machinePath(m.Name), a.removingPath(m.Name)); err != nil {
					t.Fatal(err)
				}
			}
			lockPath := filepath.Join(a.machinesDir(), "."+m.Name+".lock")
			held, err := fileLock(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- a.remove([]string{m.Name, "--yes"}) }()
			waitForLockWaiters(t, []string{lockPath}, 1)
			replacement := *m
			replacement.ID = randomID()
			if err := a.saveMachine(&replacement); err != nil {
				t.Fatal(err)
			}
			if pending {
				if err := os.Remove(a.removingPath(m.Name)); err != nil {
					t.Fatal(err)
				}
			}
			unlock(held)
			if err := <-done; err == nil || !strings.Contains(err.Error(), "replaced") {
				t.Fatal("accepted replacement:", err)
			}
			current, err := a.machine(m.Name)
			if err != nil || current.ID != replacement.ID {
				t.Fatal("replacement changed:", err)
			}
			if len(f.requests) != 0 {
				t.Fatal("contacted guest after replacement")
			}
		})
	}
}
