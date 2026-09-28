package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var version = "vmspawn-dev"

//go:embed guest/exec.py
var guestHelper string

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

type environment struct {
	Schema       int    `json:"schema"`
	Name         string `json:"name"`
	ID           string `json:"id"`
	GuestID      string `json:"guest_id,omitempty"`
	Owner        int    `json:"owner"`
	GID          int    `json:"gid"`
	Project      string `json:"project,omitempty"`
	Desktop      bool   `json:"desktop"`
	CPUs         int    `json:"cpus"`
	Memory       int    `json:"memory"`
	Disk         int    `json:"disk_gib"`
	ResizeTarget int    `json:"resize_target_gib,omitempty"`
	Digest       string `json:"image_sha256"`
	Prepared     bool   `json:"prepared"`
	Initialized  bool   `json:"initialized"`
}

type guestRequest struct {
	Version   int      `json:"version"`
	Argv      []string `json:"argv"`
	Directory string   `json:"directory,omitempty"`
}

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
	home, waypipe, self, runtimeDir string
	uid, gid                        int
	r                               runner
	in                              io.Reader
	out, err                        io.Writer
	imageService                    *imageClient
	host                            *hostFacts // nil reads the real host
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
	if strings.ContainsAny(home, "\n\r\x00:") {
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
	return &app{home: home, waypipe: tool("NSL_WAYPIPE", "waypipe"), self: self, runtimeDir: filepath.Join("/run/user", fmt.Sprint(os.Getuid()), "nsl"), uid: os.Getuid(), gid: gid, r: processRunner{}, in: os.Stdin, out: os.Stdout, err: os.Stderr}, nil
}

func checkName(name string) error {
	if !validName.MatchString(name) || strings.HasSuffix(name, "-") {
		return errors.New("name must be 1–24 lowercase letters, digits or interior hyphens, starting with a letter")
	}
	return nil
}

func (a *app) call(in io.Reader, out io.Writer, bin string, args ...string) error {
	return a.r.run(context.Background(), in, out, a.err, os.Environ(), bin, args...)
}
func (a *app) dir(name string) string { return filepath.Join(a.home, "environments", name) }

func projectPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !st.IsDir() || abs == "/" || strings.ContainsAny(abs, "\n\r\x00:") || strings.Contains(abs, "{{") {
		return "", errors.New("project must be an existing directory other than /")
	}
	return abs, nil
}

