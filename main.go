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

	"github.com/frostyard/std/problems"
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
	host                                    *hostFacts         // nil reads the real host
	hostRoot                                string             // "/" outside tests; where shared trees are found
	dataHome                                string             // the user's XDG data directory; menu entries live there
	problems                                *problems.Recorder // local journal records of panics and internal errors; nil records nothing
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
	// XDG_DATA_HOME must be an absolute path; when it is unset, empty or
	// relative, the desktop specification reads the user's own data directory.
	dataHome := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(dataHome) {
		u, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dataHome = filepath.Join(u, ".local", "share")
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
		uid: os.Getuid(), gid: gid, user: account.Username, group: group.Name, r: processRunner{}, in: os.Stdin, out: os.Stdout, err: os.Stderr, hostRoot: "/", dataHome: dataHome,
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
	err := a.r.run(context.Background(), in, out, a.err, os.Environ(), bin, args...)
	// nsl run and nsl shell return the machine command's own status through
	// ssh, so their error stays bare. A user systemd call is nsl's own work:
	// its failure needs a message, and a shell that cannot reach the user
	// manager needs the hint.
	if err != nil && isUserSystemd(bin, args) {
		if hint := userSystemdHint(); hint != "" {
			return fmt.Errorf("%s: %w; %s", bin, err, hint)
		}
		return fmt.Errorf("%s: %w", bin, err)
	}
	return err
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `nsl — WSL-style Linux machines (experimental)

  [-m NAME]                       login shell in NAME or the default machine
  run [-m NAME] [--root] [--cd PATH] [--host-files CODE:BEFORE:AFTER] COMMAND [ARGS...]
  create NAME --distro DISTRO:RELEASE [--offline] [--isolated] [--default] [--user NAME]
  create NAME --image FILE --digest sha256:HEX [--isolated] [--default] [--user NAME]
  list [--json]
  default NAME
  start NAME
  stop NAME
  remove NAME [--yes]
  export NAME FILE
  import NAME FILE [--isolated]
  ports [NAME]
  ssh-config NAME
  logs [NAME]
  app add [-m NAME] [--mime-types] APP
  app remove [-m NAME] APP
  app list [-m NAME]
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
	case "app":
		return a.appCommand(rest)
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
	args := os.Args[1:]
	if code := run(problems.New(appID, version, component(args)), args, newApp); code != 0 {
		os.Exit(code)
	}
}

// run executes one nsl command and returns its exit status. The recorder
// writes a panic, and at the next start a crash, to the local journal
// (ADR-0023); a panic still ends the process with status 2.
func run(rec *problems.Recorder, args []string, newApp func() (*app, error)) int {
	// A dev build or a host with no state folder records nothing, silently.
	_ = rec.CrashOutput()
	defer rec.RecoverPanic()
	a, err := newApp()
	if err == nil {
		a.problems = rec
		err = a.execute(args)
	}
	if err != nil {
		return exitStatus(err, os.Stderr)
	}
	return 0
}

// exitStatus turns a command's error into nsl's exit status. A bare
// *exec.ExitError is a machine command's own status, and the machine wrote
// its own stderr, so nsl passes it through silently. A wrapped one is nsl's
// own failure, and its message must show.
func exitStatus(err error, stderr io.Writer) int {
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	fmt.Fprintln(stderr, "nsl:", err)
	return 1
}
