package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/frostyard/nsl/internal/protocol"
)

func TestFailedPreparationRetainsIdentityUntilCleanup(t *testing.T) {
	for _, op := range []string{"create", "import"} {
		for _, failure := range []string{"transport", "busy", "replacement", "create-busy"} {
			t.Run(op+"/"+failure, func(t *testing.T) {
				a, f, _ := withMachine(t)
				var args []string
				if op == "create" {
					args = append([]string{"create", "recoverable"}, machineImage(t, "rootfs")...)
				} else {
					args = []string{"import", "recoverable", exportTo(t, a, f)}
				}
				var createdID string
				cleanupFailed := true
				f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
					if req.Op == op {
						createdID = req.ID // Model a published guest whose reply was lost.
						if failure == "create-busy" {
							return &protocol.Error{Code: protocol.CodeBusy, Message: "creation still in progress"}
						}
						return errors.New("creation connection lost")
					}
					if req.Op == "remove" {
						if req.ID != createdID {
							t.Fatal("cleanup used a different identity")
						}
						if cleanupFailed {
							switch failure {
							case "busy", "create-busy":
								return &protocol.Error{Code: protocol.CodeBusy, Message: "creation still in progress"}
							case "replacement":
								return &protocol.Error{Code: protocol.CodeMachineID, Message: "another machine ID"}
							default:
								return errors.New("cleanup connection lost")
							}
						}
					}
					return nil
				}
				err := a.execute(args)
				if err == nil || strings.Contains(err.Error(), "nothing was kept") || !strings.Contains(err.Error(), "nsl remove recoverable --yes") {
					t.Fatal("misleading recovery error:", err)
				}
				m, err := a.machine("recoverable")
				if err != nil {
					t.Fatal("lost recovery record:", err)
				}
				if m.ID != createdID || m.Prepared {
					t.Fatalf("wrong recovery identity: %+v", m)
				}
				var listing bytes.Buffer
				a.out = &listing
				if err := a.list(false); err != nil || !strings.Contains(listing.String(), "incomplete") {
					t.Fatal("missing incomplete status:", err, listing.String())
				}
				if err := a.execute(args); err == nil || !strings.Contains(err.Error(), "exists") {
					t.Fatal("reused the reserved name:", err)
				}
				cleanupFailed = false
				if err := a.remove([]string{"recoverable", "--yes"}); err != nil {
					t.Fatal("cleanup retry failed:", err)
				}
				if _, err := os.Stat(a.machinePath("recoverable")); !os.IsNotExist(err) {
					t.Fatal("kept recovery record:", err)
				}
				if _, err := os.Stat(a.removingPath("recoverable")); !os.IsNotExist(err) {
					t.Fatal("kept tombstone:", err)
				}
			})
		}
	}
}

func TestFailedPreparationReleasesNameAfterConfirmedAbsence(t *testing.T) {
	a, f, _ := startedVM(t)
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "create" {
			return errors.New("creation connection lost")
		}
		if req.Op == "remove" {
			return &protocol.Error{Code: protocol.CodeUnknownMachine, Message: "no machine"}
		}
		return nil
	}
	args := append([]string{"retry"}, machineImage(t, "rootfs")...)
	if err := a.create(args); err == nil || !strings.Contains(err.Error(), "nothing was kept") {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.machinePath("retry")); !os.IsNotExist(err) {
		t.Fatal("kept absent machine:", err)
	}
	f.agent = nil
	if err := a.create(args); err != nil {
		t.Fatal("name was not freed:", err)
	}
}