func encodeRequest(args []string, dir string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("expected command")
	}
	if dir != "" && (!filepath.IsAbs(dir) || strings.ContainsRune(dir, 0)) {
		return "", errors.New("guest working directory must be absolute")
	}
	for _, s := range args {
		if strings.ContainsRune(s, 0) {
			return "", errors.New("NUL in argument")
		}
	}
	b, err := json.Marshal(guestRequest{Version: 1, Argv: args, Directory: dir})
	if err != nil {
		return "", err
	}
	if len(b) > 64000 {
		return "", errors.New("command request too large")
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
func (a *app) sshArgs(e *environment, tty bool) []string {
	mode := "-T"
	if tty {
		mode = "-tt"
	}
	return []string{"-F", filepath.Join(a.dir(e.Name), "ssh.config"), mode, "guest"}
}
func (a *app) executeGuest(e *environment, args []string, root, tty, gui bool, dir string) error {
	if gui && !e.Desktop {
		return errors.New("environment was created without --desktop")
	}
	payload, err := encodeRequest(args, dir)
	if err != nil {
		return err
	}
	if gui && (os.Getenv("WAYLAND_DISPLAY") == "" || os.Getenv("XDG_RUNTIME_DIR") == "") {
		return errors.New("an active Wayland session is required")
	}
	if err = a.start(e); err != nil {
		return err
	}
	ssh := a.sshArgs(e, tty)
	remote := []string{"/usr/local/libexec/nsl-exec", payload}
	if root {
		remote = append([]string{"sudo", "-n", "--"}, remote...)
	}
	if gui {
		if os.Getenv("WAYLAND_DISPLAY") == "" || os.Getenv("XDG_RUNTIME_DIR") == "" {
			return errors.New("an active Wayland session is required")
		}
		wp := []string{"--no-gpu", "--title-prefix=nsl " + e.Name + ": ", "ssh"}
		wp = append(wp, ssh...)
		wp = append(wp, remote...)
		return a.call(a.in, a.out, a.waypipe, wp...)
	}
	return a.call(a.in, a.out, "ssh", append(ssh, remote...)...)
}
func (a *app) list() error {
	if err := a.init(); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(a.home, "environments"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		e, err := a.owned(entry.Name())
		if err != nil {
			return err
		}
		status, err := a.status(e)
		if err != nil {
			status = "Incomplete"
		}
		fmt.Fprintf(a.out, "%s\t%s\t%s\n", e.Name, status, e.Project)
	}
	return a.listRemoving()
}
func usage(w io.Writer) {
	fmt.Fprintln(w, `nsl — persistent development VMs (experimental)

  create NAME --image FILE --digest sha256:HEX [--project DIR] [--desktop]
              [--cpus 2] [--memory 2] [--disk 16]
  create NAME --distro DISTRO:RELEASE [--offline] [same resource/project flags]
  images [--offline]
  pull DISTRO:RELEASE [--offline]
  list
  start NAME
  shell NAME
  exec NAME [--root] [--tty] [--workdir /path] -- COMMAND [ARGS...]
  gui NAME -- COMMAND [ARGS...]
  stop NAME
  recover NAME
  export NAME FILE.nsl
  restore NAME FILE.nsl [--project DIR] [--desktop]
  resize NAME --disk GiB
  remove NAME [--yes]
  ports NAME
  logs NAME
  ssh-config NAME
  config
  doctor
  version

Commands start stopped VMs automatically. Project shares are set at creation.
VMs persist after commands exit. GUI forwarding currently uses software rendering.
NSL_HOME and NSL_WAYPIPE override state/tool locations.`)
}
func (a *app) execute(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		usage(a.out)
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
		if len(args) != 1 {
			return errors.New("usage: config")
		}
		return a.configCommand()
	case "_devices":
		return a.devices(args[1:])
	case "list":
		if len(args) != 1 {
			return errors.New("usage: list")
		}
		return a.list()
	}
	if len(args) < 2 {
		return errors.New("expected environment name")
	}
	name := args[1]
	if args[0] == "create" {
		return a.create(name, args[2:])
	}
	if args[0] == "restore" {
		return a.restore(name, args[2:])
	}
	if args[0] == "remove" {
		return a.remove(name, args[2:])
	}
	if args[0] == "resize" {
		return a.resize(name, args[2:])
	}
	if args[0] == "export" {
		if len(args) != 3 {
			return errors.New("usage: export NAME FILE.nsl")
		}
		return a.export(name, args[2])
	}
	if !strings.Contains("|start|stop|recover|ports|logs|shell|exec|gui|ssh-config|_launch|_forward|", "|"+args[0]+"|") {
		return fmt.Errorf("unknown command %s", args[0])
	}
	e, err := a.owned(name)
	if err != nil {
		return err
	}
	switch args[0] {
	case "exec":
		fs := flag.NewFlagSet("exec", flag.ContinueOnError)
		fs.SetOutput(a.err)
		root := fs.Bool("root", false, "")
		tty := fs.Bool("tty", false, "")
		dir := fs.String("workdir", "", "")
		if err = fs.Parse(args[2:]); err != nil {
			return err
		}
		return a.executeGuest(e, fs.Args(), *root, *tty, false, *dir)
	case "gui":
		if len(args) < 4 || args[2] != "--" {
			return errors.New("usage: gui NAME -- COMMAND")
		}
		return a.executeGuest(e, args[3:], false, false, true, "")
	}
	if len(args) != 2 {
		return errors.New("unexpected arguments")
	}
	switch args[0] {
	case "start":
		return a.start(e)
	case "stop":
		return a.stop(e)
	case "recover":
		return a.recover(e)
	case "ports":
		return a.ports(e)
	case "logs":
		return a.call(nil, a.out, "journalctl", "--user", "--no-pager", "-n", "100", "-u", unit(e), "-u", portUnit(e))
	case "_launch":
		return a.launch(e)
	case "_forward":
		return a.forward(e)
	case "shell":
		return a.executeGuest(e, []string{"/bin/bash", "-l"}, false, true, false, "")
	case "ssh-config":
		if err = a.start(e); err != nil {
			return err
		}
		fmt.Fprintln(a.out, filepath.Join(a.dir(e.Name), "ssh.config"))
		return nil
	}
	return nil
}
func main() {
	a, err := newApp()
	if err == nil {
		err = a.execute(os.Args[1:])
	}
	if err != nil {
		if code, ok := err.(*exec.ExitError); ok {
			os.Exit(code.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "nsl:", err)
		os.Exit(1)
	}
}
