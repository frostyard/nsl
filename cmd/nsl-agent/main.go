// Command nsl-agent runs in every nsl VM. As the forced SSH command it answers
// one request from the host (docs/specs/agent.md); as a boot service it
// prepares the data disk, identity and machines, and it stops idle machines
// and the idle VM (docs/specs/vm-image.md).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/frostyard/nsl/internal/protocol"
)

type runner interface {
	run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, argv ...string) error
}

type processRunner struct{}

func (processRunner) run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, argv ...string) error {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	return c.Run()
}

type agent struct {
	root                  string // filesystem prefix; "/" outside tests
	sys                   system
	r                     runner
	stdin, stdout, stderr *os.File
	now                   func() float64
}

func (a *agent) path(p string) string {
	if a.root == "/" {
		return p
	}
	return a.root + p
}

func main() {
	a := &agent{root: "/", r: processRunner{}, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, now: monotonic}
	// Cleanup must finish even when the SSH session goes away, and writes to a
	// closed session must fail rather than kill the agent.
	signal.Ignore(syscall.SIGPIPE)
	var err error
	switch len(os.Args) {
	case 1:
		os.Exit(a.serve(os.Getenv("SSH_ORIGINAL_COMMAND")))
	case 2:
		switch os.Args[1] {
		case "storage":
			err = a.storage()
		case "setup":
			err = a.setup()
		case "boot":
			err = a.boot()
		case "idle":
			err = a.idle()
		default:
			err = errors.New("usage: nsl-agent [storage|setup|boot|idle]")
		}
	default:
		err = errors.New("usage: nsl-agent [storage|setup|boot|idle]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nsl-agent:", err)
		os.Exit(1)
	}
}

// serve answers one request and returns the SSH exit status.
func (a *agent) serve(command string) int {
	status, err := a.handle(command)
	if err != nil {
		var e *protocol.Error
		if !errors.As(err, &e) {
			e = &protocol.Error{Code: protocol.CodeFailed, Message: err.Error()}
		}
		fmt.Fprintln(a.stderr, e.Error())
		return protocol.ErrorExit
	}
	return status
}

func (a *agent) handle(command string) (int, error) {
	if command == "" {
		return 0, &protocol.Error{Code: protocol.CodeBadRequest, Message: "no request; interactive login is not available"}
	}
	req, err := protocol.Decode(command)
	if err != nil {
		return 0, err
	}
	binding, err := a.binding()
	if err != nil {
		return 0, err
	}
	if req.Machine != "" && binding.Machine != nil && binding.Machine.Name != req.Machine {
		return 0, &protocol.Error{Code: protocol.CodeRefused, Message: "this isolated VM hosts only " + binding.Machine.Name}
	}
	if (req.Op == "create" || req.Op == "import") && binding.Machine != nil && binding.Machine.ID != req.ID {
		return 0, &protocol.Error{Code: protocol.CodeRefused, Message: "this isolated VM hosts another machine ID"}
	}
	if !protocol.Passive[req.Op] {
		held, err := a.holdRequest()
		if err != nil {
			return 0, err
		}
		defer held.Close()
	}
	if a.sys == nil && req.Op != "identity" && req.Op != "vm" {
		if a.sys, err = newSystem(); err != nil {
			return 0, err
		}
	}
	switch req.Op {
	case "identity":
		return 0, a.identity(binding)
	case "vm":
		return a.vm(req.Argv)
	case "machines":
		return 0, a.machines()
	case "start":
		return 0, a.start(req, binding)
	case "stop":
		return 0, a.stop(req)
	case "run":
		return a.runCommand(req)
	case "create":
		return 0, a.create(req, binding)
	case "import":
		return 0, a.importMachine(req, binding)
	case "export":
		return 0, a.export(req)
	case "remove":
		return 0, a.remove(req)
	case "listeners":
		return 0, a.listeners()
	case "display":
		return 0, a.display(req, binding)
	case "ssh":
		return a.sshd(req)
	}
	return 0, &protocol.Error{Code: protocol.CodeBadRequest, Message: "unknown operation " + req.Op}
}

// vm runs argv as VM root, for diagnostics and acceptance probes.
func (a *agent) vm(argv []string) (int, error) {
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = a.stdin, a.stdout, a.stderr
	err := c.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 0, &protocol.Error{Code: protocol.CodeFailed, Message: err.Error()}
	}
	return 0, nil
}
