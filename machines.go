package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
	"golang.org/x/sys/unix"
)

// machineRecord is the host's record of one machine.
type machineRecord struct {
	Schema   int    `json:"schema"`
	Name     string `json:"name"`
	ID       string `json:"id"`
	Tier     string `json:"tier"`
	User     string `json:"user"`
	Group    string `json:"group"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	Image    string `json:"image,omitempty"` // the machine image's digest; empty when imported
	BuildID  string `json:"build_id,omitempty"`
	Prepared bool   `json:"prepared"`
	Created  string `json:"created"`
}

func (a *app) machinesDir() string            { return filepath.Join(a.home, "machines") }
func (a *app) machinePath(name string) string { return filepath.Join(a.machinesDir(), name+".json") }
func (a *app) removingPath(name string) string {
	return filepath.Join(a.home, "removing", name+".json")
}

func (a *app) readMachine(path, name string) (*machineRecord, error) {
	if err := privateFile(path, a.uid, 0077); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m machineRecord
	if err = protocol.DecodeStrict(b, &m); err != nil {
		return nil, fmt.Errorf("machine record %s: %w", name, err)
	}
	if m.Schema != 1 || m.Name != name || !protocol.ValidID(m.ID) || (m.Tier != "shared" && m.Tier != "isolated") || !protocol.ValidAccount(m.User) ||
		!protocol.ValidAccount(m.Group) || m.UID != a.uid || m.GID != a.gid || (m.Image != "" && !protocol.ValidDigest("sha256:"+m.Image)) ||
		(m.BuildID != "" && !protocol.ValidBuildID(m.BuildID)) {
		return nil, fmt.Errorf("unsupported machine record or identity mismatch: %s", name)
	}
	return &m, nil
}

// machine loads an owned machine's record.
func (a *app) machine(name string) (*machineRecord, error) {
	if !protocol.ValidName(name) {
		return nil, errors.New("machine names are 1–24 lowercase letters, digits or interior hyphens, starting with a letter")
	}
	if err := a.initMachines(); err != nil {
		return nil, err
	}
	m, err := a.readMachine(a.machinePath(name), name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no machine named %s; see nsl list", name)
	}
	return m, err
}

func (a *app) initMachines() error {
	if err := a.init(); err != nil {
		return err
	}
	for _, p := range []string{a.machinesDir(), filepath.Join(a.home, "removing")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return err
		}
		if err := checkPrivateDir(p, a.uid); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) saveMachine(m *machineRecord) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(a.machinePath(m.Name), append(b, '\n'), 0600)
}

func (a *app) machineNames() ([]string, error) {
	if err := a.initMachines(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(a.machinesDir())
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && protocol.ValidName(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// lockMachine serializes lifecycle changes and rejects a replaced machine.
func (a *app) lockMachine(expected *machineRecord) (*os.File, *machineRecord, error) {
	l, err := fileLock(filepath.Join(a.machinesDir(), "."+expected.Name+".lock"))
	if err != nil {
		return nil, nil, err
	}
	m, err := a.machine(expected.Name)
	if err == nil && m.ID != expected.ID {
		err = errors.New(expected.Name + " was replaced while waiting; retry")
	}
	if err != nil {
		unlock(l)
		return nil, nil, err
	}
	return l, m, nil
}

func (a *app) defaultPath() string { return filepath.Join(a.home, "default") }

func (a *app) defaultMachine() (string, error) {
	b, err := os.ReadFile(a.defaultPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err = privateFile(a.defaultPath(), a.uid, 0077); err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(b))
	if !protocol.ValidName(name) {
		return "", errors.New("invalid default machine record")
	}
	return name, nil
}

func (a *app) setDefault(name string) error {
	if name == "" {
		err := os.Remove(a.defaultPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return atomicWrite(a.defaultPath(), []byte(name+"\n"), 0600)
}

func (a *app) idleTimeout() (*int, error) {
	c, err := a.loadConfig()
	if err != nil {
		return nil, err
	}
	n := c.idleTimeout.value
	return &n, nil
}

// create prepares a machine from a verified catalogue selection or a local
// machine image.
func (a *app) create(args []string) error {
	usage := errors.New("usage: create NAME --distro DISTRO:RELEASE [--offline] [--default] [--user NAME] | create NAME --image FILE --digest sha256:HEX [--default] [--user NAME]")
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usage
	}
	name := args[0]
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(a.err)
	image := fs.String("image", "", "local machine image (rootfs.tar.zst)")
	digest := fs.String("digest", "", "sha256:HEX of the local image")
	distro := fs.String("distro", "", "verified catalogue selection DISTRO:RELEASE")
	offline := fs.Bool("offline", false, "use only verified cached data")
	isolated := fs.Bool("isolated", false, "run the machine in its own VM without host access")
	makeDefault := fs.Bool("default", false, "make this the default machine")
	account := fs.String("user", a.user, "guest account name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch {
	case fs.NArg() != 0:
		return errors.New("unexpected arguments after the options")
	case !protocol.ValidName(name):
		return errors.New("machine names are 1–24 lowercase letters, digits or interior hyphens, starting with a letter")
	case (*distro == "") == (*image == "") || (*image == "") != (*digest == "") || (*offline && *distro == ""):
		return usage
	case *image != "" && !digestPattern.MatchString(*digest):
		return errors.New("--digest must be sha256: followed by 64 lowercase hex digits")
	case !protocol.ValidAccount(*account):
		return errors.New("--user must be a lowercase POSIX account name of at most 32 characters")
	case runtime.GOARCH != "amd64":
		return errors.New("machines are x86-64 only")
	}
	if err := a.initMachines(); err != nil {
		return err
	}
	var cached *cachedImage
	if *distro != "" {
		var err error
		if cached, err = a.imageClient().pullMachine(*distro, *offline); err != nil {
			return err
		}
		if err = a.ensureVMImage(*offline); err != nil {
			return err
		}
	} else {
		hexDigest := strings.TrimPrefix(*digest, "sha256:")
		path := filepath.Join(a.machineImages(), hexDigest+".tar.zst")
		if err := a.importFile(*image, path, hexDigest); err != nil {
			return err
		}
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		// A local image's build ID is whatever its descriptor says.
		cached = &cachedImage{path: path, ref: blobRef{Digest: *digest, Size: st.Size()}}
	}
	hexDigest := strings.TrimPrefix(cached.ref.Digest, "sha256:")
	m := &machineRecord{Schema: 1, Name: name, ID: randomID(), Tier: tier(*isolated), User: *account, Group: a.group, UID: a.uid, GID: a.gid,
		Image: hexDigest, Created: time.Now().UTC().Format(time.RFC3339)}
	request := &protocol.Image{Path: protocol.ImageShare + "/" + hexDigest + ".tar.zst", Digest: cached.ref.Digest, Size: cached.ref.Size, BuildID: cached.buildID}
	if err := a.newMachine(m, protocol.Request{Op: "create", Image: request}, nil, 20*time.Minute, *makeDefault); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Created %s from %s\n", name, m.BuildID)
	return nil
}

func tier(isolated bool) string {
	if isolated {
		return "isolated"
	}
	return "shared"
}

// newMachine reserves the name with an unprepared record, has the agent build
// the machine, and makes it the default when there is none or when asked.
func (a *app) newMachine(m *machineRecord, req protocol.Request, stdin io.Reader, timeout time.Duration, makeDefault bool) error {
	manager, err := fileLock(filepath.Join(a.home, "lock"))
	if err != nil {
		return err
	}
	for _, p := range []string{a.machinePath(m.Name), a.removingPath(m.Name)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			unlock(manager)
			return fmt.Errorf("%s exists or its removal is incomplete; see nsl list", m.Name)
		}
	}
	// The unprepared record reserves the name; remove cleans it up if we stop here.
	err = a.saveMachine(m)
	unlock(manager)
	if err != nil {
		return err
	}
	l, locked, err := a.lockMachine(m)
	if err != nil {
		return err
	}
	defer unlock(l)
	*m = *locked
	if err = a.prepareMachine(m, req, stdin, timeout); err != nil {
		if _, recordErr := os.Lstat(a.machinePath(m.Name)); !errors.Is(recordErr, os.ErrNotExist) {
			return fmt.Errorf("creating %s failed; its record is retained; run nsl remove %s --yes to clean up: %w", m.Name, m.Name, err)
		}
		return fmt.Errorf("creating %s failed; nothing was kept: %w", m.Name, err)
	}
	current, err := a.defaultMachine()
	if err != nil {
		return err
	}
	if current == "" || makeDefault {
		return a.setDefault(m.Name)
	}
	return nil
}

// prepareMachine has the agent build the machine. On failure it removes the
// guest machine or isolated VM before discarding the host recovery record.
// An unconfirmed cleanup retains the identity so remove can retry safely.
func (a *app) prepareMachine(m *machineRecord, req protocol.Request, stdin io.Reader, timeout time.Duration) error {
	v, err := a.machineVM(m, false)
	if err == nil {
		req.Machine, req.ID, req.TimeZone = m.Name, m.ID, hostZone()
		req.Account = &protocol.Account{User: m.User, Group: m.Group, UID: m.UID, GID: m.GID}
		var result protocol.CreateResult
		if err = a.agentJSON(v, req, stdin, timeout, &result); err == nil {
			m.BuildID, m.Prepared = result.BuildID, true
			return a.saveMachine(m)
		}
		if m.Tier == "shared" {
			cleanupErr := a.agentJSON(v, protocol.Request{Op: "remove", Machine: m.Name, ID: m.ID}, nil, time.Minute, nil)
			var agentErr *protocol.Error
			if cleanupErr != nil && (!errors.As(cleanupErr, &agentErr) || agentErr.Code != protocol.CodeUnknownMachine) {
				return errors.Join(err, fmt.Errorf("cleanup was not confirmed: %w", cleanupErr))
			}
		}
	}
	if m.Tier == "isolated" {
		if removeErr := a.removeIsolatedVM(m); removeErr != nil {
			return errors.Join(err, removeErr)
		}
	}
	if removeErr := os.Remove(a.machinePath(m.Name)); removeErr != nil {
		return errors.Join(err, removeErr)
	}
	return err
}

// hostZone is the host's IANA time zone, which machines show as WSL does.
func hostZone() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return "Etc/UTC"
	}
	_, zone, ok := strings.Cut(target, "zoneinfo/")
	if !ok || !protocol.ValidZone(zone) {
		return "Etc/UTC"
	}
	return zone
}

func (a *app) requirePrepared(m *machineRecord) error {
	if !m.Prepared {
		return fmt.Errorf("creating %s did not finish; run nsl remove %s --yes", m.Name, m.Name)
	}
	return nil
}

// startMachine starts the VM and the machine and waits for both.
func (a *app) startMachine(m *machineRecord) (*vmRecord, error) {
	if err := a.requirePrepared(m); err != nil {
		return nil, err
	}
	v, err := a.machineVM(m, true)
	if err != nil {
		return nil, err
	}
	idle, err := a.idleTimeout()
	if err != nil {
		return nil, err
	}
	var started protocol.StartResult
	if err = a.agentJSON(v, protocol.Request{Op: "start", Machine: m.Name, ID: m.ID, IdleTimeout: idle}, nil, 90*time.Second, &started); err != nil {
		return nil, err
	}
	// A machine without its desktop still runs commands.
	if err = a.startDesktop(v, m); err != nil {
		fmt.Fprintln(a.err, "nsl: desktop session:", err)
	}
	return v, nil
}

func (a *app) machineCommand(op string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: %s NAME", op)
	}
	m, err := a.machine(args[0])
	if err != nil {
		return err
	}
	switch op {
	case "start":
		_, err = a.startMachine(m)
		return err
	case "default":
		if err = a.requirePrepared(m); err != nil {
			return err
		}
		return a.setDefault(m.Name)
	}
	// stop: a machine in a stopped VM is already stopped.
	v, err := a.vmOf(m)
	if err != nil || v == nil {
		return err
	}
	if state, err := a.vmState(v); err != nil || state != "running" {
		return err
	}
	return a.agentJSON(v, protocol.Request{Op: "stop", Machine: m.Name, ID: m.ID}, nil, 60*time.Second, nil)
}

// statuses asks the running VMs for their machines' states.
func (a *app) statuses(vms []*vmRecord) map[string]protocol.MachineStatus {
	out := map[string]protocol.MachineStatus{}
	for _, v := range vms {
		if state, err := a.vmState(v); err != nil || state != "running" {
			continue
		}
		var list []protocol.MachineStatus
		if a.agentJSON(v, protocol.Request{Op: "machines"}, nil, 10*time.Second, &list) == nil {
			for _, s := range list {
				out[s.Machine] = s
			}
		}
	}
	return out
}

func (a *app) listMachines(w io.Writer, vms []*vmRecord) error {
	names, err := a.machineNames()
	if err != nil {
		return err
	}
	removing, _ := filepath.Glob(filepath.Join(a.home, "removing", "*.json"))
	if len(names) == 0 && len(removing) == 0 {
		fmt.Fprintln(w, "No machines; create one with nsl create NAME --distro DISTRO:RELEASE")
		return nil
	}
	current, err := a.defaultMachine()
	if err != nil {
		return err
	}
	statuses := a.statuses(vms)
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "MACHINE\tSTATE\tIMAGE\tTIER\tDEFAULT")
	for _, name := range names {
		m, err := a.machine(name)
		if err != nil {
			return err
		}
		state := "stopped"
		if s, ok := statuses[name]; ok && s.ID == m.ID {
			state = s.State
		}
		if !m.Prepared {
			state = "incomplete"
		}
		mark := ""
		if name == current {
			mark = "*"
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\n", name, state, m.BuildID, m.Tier, mark)
	}
	for _, p := range removing {
		fmt.Fprintf(t, "%s\tremoving\t\t\t\n", strings.TrimSuffix(filepath.Base(p), ".json"))
	}
	return t.Flush()
}

// removalRecord reads either the active record or a pending removal. Callers
// recheck its identity after waiting for the machine lock.
func (a *app) removalRecord(name string) (*machineRecord, bool, error) {
	m, err := a.readMachine(a.removingPath(name), name)
	if errors.Is(err, os.ErrNotExist) {
		m, err = a.machine(name)
		return m, false, err
	}
	return m, err == nil, err
}

// reserveRemoval holds the manager lock only while moving the record. The
// caller already holds the machine lock and keeps it until removal finishes.
func (a *app) reserveRemoval(expected *machineRecord) (*machineRecord, error) {
	manager, err := fileLock(filepath.Join(a.home, "lock"))
	if err != nil {
		return nil, err
	}
	defer unlock(manager)
	m, pending, err := a.removalRecord(expected.Name)
	if err != nil {
		return nil, err
	}
	if m.ID != expected.ID {
		return nil, errors.New(expected.Name + " was replaced while waiting; retry")
	}
	if !pending {
		if err = os.Rename(a.machinePath(m.Name), a.removingPath(m.Name)); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// remove previews, then permanently removes a stopped machine. The record
// moves to removing/ first, so an interrupted removal resumes.
func (a *app) remove(args []string) error {
	if len(args) == 0 || !protocol.ValidName(args[0]) {
		return errors.New("usage: remove NAME [--yes]")
	}
	name := args[0]
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(a.err)
	yes := fs.Bool("yes", false, "permanently remove the stopped machine")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: remove NAME [--yes]")
	}
	if err := a.initMachines(); err != nil {
		return err
	}
	m, _, err := a.removalRecord(name)
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Fprintf(a.out, "This permanently deletes machine %s (%s): its packages, services and home.\nHost files under /mnt/host are not touched. Run nsl remove %s --yes to proceed.\n", name, m.BuildID, name)
		return nil
	}
	// Never wait for a machine while holding the manager lock: creation needs
	// the manager to create its VM while it holds this same machine lock.
	l, err := fileLock(filepath.Join(a.machinesDir(), "."+name+".lock"))
	if err != nil {
		return err
	}
	defer unlock(l)
	m, err = a.reserveRemoval(m)
	if err != nil {
		return err
	}
	tombstone := a.removingPath(name)
	if m.Tier == "isolated" {
		// The machine's VM holds nothing else, so it goes with the machine.
		err = a.removeIsolatedVM(m)
	} else {
		var v *vmRecord
		v, err = a.runningVM(false)
		if err == nil {
			err = a.stopHelper(desktopUnit(v, name), desktopDescription(v, name))
		}
		if err != nil {
			return fmt.Errorf("removal of %s is pending; retry nsl remove %s --yes: %w", name, name, err)
		}
		err = a.agentJSON(v, protocol.Request{Op: "remove", Machine: name, ID: m.ID}, nil, 10*time.Minute, nil)
	}
	var agentErr *protocol.Error
	if err != nil && (!errors.As(err, &agentErr) || agentErr.Code != protocol.CodeUnknownMachine) {
		if errors.As(err, &agentErr) && agentErr.Code == protocol.CodeBusy {
			// Still running: put the record back so the machine stays usable.
			if renameErr := os.Rename(tombstone, a.machinePath(name)); renameErr != nil {
				return errors.Join(err, renameErr)
			}
			return fmt.Errorf("stop %s first: %w", name, err)
		}
		return fmt.Errorf("removal of %s is pending; retry nsl remove %s --yes: %w", name, name, err)
	}
	if current, _ := a.defaultMachine(); current == name {
		if err = a.setDefault(""); err != nil {
			return err
		}
	}
	if err = os.RemoveAll(a.sshDir(name)); err != nil {
		return err
	}
	if err = os.Remove(tombstone); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Removed %s\n", name)
	return nil
}

// removeIsolatedVM stops an isolated machine's VM and deletes it with its
// disks. The directory is renamed first, so an interrupted removal resumes.
func (a *app) removeIsolatedVM(m *machineRecord) error {
	dir, gone := a.isolatedVMDir(m.Name), filepath.Join(a.isolatedDir(), ".removing-"+m.Name)
	v, err := a.loadVMAt(dir)
	if err != nil {
		return err
	}
	if v != nil {
		if v.Machine.ID != m.ID {
			return fmt.Errorf("%s belongs to another machine; inspect it before removing", dir)
		}
		l, v, err := a.lockVM(v)
		if err != nil {
			return err
		}
		defer unlock(l)
		if err = a.requireIsolatedStopped(v, m); err != nil {
			return err
		}
		if err = a.stopVM(v); err != nil {
			return err
		}
		if err = os.Rename(dir, gone); err != nil {
			return err
		}
	}
	return os.RemoveAll(gone)
}

// requireIsolatedStopped checks destructive removal independently of the
// best-effort status listing. The caller holds the VM lock.
func (a *app) requireIsolatedStopped(v *vmRecord, m *machineRecord) error {
	state, err := a.vmState(v)
	if err != nil {
		return err
	}
	if state == "stopped" || state == "failed" {
		return nil
	}
	if state != "running" {
		return fmt.Errorf("the VM of %s is %s; retry after it stops", m.Name, state)
	}
	var statuses []protocol.MachineStatus
	if err = a.agentJSON(v, protocol.Request{Op: "machines"}, nil, 10*time.Second, &statuses); err != nil {
		return fmt.Errorf("checking whether %s is stopped: %w", m.Name, err)
	}
	for _, status := range statuses {
		if status.Machine != m.Name {
			continue
		}
		if status.ID != m.ID {
			return fmt.Errorf("%s in its VM has another ID; refusing removal", m.Name)
		}
		if status.State != "stopped" {
			return &protocol.Error{Code: protocol.CodeBusy, Message: m.Name + " is " + status.State + "; stop it first"}
		}
		return nil
	}
	// Failed creation can leave an empty VM with no published machine.
	if !m.Prepared && len(statuses) == 0 {
		return nil
	}
	return fmt.Errorf("%s is missing from its VM's status report; refusing removal", m.Name)
}

// translate maps a host directory to its machine path under /mnt/host,
// matching ancestors by device and inode so that bind-mount and symlink
// aliases translate.
func translate(dir string, shares []protocol.Share) (string, bool) {
	rest := ""
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		for _, s := range shares {
			if sameFile(current, s.Source) {
				return filepath.Join("/mnt/host"+s.Source, rest), true
			}
		}
		if current == "/" || current == "." {
			return "", false
		}
		rest = filepath.Join(filepath.Base(current), rest)
	}
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// forwardedEnv is the host environment a command receives: terminal and locale.
func forwardedEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if k == "TERM" || k == "COLORTERM" || k == "LANG" || k == "LANGUAGE" || (strings.HasPrefix(k, "LC_") && strings.Trim(k[3:], "ABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" && len(k) > 3) {
			if len(v) <= 256 && !strings.ContainsAny(v, "\x00\n") {
				env[k] = v
			}
		}
	}
	return env
}

// run executes argv in a machine, or a login shell when shell is set.
func (a *app) run(args []string, shell bool) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(a.err)
	name := fs.String("m", "", "machine name (default: the default machine)")
	root := fs.Bool("root", false, "run as machine root")
	cd := fs.String("cd", "", "absolute machine directory")
	if shell {
		fs = flag.NewFlagSet("nsl", flag.ContinueOnError)
		fs.SetOutput(a.err)
		name = fs.String("m", "", "machine name")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	argv := fs.Args()
	if shell != (len(argv) == 0) {
		if shell {
			return errors.New("unexpected arguments; use nsl run to run a command")
		}
		return errors.New("usage: run [-m NAME] [--root] [--cd PATH] COMMAND [ARGS...]")
	}
	if *cd != "" && !filepath.IsAbs(*cd) {
		return errors.New("--cd takes an absolute machine path")
	}
	if *name == "" {
		current, err := a.defaultMachine()
		if err != nil {
			return err
		}
		if current == "" {
			var w strings.Builder
			_ = a.listMachines(&w, nil)
			return errors.New("there is no default machine; use -m NAME or nsl default NAME\n" + w.String())
		}
		*name = current
	}
	m, err := a.machine(*name)
	if err != nil {
		return err
	}
	directory := *cd
	if directory == "" {
		shares, err := a.hostShares()
		if err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		// An isolated machine sees no host files, so nothing translates.
		translated, ok := translate(cwd, shares)
		switch {
		case ok && m.Tier == "shared":
			directory = translated
		case shell:
			fmt.Fprintf(a.err, "nsl: %s is not shared with %s; starting in the home directory\n", cwd, m.Name)
		default:
			return fmt.Errorf("%s is not shared with %s; use --cd to choose a machine directory", cwd, m.Name)
		}
	}
	if shell {
		// The account's login shell, from passwd, which systemd puts in $SHELL.
		argv = []string{"/bin/sh", "-c", `exec "${SHELL:-/bin/sh}" -l`}
	}
	v, err := a.startMachine(m)
	if err != nil {
		return err
	}
	idle, err := a.idleTimeout()
	if err != nil {
		return err
	}
	tty := isTerminal(a.in) && isTerminal(a.out)
	encoded, err := protocol.Encode(protocol.Request{Protocol: protocol.Version, Op: "run", Machine: m.Name, ID: m.ID, Argv: argv,
		Directory: directory, Root: *root, TTY: tty, Env: forwardedEnv(), IdleTimeout: idle})
	if err != nil {
		return err
	}
	return a.call(a.in, a.out, "ssh", append(a.sshArgs(v, tty), encoded)...)
}
