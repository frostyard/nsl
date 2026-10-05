package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

var version = "vmspawn-dev"

type runner interface {
	run(ctx context.Context, input io.Reader, output, stderr io.Writer, env []string, bin string, args ...string) error
}

type processRunner struct{}

func (processRunner) run(ctx context.Context, in io.Reader, out, stderr io.Writer, env []string, bin string, args ...string) error {
	c := exec.CommandContext(ctx, bin, args...)
	c.Env = env
	c.Stdin, c.Stdout, c.Stderr = in, out, stderr
	return c.Run()
}

type app struct {
	home, waypipe, opener, self, runtimeDir string
	groupSwitch                             string // "sg", or "newgrp" on hosts without sg
	uid, gid                                int
	user, group                             string
	r                                       runner
	in                                      io.Reader
	out, err                                io.Writer
	imageService                            *imageClient
	host                                    *hostFacts // nil reads the real host
	hostRoot                                string     // "/" outside tests; where shared trees are found
}

func newApp() (*app, error) {
	home := os.Getenv("NSL_HOME")
	if home == "" {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			u, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			data = filepath.Join(u, ".local", "share")
		}
		home = filepath.Join(data, "nsl")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	tool := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(home, "\n\r\x00:,") {
		return nil, errors.New("unsupported state path")
	}
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		return nil, err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return nil, err
	}
	group, err := user.LookupGroupId(account.Gid)
	if err != nil {
		return nil, err
	}
	return &app{home: home, waypipe: tool("NSL_WAYPIPE", "waypipe"), opener: tool("NSL_OPENER", "xdg-open"), self: self, runtimeDir: filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "nsl"),
		uid: os.Getuid(), gid: gid, user: account.Username, group: group.Name, r: processRunner{}, in: os.Stdin, out: os.Stdout, err: os.Stderr, hostRoot: "/",
		groupSwitch: groupSwitcher(exec.LookPath)}, nil
}

// groupSwitcher picks the tool that runs a command under another primary group:
// sg (shadow's, or util-linux's link to newgrp), else util-linux's newgrp -c on a
// host that installs newgrp without sg.
func groupSwitcher(lookPath func(string) (string, error)) string {
	if _, err := lookPath("sg"); err != nil {
		if _, err := lookPath("newgrp"); err == nil {
			return "newgrp"
		}
	}
	return "sg"
}

func (a *app) call(in io.Reader, out io.Writer, bin string, args ...string) error {
	return a.r.run(context.Background(), in, out, a.err, os.Environ(), bin, args...)
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `nsl — WSL-style Linux machines (experimental)

  [-m NAME]                       login shell in NAME or the default machine
  run [-m NAME] [--root] [--cd PATH] COMMAND [ARGS...]
  create NAME --distro DISTRO:RELEASE [--offline] [--default] [--user NAME]
  create NAME --image FILE --digest sha256:HEX [--default] [--user NAME]
  list [--json]
  default NAME
  start NAME
  stop NAME
  remove NAME [--yes]
  export NAME FILE
  import NAME FILE
  ports [NAME]
  ssh-config NAME
  logs [NAME]
  shutdown
  update [--offline]
  update --image FILE --digest sha256:HEX
  config [--json]
  recover [NAME]
  resize [NAME] --disk GiB
  images [--offline | --refresh] [--json]
  pull DISTRO:RELEASE [--offline]
  doctor
  version

Machines run as containers in one nsl VM. The VM starts on first use; update
selects the VM image for its next start. --json prints list, images and config
for other programs. NSL_HOME, NSL_WAYPIPE and NSL_OPENER override state and tool
locations; $XDG_CONFIG_HOME/nsl/nsl.conf holds settings.`)
}

func (a *app) execute(args []string) error {
	if len(args) == 0 || args[0] == "-m" {
		return a.run(args, true)
	}
	if args[0] == "help" || args[0] == "--help" {
		usage(a.out)
		return nil
	}
	rest := args[1:]
	noArgs := func() error {
		if len(rest) != 0 {
			return errors.New("usage: " + args[0])
		}
		return nil
	}
	switch args[0] {
	case "images", "pull":
		return a.imageCommand(args)
	case "version":
		fmt.Fprintln(a.out, version)
		return nil
	case "doctor":
		return a.doctor()
	case "config":
		asJSON, err := a.jsonOnly(args[0], rest)
		if err != nil {
			return err
		}
		return a.configCommand(asJSON)
	case "list":
		asJSON, err := a.jsonOnly(args[0], rest)
		if err != nil {
			return err
		}
		return a.list(asJSON)
	case "shutdown":
		if err := noArgs(); err != nil {
			return err
		}
		return a.shutdown()
	case "recover":
		return a.recover(rest)
	case "resize":
		return a.resize(rest)
	case "run":
		return a.run(rest, false)
	case "create":
		return a.create(rest)
	case "remove":
		return a.remove(rest)
	case "export":
		return a.export(rest)
	case "import":
		return a.importArchive(rest)
	case "start", "stop", "default":
		return a.machineCommand(args[0], rest)
	case "update":
		return a.update(rest)
	case "ports":
		return a.ports(rest)
	case "logs":
		return a.logs(rest)
	case "ssh-config":
		return a.sshConfig(rest)
	case "_ssh":
		return a.sshProxy(rest)
	case "_forward":
		return a.forward(rest)
	case "_desktop":
		return a.desktop(rest)
	case "_devices":
		return a.devices(rest)
	case "_launch":
		return a.launch(rest)
	}
	return fmt.Errorf("unknown command %s; see nsl help", args[0])
}

// jsonOnly parses the arguments of a command whose only option is --json.
func (a *app) jsonOnly(command string, args []string) (bool, error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(a.err)
	asJSON := fs.Bool("json", false, "print JSON for other programs")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if fs.NArg() != 0 {
		return false, errors.New("usage: " + command + " [--json]")
	}
	return *asJSON, nil
}

// writeJSON prints the one JSON document of a --json command (ADR-0021).
func writeJSON(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

func main() {
	a, err := newApp()
	if err == nil {
		err = a.execute(os.Args[1:])
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "nsl:", err)
		os.Exit(1)
	}
}
