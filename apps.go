package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/frostyard/nsl/internal/protocol"
)

// Host menu entries of machine applications ([ADR-0022], docs/specs/cli.md#menu-entries):
// `nsl app` reads an application's desktop entry, its icon and its command line
// from the machine through the agent's run operation with cat, and writes a
// host entry of its own. The guest is never given a shell command line.

const (
	appUsage = "usage: app add [-m NAME] [--mime-types] APP | app remove [-m NAME] APP | app list [-m NAME]"

	// The guest reads are bounded: one entry, one icon. A host entry is a
	// guest entry quoted for a desktop, so it can be longer than the guest's.
	appEntryLimit  = 64 << 10
	appIconLimit   = 1 << 20
	hostEntryLimit = 1 << 20
)

// appCommand is `nsl app add|remove|list`.
func (a *app) appCommand(args []string) error {
	if len(args) == 0 {
		return errors.New(appUsage)
	}
	rest := args[1:]
	switch args[0] {
	case "add":
		return a.appAdd(rest)
	case "remove":
		return a.appRemove(rest)
	case "list":
		return a.appList(rest)
	}
	return fmt.Errorf("unknown app command %s; %s", args[0], appUsage)
}

var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// appName is APP as a desktop-file ID without its .desktop suffix.
func appName(s string) (string, error) {
	s = strings.TrimSuffix(s, ".desktop")
	if len(s) > 200 || !appIDPattern.MatchString(s) {
		return "", errors.New("APP must be a desktop-file ID of at most 200 letters, digits, '.' or '-', starting with a letter or digit")
	}
	return s, nil
}

// appMachine resolves -m, which names the default machine when absent, as run
// does.
func (a *app) appMachine(name string) (*machineRecord, error) {
	if name == "" {
		current, err := a.defaultMachine()
		if err != nil {
			return nil, err
		}
		if current == "" {
			var w strings.Builder
			_ = a.listMachines(&w, nil)
			return nil, errors.New("there is no default machine; use -m NAME or nsl default NAME\n" + w.String())
		}
		name = current
	}
	return a.machine(name)
}

// applicationsDir is the user's desktop-entry directory, which desktops watch.
func (a *app) applicationsDir() string { return filepath.Join(a.dataHome, "applications") }

// menuEntryPath is the host entry of APP in the machine NAME.
func (a *app) menuEntryPath(name, app string) string {
	return filepath.Join(a.applicationsDir(), "nsl-"+name+"-"+app+".desktop")
}

// appsDir holds a machine's icons inside nsl's own state.
func (a *app) appsDir(name string) string { return filepath.Join(a.machinesDir(), name+".apps") }

