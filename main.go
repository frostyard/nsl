package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var version = "dev"

const base = "nsl-base-debian-13"
const label = "nsl.owner"

type machine struct {
	Name    string            `json:"name"`
	State   string            `json:"state"`
	Mode    string            `json:"mode"`
	Origin  string            `json:"origin"`
	Labels  map[string]string `json:"labels"`
	Volumes []string          `json:"volumes"`
}

type runner interface {
	run(capture bool, args ...string) (string, error)
}
type processRunner struct{}

func (processRunner) run(capture bool, args ...string) (string, error) {
	bin := os.Getenv("NSL_NSPAWN")
	if bin == "" {
		bin = "nspawn"
	}
	cmdArgs := args
	if os.Geteuid() != 0 {
		cmdArgs = append([]string{"-n", bin}, args...)
		bin = "sudo"
	}
	cmd := exec.Command(bin, cmdArgs...)
	cmd.Stdin = os.Stdin
	if !capture {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = os.Stderr
	if capture {
		out, err := cmd.Output()
		return string(out), err
	}
	return "", cmd.Run()
}

type app struct {
	r                  runner
	uid, gid, username string
	out                io.Writer
}

func currentApp(r runner, out io.Writer) (*app, error) {
	u, err := user.Current()
	if err != nil {
		return nil, err
	}
	if u.Uid == "0" {
		if sudoUID := os.Getenv("SUDO_UID"); sudoUID != "" {
			u, err = user.LookupId(sudoUID)
			if err != nil {
				return nil, err
			}
		}
	}
	if !regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`).MatchString(u.Username) || strings.HasPrefix(u.Username, "-") {
		return nil, fmt.Errorf("unsupported host username %q", u.Username)
	}
	return &app{r: r, uid: u.Uid, gid: u.Gid, username: u.Username, out: out}, nil
}

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

func machineName(name string) (string, error) {
	if !validName.MatchString(name) || strings.HasSuffix(name, "-") {
		return "", fmt.Errorf("invalid environment name %q: use lowercase letters, digits and interior hyphens", name)
	}
	return "nsl-" + name, nil
}
func (a *app) inspect(name string) (*machine, error) {
	out, err := a.r.run(true, "inspect", name)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", name, err)
	}
	var list []machine
	if err = json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 || list[0].Name != name {
		return nil, fmt.Errorf("invalid inspect response for %s: %v", name, err)
	}
	return &list[0], nil
}
func (a *app) owned(name string) (*machine, error) {
	n, err := a.inspect(name)
	if err != nil {
		return nil, err
	}
	if n.Labels[label] != a.uid || n.Origin != "create" || n.Mode != "boot" {
		return nil, fmt.Errorf("%s is not an nsl environment owned by UID %s", name, a.uid)
	}
	return n, nil
}
func (a *app) nspawn(args ...string) error { _, err := a.r.run(false, args...); return err }
func (a *app) start(n *machine, volume string) error {
	want := []string{}
	if volume != "" {
		want = []string{volume}
	}
	if n.State == "running" {
		if !slices.Equal(n.Volumes, want) {
			return fmt.Errorf("%s is running with different mounts %v; run 'nsl stop %s' before switching context", n.Name, n.Volumes, strings.TrimPrefix(n.Name, "nsl-"))
		}
		return nil
	}
	if n.State != "stopped" {
		return fmt.Errorf("%s is %s; stop it first", n.Name, n.State)
	}
	// nspawn rejects 'none' and a mount in the same start invocation.
	// Clear a saved mount in a separate short boot before attaching a new one.
	if len(n.Volumes) != 0 && !slices.Equal(n.Volumes, want) {
		if err := a.nspawn("start", n.Name, "-v", "none"); err != nil {
			return err
		}
		if volume == "" {
			return nil
		}
		if err := a.nspawn("stop", n.Name); err != nil {
			return err
		}
	}
	if volume == "" {
		return a.nspawn("start", n.Name, "-v", "none")
	}
	return a.nspawn("start", n.Name, "-v", volume)
}
func (a *app) guest(n *machine, root, detach bool, dir string, env []string, command []string) error {
	args := []string{"exec", n.Name}
	if !root {
		args = append(args, "-u", a.uid)
	}
	if detach {
		args = append(args, "-d")
	}
	if dir != "" {
		args = append(args, "-w", dir)
	}
	if !root {
		env = append(env, "HOME=/home/"+a.username, "USER="+a.username, "LOGNAME="+a.username)
	}
	// Avoid a pseudo-terminal for noninteractive pipelines; nspawn still passes stdin.
	if len(command) == 0 {
		command = []string{"/bin/sh"}
		args = append(args, "-t")
	}
	// nspawn exec -e does not override HOME set by its user lookup; env does.
	if len(env) != 0 {
		command = append(append([]string{"env", "--"}, env...), command...)
	}
	args = append(args, command...)
	return a.nspawn(args...)
}
func (a *app) newEnv(name string) error {
	full, err := machineName(name)
	if err != nil {
		return err
	}
	// Never claim/replace any preexisting machine, even one without our label.
	out, err := a.r.run(true, "images", "ls", "--json")
	if err != nil {
		return err
	}
	var images []struct {
		Name string `json:"name"`
	}
	if err = json.Unmarshal([]byte(out), &images); err != nil {
		return err
	}
	for _, im := range images {
		if im.Name == full {
			return fmt.Errorf("machine %s already exists", full)
		}
	}
	// Pull to a private cache name. Do not trust a preexisting image with that name.
	found := false
	for _, im := range images {
		if im.Name == base {
			found = true
		}
	}
	if !found {
		if err = a.nspawn("pull", "debian:13", "--name", base); err != nil {
			return err
		}
	}
	// A user-defined image at the cache name must still be signed and reference the right hub image.
	info, err := a.r.run(true, "images", "ls", "--json")
	if err != nil {
		return err
	}
	var sources []struct {
		Name      string `json:"name"`
		Reference string `json:"reference"`
		SignedBy  string `json:"signed_by"`
	}
	if err = json.Unmarshal([]byte(info), &sources); err != nil {
		return err
	}
	trusted := false
	for _, s := range sources {
		if s.Name == base && s.Reference == "hub.nspawn.org/debian:13" && s.SignedBy != "" {
			trusted = true
		}
	}
	if !trusted {
		return fmt.Errorf("%s is not a signed Debian 13 hub image", base)
	}
	if err = a.nspawn("create", base, full, "-l", label+"="+a.uid); err != nil {
		return err
	}
	n, err := a.owned(full)
	if err != nil {
		return err
	}
	if err = a.start(n, ""); err != nil {
		return err
	}
	// Group name uses numeric UID to avoid collisions with image-provided groups.
	if err = a.guest(n, true, false, "", nil, []string{"groupadd", "-g", a.gid, "nsluser"}); err != nil {
		return fmt.Errorf("group bootstrap: %w", err)
	}
	if err = a.guest(n, true, false, "", nil, []string{"useradd", "-m", "-u", a.uid, "-g", a.gid, "-s", "/bin/sh", a.username}); err != nil {
		return fmt.Errorf("user bootstrap: %w", err)
	}
	if err = a.guest(n, true, false, "", nil, []string{"install", "-d", "-m", "0700", "-o", a.uid, "-g", a.gid, "/home/" + a.username + "/.nsl-runtime", "/home/" + a.username + "/.config"}); err != nil {
		return fmt.Errorf("GUI runtime bootstrap: %w", err)
	}
	if err = a.guest(n, true, false, "", nil, []string{"touch", "/etc/nsl-ready"}); err != nil {
		return fmt.Errorf("bootstrap marker: %w", err)
	}
	fmt.Fprintf(a.out, "Created %s (Debian 13); use nsl enter %s\n", name, name)
	return nil
}
func projectVolume(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s is not a directory", p)
	}
	if strings.ContainsAny(p, ":\n\r\t ") {
		return "", errors.New("project path cannot contain colons or whitespace (nspawn volume limitation)")
	}
	if p == "/" {
		return "", errors.New("refusing to mount the host filesystem root")
	}
	return p + ":/work", nil
}
func waylandVolume(username string) (string, []string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	display := os.Getenv("WAYLAND_DISPLAY")
	if runtime == "" || display == "" || strings.Contains(display, "/") {
		return "", nil, errors.New("active Wayland session required (XDG_RUNTIME_DIR and WAYLAND_DISPLAY)")
	}
	uid := strconv.Itoa(os.Getuid())
	if runtime != "/run/user/"+uid {
		return "", nil, fmt.Errorf("unexpected runtime dir %q", runtime)
	}
	sock := filepath.Join(runtime, display)
	st, err := os.Lstat(sock)
	if err != nil {
		return "", nil, err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return "", nil, fmt.Errorf("%s is not a socket", sock)
	}
	if strings.ContainsAny(sock, ": \n\t") {
		return "", nil, errors.New("unsupported Wayland socket path")
	}
	guestRuntime := "/home/" + username + "/.nsl-runtime"
	return sock + ":" + guestRuntime + "/wayland-0", []string{"XDG_RUNTIME_DIR=" + guestRuntime, "WAYLAND_DISPLAY=wayland-0", "GDK_BACKEND=wayland"}, nil
}

func (a *app) execute(args []string) error {
	if len(args) == 0 {
		usage(a.out)
		return errors.New("missing command")
	}
	if args[0] == "version" {
		fmt.Fprintln(a.out, "nsl "+version)
		return nil
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(a.out)
		return nil
	}
	if args[0] == "ls" {
		if len(args) != 1 {
			return errors.New("usage: nsl ls")
		}
		out, err := a.r.run(true, "ps", "-a", "--json")
		if err != nil {
			return err
		}
		var list []machine
		if err = json.Unmarshal([]byte(out), &list); err != nil {
			return err
		}
		for _, n := range list {
			if strings.HasPrefix(n.Name, "nsl-") && n.Labels[label] == a.uid && n.Origin == "create" {
				fmt.Fprintf(a.out, "%s\t%s\t%v\n", strings.TrimPrefix(n.Name, "nsl-"), n.State, n.Volumes)
			}
		}
		return nil
	}
	if len(args) < 2 {
		return errors.New("expected environment name")
	}
	name, err := machineName(args[1])
	if err != nil {
		return err
	}
	if args[0] == "new" {
		if len(args) != 2 {
			return errors.New("usage: nsl new NAME")
		}
		return a.newEnv(args[1])
	}
	if !slices.Contains([]string{"stop", "enter", "run", "in", "gui"}, args[0]) {
		return fmt.Errorf("unknown command %q", args[0])
	}
	var vol, dir string
	var env []string
	var cmd []string
	root := false
	detach := false
	switch args[0] {
	case "stop", "enter":
		if len(args) != 2 {
			return fmt.Errorf("usage: nsl %s NAME", args[0])
		}
	case "run":
		rest := args[2:]
		if len(rest) > 0 && rest[0] == "--root" {
			root = true
			rest = rest[1:]
		}
		if len(rest) < 2 || rest[0] != "--" {
			return errors.New("usage: nsl run NAME [--root] -- COMMAND [ARGS...]")
		}
		cmd = rest[1:]
	case "in":
		if len(args) < 3 {
			return errors.New("usage: nsl in NAME PROJECT [-- COMMAND [ARGS...]]")
		}
		if len(args) > 3 && (args[3] != "--" || len(args) == 4) {
			return errors.New("expected -- COMMAND")
		}
		vol, err = projectVolume(args[2])
		if err != nil {
			return err
		}
		dir = "/work"
		if len(args) > 3 {
			cmd = args[4:]
		}
	case "gui":
		if len(args) < 4 || args[2] != "--" {
			return errors.New("usage: nsl gui NAME -- COMMAND [ARGS...]")
		}
		vol, env, err = waylandVolume(a.username)
		if err != nil {
			return err
		}
		cmd = args[3:]
		env = append(env, "XDG_CONFIG_HOME=/home/"+a.username+"/.config")
	}
	n, err := a.owned(name)
	if err != nil {
		return err
	}
	if args[0] != "stop" {
		// A failed new must not silently give the user a half-configured shell.
		wasStopped := n.State != "running"
		if wasStopped {
			if err = a.start(n, ""); err != nil {
				return err
			}
		}
		if err = a.guest(n, true, false, "", nil, []string{"test", "-f", "/etc/nsl-ready"}); err != nil {
			return fmt.Errorf("%s bootstrap incomplete; inspect machine and repair or remove it manually", name)
		}
		if wasStopped && (args[0] == "in" || args[0] == "gui") {
			if err = a.nspawn("stop", name); err != nil {
				return err
			}
		}
		// Re-inspect because start above changed state and mount eligibility.
		n, err = a.owned(name)
		if err != nil {
			return err
		}
	}
	switch args[0] {
	case "stop":
		if len(args) != 2 {
			return errors.New("usage: nsl stop NAME")
		}
		if n.State == "running" {
			if err = a.nspawn("stop", name); err != nil {
				return err
			}
		}
		if len(n.Volumes) == 0 {
			return nil
		}
		// Clear remembered mounts; nspawn only accepts mount updates on start.
		if err = a.nspawn("start", name, "-v", "none"); err != nil {
			return fmt.Errorf("stopped but could not clear saved mounts: %w", err)
		}
		return a.nspawn("stop", name)
	case "enter", "run", "in", "gui":
		if err = a.start(n, vol); err != nil {
			return err
		}
		return a.guest(n, root, detach, dir, env, cmd)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func usage(w io.Writer) {
	fmt.Fprintf(w, "nsl %s — NSpawn Subsystem for Linux\n", version)
	fmt.Fprint(w, `
Usage:
  nsl new NAME                         Create a Debian 13 environment
  nsl ls                               List your environments
  nsl enter NAME                       Enter as your host UID
  nsl run NAME [--root] -- CMD [ARGS]  Run command (root only when explicit)
  nsl in NAME PROJECT [-- CMD [ARGS]]  Mount one project at /work and work there
  nsl gui NAME -- CMD [ARGS]           Launch app on current Wayland session
  nsl stop NAME                        Stop and clear temporary mounts
  nsl version
Switching mounts while a machine runs requires nsl stop NAME first.
`)
}
func main() {
	a, err := currentApp(processRunner{}, os.Stdout)
	if err == nil {
		err = a.execute(os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nsl:", err)
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			os.Exit(exited.ExitCode())
		}
		os.Exit(1)
	}
}
