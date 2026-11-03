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
	appUsage = "usage: app add [-m NAME] APP | app remove [-m NAME] APP | app list [-m NAME]"

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
	argv, err := execArgs(guest.Exec)
	if err != nil {
		return fmt.Errorf("%s in %s: %w", path, m.Name, err)
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
	body := renderMenuEntry(menuInput{Machine: m.Name, ID: m.ID, App: app, Guest: guest, Argv: argv,
		Dir: dir, Nsl: a.nslPath(), Icon: iconPath})
	if err = a.publishMenuEntry(entryPath, body, m, app); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Added %s (%s) to the menu as %s\n", guest.Name, m.Name, filepath.Base(entryPath))
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
				mine = append(mine, menuEntry{Machine: m.Name, App: entry.App, Name: entry.Name})
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
	fmt.Fprintln(t, "MACHINE\tAPP\tNAME")
	for _, e := range entries {
		fmt.Fprintf(t, "%s\t%s\t%s\n", e.Machine, e.App, e.Name)
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

// guestFile reads PATH in the machine with cat through the agent's run
// operation. found is false when cat's exit status is 1, which is how cat
// reports a missing file; every other failure stops the command.
func (a *app) guestFile(v *vmRecord, m *machineRecord, path string, limit int) ([]byte, bool, error) {
	idle, err := a.idleTimeout()
	if err != nil {
		return nil, false, err
	}
	encoded, err := protocol.Encode(protocol.Request{Protocol: protocol.Version, Op: "run", Machine: m.Name, ID: m.ID,
		Argv: []string{"cat", "--", path}, Directory: "/", IdleTimeout: idle})
	if err != nil {
		return nil, false, err
	}
	out := &boundedBuffer{limit: limit + 1}
	err = a.r.run(context.Background(), nil, out, io.Discard, os.Environ(), "ssh", append(a.sshArgs(v, false), encoded)...)
	if err != nil {
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
		StartupWMClass: values["StartupWMClass"], Exec: values["Exec"], Path: values["Path"],
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

// fieldCodes are the Exec field codes nsl drops (ADR-0022): files and URLs are
// not translated yet.
var fieldCodes = map[string]bool{"%f": true, "%F": true, "%u": true, "%U": true, "%i": true, "%c": true,
	"%k": true, "%d": true, "%D": true, "%n": true, "%N": true, "%v": true, "%m": true}

// unquotedSyntax are the characters a desktop's Exec may not pass unquoted.
// The double quote belongs here although the contract's list omits it: quoting
// part of an argument is not allowed, and passing it through would split the
// argument at its next space rather than refuse it.
const unquotedSyntax = "'\\\"><~|&;$*?#()`"

// execArgs splits a desktop entry's Exec into arguments and removes the field
// codes. Shell syntax is refused rather than guessed.
func execArgs(execLine string) ([]string, error) {
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
						return nil, errors.New("the Exec ends with a backslash")
					}
					if n := execLine[i+1]; strings.ContainsRune(execQuotedEscapes, rune(n)) {
						b.WriteByte(n)
						i += 2
					} else {
						return nil, fmt.Errorf("the Exec quotes the unsupported escape %q", `\`+string(n))
					}
				default:
					b.WriteByte(execLine[i])
					i++
				}
			}
			if !closed {
				return nil, errors.New("the Exec has an unclosed quote")
			}
			// A quote encloses a whole argument, so a space or the end of
			// the line must follow it.
			if i < len(execLine) && execLine[i] != ' ' {
				return nil, errors.New("the Exec quotes part of an argument, which nsl does not support")
			}
			split = append(split, b.String())
			continue
		}
		start := i
		for i < len(execLine) && execLine[i] != ' ' {
			if strings.ContainsRune(unquotedSyntax, rune(execLine[i])) {
				return nil, fmt.Errorf("the Exec argument %q uses shell syntax or quotes part of an argument", execLine[start:i+1])
			}
			i++
		}
		split = append(split, execLine[start:i])
	}
	var args []string
	for _, arg := range split {
		if fieldCodes[arg] {
			continue
		}
		expanded, err := expandFieldCodes(arg)
		if err != nil {
			return nil, err
		}
		args = append(args, expanded)
	}
	if len(args) == 0 {
		return nil, errors.New("the Exec has no command")
	}
	return args, nil
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
	Guest            *desktopEntry
	Argv             []string // the guest Exec, split and without field codes
	Dir              string   // the machine directory the command starts in
	Nsl              string   // the absolute path of nsl
	Icon             string   // the host path of the icon, or ""
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
	parts := []string{in.Nsl, "run", "-m", in.Machine, "--cd", in.Dir, "--"}
	parts = append(parts, in.Argv...)
	for i, part := range parts {
		parts[i] = quoteExecArg(part)
	}
	return strings.ReplaceAll(strings.Join(parts, " "), "%", "%%")
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

// readGuestIcon finds the machine's icon. A nil icon means none was found.
func (a *app) readGuestIcon(v *vmRecord, m *machineRecord, icon string) (*appIcon, error) {
	for _, candidate := range iconCandidates(icon) {
		content, found, err := a.guestFile(v, m, candidate.path, appIconLimit)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if !validIcon(content, candidate.ext) {
			fmt.Fprintf(a.err, "nsl: %s in %s is not a %s image\n", candidate.path, m.Name, strings.ToUpper(candidate.ext))
			continue
		}
		return &appIcon{data: content, ext: candidate.ext}, nil
	}
	return nil, nil
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