// appAdd reads one application's entry, icon and command line from the machine
// and writes the host's menu entry for it.
func (a *app) appAdd(args []string) error {
	fs := flag.NewFlagSet("app add", flag.ContinueOnError)
	fs.SetOutput(a.err)
	name := fs.String("m", "", "machine name (default: the default machine)")
	mimeTypes := fs.Bool("mime-types", false, "copy the application's MimeType so it appears in Open With lists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(appUsage)
	}
	app, err := appName(fs.Arg(0))
	if err != nil {
		return err
	}
	m, err := a.appMachine(*name)
	if err != nil {
		return err
	}
	if m.Tier == "isolated" {
		return fmt.Errorf("%s is isolated and has no host desktop; menu entries are for shared machines", m.Name)
	}
	v, err := a.startMachine(m)
	if err != nil {
		return err
	}
	guest, path, err := a.readGuestEntry(v, m, app)
	if err != nil {
		return err
	}
	argv, field, err := execArgs(guest.Exec)
	if err != nil {
		return fmt.Errorf("%s in %s: %w", path, m.Name, err)
	}
	mimeType := ""
	mimeTypeCount := 0
	if *mimeTypes {
		mimeType, err = menuMimeTypes(guest.MimeType, field)
		if err != nil {
			return fmt.Errorf("%s in %s: %w", path, m.Name, err)
		}
		mimeTypeCount = mimeTypeItemCount(mimeType)
	}
	icon, err := a.readGuestIcon(v, m, guest.Icon)
	if err != nil {
		return err
	}
	if guest.Icon != "" && icon == nil {
		fmt.Fprintf(a.err, "nsl: no icon %q for %s in %s; the entry has no Icon\n", guest.Icon, app, m.Name)
	}
	dir := "/home/" + m.User
	if strings.HasPrefix(guest.Path, "/") {
		dir = guest.Path
	}
	// The writes hold the machine lock and recheck the ID, so they cannot race
	// nsl remove.
	l, locked, err := a.lockMachine(m)
	if err != nil {
		return err
	}
	defer unlock(l)
	*m = *locked
	entryPath := a.menuEntryPath(m.Name, app)
	// Refuse a file nsl did not write before touching the icon, so a refusal
	// leaves nothing behind.
	if _, owned, err := a.ownedEntry(entryPath, m, app); err != nil {
		return err
	} else if !owned {
		if _, err := os.Lstat(entryPath); err == nil {
			return fmt.Errorf("%s exists and nsl did not write it; move it away and run nsl app add again", entryPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	iconPath := ""
	if icon != nil {
		if iconPath, err = a.writeAppIcon(m, app, icon); err != nil {
			return err
		}
	}
	body := renderMenuEntry(menuInput{Machine: m.Name, ID: m.ID, App: app, Guest: guest, Argv: argv, Field: field,
		Dir: dir, Nsl: a.nslPath(), Icon: iconPath, MimeType: mimeType})
	if err = a.publishMenuEntry(entryPath, body, m, app); err != nil {
		return err
	}
	types := ""
	switch {
	case mimeTypeCount == 1:
		types = ", for 1 file type"
	case mimeTypeCount > 1:
		types = fmt.Sprintf(", for %d file types", mimeTypeCount)
	}
	fmt.Fprintf(a.out, "Added %s (%s) to the menu as %s%s\n", guest.Name, m.Name, filepath.Base(entryPath), types)
	return nil
}

// appRemove deletes the host entry of one application and its icons.
func (a *app) appRemove(args []string) error {
	fs := flag.NewFlagSet("app remove", flag.ContinueOnError)
	fs.SetOutput(a.err)
	name := fs.String("m", "", "machine name (default: the default machine)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(appUsage)
	}
	app, err := appName(fs.Arg(0))
	if err != nil {
		return err
	}
	m, err := a.appMachine(*name)
	if err != nil {
		return err
	}
	l, locked, err := a.lockMachine(m)
	if err != nil {
		return err
	}
	defer unlock(l)
	*m = *locked
	entryPath := a.menuEntryPath(m.Name, app)
	if _, owned, err := a.ownedEntry(entryPath, m, app); err != nil {
		return err
	} else if !owned {
		if _, err := os.Lstat(entryPath); err == nil {
			return fmt.Errorf("%s exists and nsl did not write it; not removing it", entryPath)
		}
		return fmt.Errorf("no menu entry for %s in %s; see nsl app list", app, m.Name)
	}
	if err = os.Remove(entryPath); err != nil {
		return err
	}
	if err = a.removeAppIcons(m, app); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Removed %s\n", filepath.Base(entryPath))
	return nil
}

// appList prints the menu entries nsl owns, for one machine or all of them.
func (a *app) appList(args []string) error {
	fs := flag.NewFlagSet("app list", flag.ContinueOnError)
	fs.SetOutput(a.err)
	name := fs.String("m", "", "machine name (default: every machine)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: app list [-m NAME]")
	}
	var machines []*machineRecord
	if *name != "" {
		m, err := a.machine(*name)
		if err != nil {
			return err
		}
		machines = append(machines, m)
	} else {
		names, err := a.machineNames()
		if err != nil {
			return err
		}
		for _, n := range names {
			m, err := a.machine(n)
			if err != nil {
				return err
			}
			machines = append(machines, m)
		}
	}
	entries, err := a.menuEntries(machines)
	if err != nil {
		return err
	}
	return printMenuEntries(a.out, entries)
}

// menuEntry is one owned host entry, as app list prints it.
type menuEntry struct {
	Machine, App, Name string
	MimeTypes          int
}

// menuEntries is every entry nsl owns for the machines given, sorted by machine
// and APP.
func (a *app) menuEntries(machines []*machineRecord) ([]menuEntry, error) {
	entries := []menuEntry{}
	files, err := os.ReadDir(a.applicationsDir())
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	for _, m := range machines {
		var mine []menuEntry
		for _, f := range files {
			file := f.Name()
			if !strings.HasPrefix(file, "nsl-"+m.Name+"-") || !strings.HasSuffix(file, ".desktop") {
				continue
			}
			entry, owned, err := a.ownedEntry(filepath.Join(a.applicationsDir(), file), m, "")
			if err != nil {
				return nil, err
			}
			if owned {
				mine = append(mine, menuEntry{Machine: m.Name, App: entry.App, Name: entry.Name, MimeTypes: mimeTypeItemCount(entry.MimeType)})
			}
		}
		sort.Slice(mine, func(i, j int) bool { return mine[i].App < mine[j].App })
		entries = append(entries, mine...)
	}
	return entries, nil
}

func printMenuEntries(w io.Writer, entries []menuEntry) error {
	if len(entries) == 0 {
		fmt.Fprintln(w, "No menu entries; add one with nsl app add [-m NAME] APP")
		return nil
	}
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "MACHINE\tAPP\tNAME\tFILE TYPES")
	for _, e := range entries {
		count := "-"
		if e.MimeTypes != 0 {
			count = fmt.Sprint(e.MimeTypes)
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", e.Machine, e.App, e.Name, count)
	}
	return t.Flush()
}

// removeMenuEntries deletes the machine's owned entries and its icons. Files nsl
// did not write stay; remove calls it after the machine is gone and before the
// tombstone, so a resumed removal finishes it.
func (a *app) removeMenuEntries(m *machineRecord) error {
	files, err := os.ReadDir(a.applicationsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, f := range files {
		file := f.Name()
		if !strings.HasPrefix(file, "nsl-"+m.Name+"-") || !strings.HasSuffix(file, ".desktop") {
			continue
		}
		path := filepath.Join(a.applicationsDir(), file)
		_, owned, err := a.ownedEntry(path, m, "")
		if err != nil {
			return err
		}
		if !owned {
			continue
		}
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.RemoveAll(a.appsDir(m.Name))
}

// menuEntryCount is how many owned entries go with the machine, which remove
// names in its preview.
func (a *app) menuEntryCount(m *machineRecord) (int, error) {
	entries, err := a.menuEntries([]*machineRecord{m})
	return len(entries), err
}

// ownedEntry reads the host entry at path. It is owned when it is a regular
// file of the user whose markers name this machine and its ID, and APP too when
// app is not empty. A symlink, another user's file or a file nsl did not write
// is not owned.
func (a *app) ownedEntry(path string, m *machineRecord, app string) (*desktopEntry, bool, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || !ok || int(stat.Uid) != a.uid || st.Size() > hostEntryLimit {
		return nil, false, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	values, err := readDesktopGroup(b)
	if err != nil {
		return nil, false, nil
	}
	entry := desktopEntryOf(values)
	if entry.Machine != m.Name || entry.MachineID != m.ID || entry.App == "" || (app != "" && entry.App != app) {
		return nil, false, nil
	}
	return entry, true, nil
}

// publishMenuEntry writes the host entry: a new path is published with a hard
// link, so nothing is replaced, and an entry nsl owns is replaced atomically.
func (a *app) publishMenuEntry(path string, body []byte, m *machineRecord, app string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	_, owned, err := a.ownedEntry(path, m, app)
	if err != nil {
		return err
	}
	// The temporary name ends in nothing a desktop reads; the entry appears
	// only complete.
	tmp, err := os.CreateTemp(dir, ".nsl-app-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(body); err == nil {
		if err = tmp.Chmod(0644); err == nil {
			err = tmp.Sync()
		}
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if owned {
		if err = os.Rename(tmp.Name(), path); err != nil {
			return err
		}
		return syncDir(dir)
	}
	if err = os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s exists and nsl did not write it; move it away and run nsl app add again", path)
		}
		return err
	}
	return syncDir(dir)
}

// guestRun sends one bounded run request to the machine. The buffer is nil
// only when the request was not sent.
func (a *app) guestRun(v *vmRecord, m *machineRecord, argv []string, limit int) (*boundedBuffer, error) {
	idle, err := a.idleTimeout()
	if err != nil {
		return nil, err
	}
	encoded, err := protocol.Encode(protocol.Request{Protocol: protocol.Version, Op: "run", Machine: m.Name, ID: m.ID,
		Argv: argv, Directory: "/", IdleTimeout: idle})
	if err != nil {
		return nil, err
	}
	out := &boundedBuffer{limit: limit + 1}
	err = a.r.run(context.Background(), nil, out, io.Discard, os.Environ(), "ssh", append(a.sshArgs(v, false), encoded)...)
	return out, err
}

// guestFile reads PATH in the machine with cat through the agent's run
// operation. found is false when cat's exit status is 1, which is how cat
// reports a missing file; every other failure stops the command.
func (a *app) guestFile(v *vmRecord, m *machineRecord, path string, limit int) ([]byte, bool, error) {
	out, err := a.guestRun(v, m, []string{"cat", "--", path}, limit)
	if err != nil {
		if out == nil {
			return nil, false, err
		}
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, false, nil
		}
		if errors.Is(err, io.ErrShortBuffer) || out.buffer.Len() > limit {
			return nil, false, fmt.Errorf("%s in %s is larger than %d bytes", path, m.Name, limit)
		}
		return nil, false, fmt.Errorf("reading %s in %s: %w", path, m.Name, err)
	}
	if out.buffer.Len() > limit {
		return nil, false, fmt.Errorf("%s in %s is larger than %d bytes", path, m.Name, limit)
	}
	return out.buffer.Bytes(), true, nil
}

// guestListing runs an argv-only guest listing. Exit statuses 0, 1 and 2 are
// listings, as ls reports a usable directory or missing entries.
func (a *app) guestListing(v *vmRecord, m *machineRecord, argv []string, limit int) ([]byte, error) {
	out, err := a.guestRun(v, m, argv, limit)
	if err != nil {
		if out == nil {
			return nil, err
		}
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case 0, 1, 2:
				return boundedListing(out, limit)
			}
		}
		if errors.Is(err, io.ErrShortBuffer) || out.buffer.Len() > limit {
			return nil, fmt.Errorf("listing %s in %s is larger than %d bytes", strings.Join(argv[2:], " "), m.Name, limit)
		}
		return nil, fmt.Errorf("listing in %s: %w", m.Name, err)
	}
	return boundedListing(out, limit)
}

func boundedListing(out *boundedBuffer, limit int) ([]byte, error) {
	if out.buffer.Len() > limit {
		return nil, io.ErrShortBuffer
	}
	return out.buffer.Bytes(), nil
}

// guestEntryPaths is where a machine keeps desktop entries, in lookup order.
func guestEntryPaths(user string) []string {
	return []string{
		"/home/" + user + "/.local/share/applications/%s.desktop",
		"/usr/local/share/applications/%s.desktop",
		"/usr/share/applications/%s.desktop",
	}
}

// readGuestEntry finds and reads APP.desktop in the machine.
func (a *app) readGuestEntry(v *vmRecord, m *machineRecord, app string) (*desktopEntry, string, error) {
	for _, pattern := range guestEntryPaths(m.User) {
		path := fmt.Sprintf(pattern, app)
		content, found, err := a.guestFile(v, m, path, appEntryLimit)
		if err != nil {
			return nil, "", err
		}
		if !found {
			continue
		}
		entry, err := parseDesktopEntry(content)
		if err != nil {
			return nil, "", fmt.Errorf("%s in %s: %w", path, m.Name, err)
		}
		return entry, path, nil
	}
	return nil, "", fmt.Errorf("no %s.desktop in %s; see nsl run -m %s ls /usr/share/applications", app, m.Name, m.Name)
}

// nslPath is the absolute path of nsl for the host entry: the nsl on PATH,
// which a desktop session may lack, or the running binary. A symlink is not
// resolved, so the entry names the program the user runs.
func (a *app) nslPath() string {
	found, err := exec.LookPath("nsl")
	if err != nil {
		return a.self
	}
	abs, err := filepath.Abs(found)
	if err != nil {
		return a.self
	}
	return abs
}

// desktopEntry is the part of a machine's desktop entry that nsl reads, with
// its string escapes decoded.
type desktopEntry struct {
	Name, GenericName, Comment, Icon, Categories, Keywords, StartupWMClass string
	MimeType                                                               string
	Exec, Path                                                             string
	Terminal                                                               bool
	Machine, MachineID, App                                                string // nsl's markers
}

// readDesktopGroup reads the unlocalized keys of the entry's first group,
// which MUST be [Desktop Entry], with their string escapes decoded. It is all a
// host entry needs: nsl's own entries pass the guest checks below, and a user
// may edit one without nsl losing it.
func readDesktopGroup(content []byte) (map[string]string, error) {
	if !utf8.Valid(content) {
		return nil, errors.New("the desktop entry is not UTF-8")
	}
	values := map[string]string{}
	group, first := "", true
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("bad group header %q", line)
			}
			name := line[1 : len(line)-1]
			if first {
				if name != "Desktop Entry" {
					return nil, fmt.Errorf("the first group is [%s], not [Desktop Entry]", name)
				}
				first, group = false, name
				continue
			}
			group = "" // only [Desktop Entry] is read
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %q is not key=value", line)
		}
		// Spaces around the equals sign are ignored.
		key, value = strings.TrimSpace(key), strings.TrimLeft(value, " \t")
		if group != "Desktop Entry" || strings.Contains(key, "[") {
			continue // another group, or a localized key
		}
		decoded, err := decodeDesktopValue(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if strings.ContainsFunc(decoded, isControl) {
			return nil, fmt.Errorf("%s has a control character", key)
		}
		if _, dup := values[key]; dup {
			return nil, fmt.Errorf("%s appears twice", key)
		}
		values[key] = decoded
	}
	if first {
		return nil, errors.New("the entry has no [Desktop Entry] group")
	}
	return values, nil
}

// desktopEntryOf is the entry nsl reads from a group's keys. It leaves out the
// guest checks, which a host entry nsl wrote has already passed.
func desktopEntryOf(values map[string]string) *desktopEntry {
	return &desktopEntry{Name: values["Name"], GenericName: values["GenericName"], Comment: values["Comment"],
		Icon: values["Icon"], Categories: values["Categories"], Keywords: values["Keywords"],
		StartupWMClass: values["StartupWMClass"], MimeType: values["MimeType"], Exec: values["Exec"], Path: values["Path"],
		Terminal: values["Terminal"] == "true", Machine: values["X-Nsl-Machine"],
		MachineID: values["X-Nsl-Machine-ID"], App: values["X-Nsl-App"]}
}

// parseDesktopEntry reads a machine's desktop entry and refuses one a host
// cannot honour for a machine application.
func parseDesktopEntry(content []byte) (*desktopEntry, error) {
	values, err := readDesktopGroup(content)
	if err != nil {
		return nil, err
	}
	if typ := values["Type"]; typ != "Application" {
		if typ == "" {
			return nil, errors.New("the entry has no Type=Application")
		}
		return nil, fmt.Errorf("the entry has Type=%s, not Application", typ)
	}
	if values["Hidden"] == "true" {
		return nil, errors.New("the entry is Hidden=true")
	}
	entry := desktopEntryOf(values)
	if entry.Name == "" {
		return nil, errors.New("the entry has no Name")
	}
	if entry.Exec == "" {
		return nil, errors.New("the entry has no Exec")
	}
	return entry, nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// decodeDesktopValue decodes the string escapes of a key's value.
func decodeDesktopValue(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			return "", errors.New("the value ends with a backslash")
		}
		i++
		switch s[i] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		default:
			return "", fmt.Errorf("unsupported escape %q", `\`+string(s[i]))
		}
	}
	return b.String(), nil
}

// encodeDesktopValue writes a value with the string escapes. A space is escaped
// where the format needs it: at either end, and as the first of a run.
func encodeDesktopValue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == ' ' && (i == 0 || i == len(s)-1 || s[i+1] == ' '):
			b.WriteString(`\s`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// droppedFieldCodes are the Exec field codes nsl does not pass to the host.
var droppedFieldCodes = map[string]bool{"%i": true, "%c": true, "%k": true, "%d": true, "%D": true,
	"%n": true, "%N": true, "%v": true, "%m": true}

// execField is the first file or URL field code in a guest Exec. Index is the
// number of guest argv elements before it.
type execField struct {
	Code  string
	Index int
}

// unquotedSyntax are the characters a desktop's Exec may not pass unquoted.
// The double quote belongs here although the contract's list omits it: quoting
// part of an argument is not allowed, and passing it through would split the
// argument at its next space rather than refuse it.
const unquotedSyntax = "'\\\"><~|&;$*?#()`"

// execArgs splits a desktop entry's Exec into arguments. The first file or
// URL field code is kept; later file field codes and unsupported desktop fields
// are dropped. Shell syntax is refused rather than guessed.
func execArgs(execLine string) ([]string, *execField, error) {
	var split []string
	for i := 0; i < len(execLine); {
		if execLine[i] == ' ' {
			i++
			continue
		}
		if execLine[i] == '"' {
			var b strings.Builder
			closed := false
			for i++; i < len(execLine) && !closed; {
				switch execLine[i] {
				case '"':
					i++
					closed = true
				case '\\':
					if i+1 >= len(execLine) {
						return nil, nil, errors.New("the Exec ends with a backslash")
					}
					if n := execLine[i+1]; strings.ContainsRune(execQuotedEscapes, rune(n)) {
						b.WriteByte(n)
						i += 2
					} else {
						return nil, nil, fmt.Errorf("the Exec quotes the unsupported escape %q", `\`+string(n))
					}
				default:
					b.WriteByte(execLine[i])
					i++
				}
			}
			if !closed {
				return nil, nil, errors.New("the Exec has an unclosed quote")
			}
			// A quote encloses a whole argument, so a space or the end of
			// the line must follow it.
			if i < len(execLine) && execLine[i] != ' ' {
				return nil, nil, errors.New("the Exec quotes part of an argument, which nsl does not support")
			}
			split = append(split, b.String())
			continue
		}
		start := i
		for i < len(execLine) && execLine[i] != ' ' {
			if strings.ContainsRune(unquotedSyntax, rune(execLine[i])) {
				return nil, nil, fmt.Errorf("the Exec argument %q uses shell syntax or quotes part of an argument", execLine[start:i+1])
			}
			i++
		}
		split = append(split, execLine[start:i])
	}
	var args []string
	var field *execField
	for _, arg := range split {
		if len(arg) == 2 && arg[0] == '%' && strings.ContainsRune("fFuU", rune(arg[1])) {
			if len(args) == 0 {
				continue
			}
			if field == nil {
				field = &execField{Code: arg[1:2], Index: len(args)}
				args = append(args, arg)
			}
			continue
		}
		if droppedFieldCodes[arg] {
			continue
		}
		expanded, err := expandFieldCodes(arg)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, expanded)
	}
	if len(args) == 0 {
		return nil, nil, errors.New("the Exec has no command")
	}
	return args, field, nil
}

var mimeTypePartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]{0,126}$`)

// menuMimeTypes is the host MimeType of `app add --mime-types` (ADR-0022): the
// guest's media types in order, without empty or repeated items. It refuses an
// entry with none, an Exec that takes no files, and an item that is not a
// TYPE/SUBTYPE media type of RFC 6838 restricted names.
func menuMimeTypes(guest string, field *execField) (string, error) {
	seen := map[string]bool{}
	items := []string{}
	for _, item := range strings.Split(guest, ";") {
		if item == "" {
			continue
		}
		typ, subtype, _ := strings.Cut(item, "/")
		if !mimeTypePartPattern.MatchString(typ) || !mimeTypePartPattern.MatchString(subtype) {
			return "", fmt.Errorf("the MimeType item %q is not a TYPE/SUBTYPE media type", item)
		}
		if !seen[item] {
			seen[item] = true
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return "", errors.New("the guest entry has no MimeType or only empty items")
	}
	if field == nil {
		return "", errors.New("the Exec has no %f, %F, %u or %U that nsl keeps")
	}
	return strings.Join(items, ";") + ";", nil
}

// mimeTypeItemCount counts non-empty MimeType items in a semicolon-separated value.
func mimeTypeItemCount(value string) int {
	count := 0
	for _, item := range strings.Split(value, ";") {
		if item != "" {
			count++
		}
	}
	return count
}

// expandFieldCodes turns %% into % and refuses every other field code.
func expandFieldCodes(arg string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(arg); i++ {
		if arg[i] != '%' {
			b.WriteByte(arg[i])
			continue
		}
		if i+1 >= len(arg) {
			return "", errors.New("the Exec ends with a bare percent")
		}
		if arg[i+1] != '%' {
			return "", fmt.Errorf("the Exec uses the field code %%%c, which nsl does not translate yet", arg[i+1])
		}
		b.WriteByte('%')
		i++
	}
	return b.String(), nil
}

// execQuotedEscapes are the characters a quoted Exec argument may escape.
const execQuotedEscapes = "\"`$\\"

// execQuotedChars are the characters that make a host Exec argument use quotes.
const execQuotedChars = " \t\"'\\><~|&;$*?#()`"

// quoteExecArg quotes one host Exec argument so that a desktop reads it back as
// one argument.
func quoteExecArg(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, execQuotedChars) {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(arg); i++ {
		if strings.ContainsRune("\"`$\\", rune(arg[i])) {
			b.WriteByte('\\')
		}
		b.WriteByte(arg[i])
	}
	b.WriteByte('"')
	return b.String()
}

// menuInput is everything the host entry holds.
type menuInput struct {
	Machine, ID, App string
	MimeType         string
	Guest            *desktopEntry
	Argv             []string // the guest Exec, split and with the first file field code
	Field            *execField
	Dir              string // the machine directory the command starts in
	Nsl              string // the absolute path of nsl
	Icon             string // the host path of the icon, or ""
}

// renderMenuEntry is the host entry of one application: selected keys of the
// guest entry, the machine's name and ID, and nothing the host cannot honour.
func renderMenuEntry(in menuInput) []byte {
	var b strings.Builder
	writeDesktopKey(&b, "Type", "Application")
	writeDesktopKey(&b, "Name", in.Guest.Name+" ("+in.Machine+")")
	writeDesktopKey(&b, "Exec", hostExec(in))
	writeDesktopKey(&b, "TryExec", in.Nsl)
	terminal := "false"
	if in.Guest.Terminal {
		terminal = "true"
	}
	writeDesktopKey(&b, "Terminal", terminal)
	for _, kv := range [][2]string{{"GenericName", in.Guest.GenericName}, {"Comment", in.Guest.Comment},
		{"Icon", in.Icon}, {"Categories", in.Guest.Categories}, {"Keywords", in.Guest.Keywords},
		{"StartupWMClass", in.Guest.StartupWMClass}} {
		if kv[1] != "" {
			writeDesktopKey(&b, kv[0], kv[1])
		}
	}
	if in.MimeType != "" {
		writeDesktopKey(&b, "MimeType", in.MimeType)
	}
	writeDesktopKey(&b, "X-Nsl-Machine", in.Machine)
	writeDesktopKey(&b, "X-Nsl-Machine-ID", in.ID)
	writeDesktopKey(&b, "X-Nsl-App", in.App)
	return []byte("[Desktop Entry]\n" + b.String())
}

func writeDesktopKey(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteString("=")
	b.WriteString(encodeDesktopValue(value))
	b.WriteString("\n")
}

// hostExec is `NSL run -m NAME --cd DIR -- ARGV`, quoted for a desktop entry.
func hostExec(in menuInput) string {
	parts := []string{in.Nsl, "run", "-m", in.Machine, "--cd", in.Dir}
	if in.Field != nil {
		after := len(in.Argv) - in.Field.Index - 1
		parts = append(parts, "--host-files", in.Field.Code+":"+fmt.Sprint(in.Field.Index)+":"+fmt.Sprint(after))
	}
	parts = append(parts, "--")
	out := make([]string, 0, len(parts)+len(in.Argv))
	for _, part := range parts {
		out = append(out, strings.ReplaceAll(quoteExecArg(part), "%", "%%"))
	}
	for i, part := range in.Argv {
		if in.Field != nil && i == in.Field.Index {
			out = append(out, "%"+in.Field.Code)
		} else {
			out = append(out, strings.ReplaceAll(quoteExecArg(part), "%", "%%"))
		}
	}
	return strings.Join(out, " ")
}

// appIcon is an icon's bytes and the file extension they belong in.
type appIcon struct {
	data []byte
	ext  string
}

// iconCandidate is one guest file an Icon value can name.
type iconCandidate struct{ path, ext string }

// iconSizes are the hicolor PNG sizes, largest first.
var iconSizes = []string{"512x512", "256x256", "192x192", "128x128", "96x96", "64x64", "48x48", "32x32"}

var (
	iconNamePattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
	pngSignature    = []byte("\x89PNG\r\n\x1a\n")
)

// iconCandidates lists the guest files an Icon value can name, in lookup order.
// Theme lookup beyond hicolor is later work (ADR-0022).
func iconCandidates(icon string) []iconCandidate {
	if strings.HasPrefix(icon, "/") {
		switch {
		case strings.HasSuffix(icon, ".png"):
			return []iconCandidate{{icon, "png"}}
		case strings.HasSuffix(icon, ".svg"):
			return []iconCandidate{{icon, "svg"}}
		}
		return nil
	}
	if !iconNamePattern.MatchString(icon) {
		return nil
	}
	out := []iconCandidate{{"/usr/share/icons/hicolor/scalable/apps/" + icon + ".svg", "svg"}}
	for _, size := range iconSizes {
		out = append(out, iconCandidate{"/usr/share/icons/hicolor/" + size + "/apps/" + icon + ".png", "png"})
	}
	return out
}

// readGuestIconCandidate reads and validates one icon candidate.
func (a *app) readGuestIconCandidate(v *vmRecord, m *machineRecord, candidate iconCandidate) (*appIcon, error) {
	content, found, err := a.guestFile(v, m, candidate.path, appIconLimit)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if !validIcon(content, candidate.ext) {
		fmt.Fprintf(a.err, "nsl: %s in %s is not a %s image\n", candidate.path, m.Name, strings.ToUpper(candidate.ext))
		return nil, nil
	}
	return &appIcon{data: content, ext: candidate.ext}, nil
}

// readGuestIcon finds the machine's icon. A nil icon means none was found.
func (a *app) readGuestIcon(v *vmRecord, m *machineRecord, icon string) (*appIcon, error) {
	candidates := iconCandidates(icon)
	for _, candidate := range candidates {
		found, err := a.readGuestIconCandidate(v, m, candidate)
		if err != nil {
			return nil, err
		}
		if found != nil {
			return found, nil
		}
	}
	if strings.HasPrefix(icon, "/") || !iconNamePattern.MatchString(icon) {
		return nil, nil
	}
	fallback, err := a.otherThemeIconCandidates(v, m, icon)
	if err != nil {
		return nil, err
	}
	if len(fallback) == 0 {
		return nil, nil
	}
	var batchCandidates []iconCandidate
	for _, candidate := range fallback {
		if len(candidate.path) <= 4096 {
			batchCandidates = append(batchCandidates, candidate)
		}
	}
	const iconListingBatchBytes = 32768
	for start := 0; start < len(batchCandidates); {
		end := start
		size := 0
		for end < len(batchCandidates) {
			length := len(batchCandidates[end].path) + 1
			if size+length > iconListingBatchBytes && end != start {
				break
			}
			size += length
			end++
			if size >= iconListingBatchBytes {
				break
			}
		}
		batch := batchCandidates[start:end]
		listingArgv := []string{"ls", "-1d", "--"}
		listingArgv = append(listingArgv, candidatePaths(batch)...)
		listing, err := a.guestListing(v, m, listingArgv, 64<<10)
		if err != nil {
			return nil, err
		}
		foundPaths := map[string]bool{}
		for _, line := range strings.Split(string(listing), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if line != "" {
				foundPaths[line] = true
			}
		}
		for _, candidate := range batch {
			if !foundPaths[candidate.path] {
				continue
			}
			found, err := a.readGuestIconCandidate(v, m, candidate)
			if err != nil {
				return nil, err
			}
			if found != nil {
				return found, nil
			}
		}
		start = end
	}
	return nil, nil
}

func candidatePaths(candidates []iconCandidate) []string {
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.path)
	}
	return paths
}

const iconThemeDirPattern = `^[A-Za-z0-9._+@-]+(?:/[A-Za-z0-9._+@-]+)*$`

var iconThemeDirRegexp = regexp.MustCompile(iconThemeDirPattern)

type themeDirectory struct {
	name        string
	size, scale int
	scalable    bool
}

// otherThemeIconCandidates lists icon candidates from themes other than
// hicolor, followed by the pixmaps directory.
func (a *app) otherThemeIconCandidates(v *vmRecord, m *machineRecord, icon string) ([]iconCandidate, error) {
	listing, err := a.guestListing(v, m, []string{"ls", "-1", "--", "/usr/share/icons"}, 64<<10)
	if err != nil {
		return nil, err
	}
	var themes []string
	for _, line := range strings.Split(string(listing), "\n") {
		theme := strings.TrimSuffix(line, "\r")
		if theme == "" || theme == "hicolor" || len(theme) > 255 || strings.HasPrefix(theme, ".") || !iconNamePattern.MatchString(theme) {
			continue
		}
		themes = append(themes, theme)
	}
	sort.Strings(themes)
	if len(themes) > 32 {
		themes = themes[:32]
	}
	var candidates []iconCandidate
	for _, theme := range themes {
		content, found, err := a.guestFile(v, m, "/usr/share/icons/"+theme+"/index.theme", 256<<10)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		dirs, ok := parseIconTheme(content)
		if !ok {
			continue
		}
		for _, dir := range dirs {
			base := "/usr/share/icons/" + theme + "/" + dir.name + "/" + icon
			candidates = append(candidates, iconCandidate{base + ".svg", "svg"}, iconCandidate{base + ".png", "png"})
			if len(candidates) >= 2048 {
				break
			}
		}
		if len(candidates) >= 2048 {
			break
		}
	}
	for _, ext := range []string{"svg", "png"} {
		if len(candidates) >= 2048 {
			break
		}
		candidates = append(candidates, iconCandidate{"/usr/share/pixmaps/" + icon + "." + ext, ext})
	}
	return candidates, nil
}

// parseIconTheme reads one machine index.theme and returns applications
// directories in scalable-first, then largest-first order.
func parseIconTheme(content []byte) ([]themeDirectory, bool) {
	if !utf8.Valid(content) {
		return nil, false
	}
	groups := map[string]map[string]string{}
	current := ""
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.TrimSpace(line[1 : len(line)-1])
			if _, ok := groups[current]; !ok {
				groups[current] = map[string]string{}
			}
			continue
		}
		if current == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		groups[current][strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	theme, ok := groups["Icon Theme"]
	if !ok {
		return nil, false
	}
	var names []string
	seenNames := map[string]bool{}
	for _, key := range []string{"Directories", "ScaledDirectories"} {
		for _, item := range strings.Split(theme[key], ",") {
			name := strings.TrimSpace(item)
			if name == "" || seenNames[name] {
				continue
			}
			seenNames[name] = true
			names = append(names, name)
		}
	}
	var dirs []themeDirectory
	for _, name := range names {
		group, ok := groups[name]
		if !ok || group["Context"] != "Applications" || !validIconThemeDir(name) {
			continue
		}
		size, err := strconv.Atoi(group["Size"])
		if err != nil || size < 1 {
			continue
		}
		scale := 1
		if value := strings.TrimSpace(group["Scale"]); value != "" {
			scale, err = strconv.Atoi(value)
			if err != nil || scale < 1 {
				continue
			}
		}
		dirs = append(dirs, themeDirectory{name: name, size: size, scale: scale,
			scalable: group["Type"] == "Scalable"})
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		if dirs[i].scalable != dirs[j].scalable {
			return dirs[i].scalable
		}
		if !dirs[i].scalable {
			left, right := dirs[i].size*dirs[i].scale, dirs[j].size*dirs[j].scale
			if left != right {
				return left > right
			}
		}
		return false
	})
	return dirs, true
}

func validIconThemeDir(name string) bool {
	if len(name) > 1024 || name == "" || strings.HasPrefix(name, "/") || !iconThemeDirRegexp.MatchString(name) {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if len(component) > 255 || component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

// validIcon checks a file's signature for its extension.
func validIcon(content []byte, ext string) bool {
	if ext == "png" {
		return bytes.HasPrefix(content, pngSignature)
	}
	if len(content) > 4096 {
		content = content[:4096]
	}
	return bytes.Contains(content, []byte("<svg"))
}

// writeAppIcon copies the icon into the machine's icon directory, replacing the
// APP's other icon, and returns its host path.
func (a *app) writeAppIcon(m *machineRecord, app string, icon *appIcon) (string, error) {
	dir := a.appsDir(m.Name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := checkPrivateDir(dir, a.uid); err != nil {
		return "", err
	}
	path := filepath.Join(dir, app+"."+icon.ext)
	if err := atomicWrite(path, icon.data, 0600); err != nil {
		return "", err
	}
	other := map[string]string{"png": "svg", "svg": "png"}[icon.ext]
	if err := os.Remove(filepath.Join(dir, app+"."+other)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}

// removeAppIcons deletes the APP's icons and the machine's icon directory when
// it is then empty.
func (a *app) removeAppIcons(m *machineRecord, app string) error {
	dir := a.appsDir(m.Name)
	// A missing directory holds no icons. A symlink or a foreign directory is
	// refused, so that nothing outside nsl's own state is touched.
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := checkPrivateDir(dir, a.uid); err != nil {
		return err
	}
	for _, ext := range []string{"png", "svg"} {
		if err := os.Remove(filepath.Join(dir, app+"."+ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) {
		return err
	}
	return nil
}
