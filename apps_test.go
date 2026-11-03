package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frostyard/nsl/internal/protocol"
)

// commandExit is a machine command's own exit status, as ssh reports it.
type commandExit int

func (e commandExit) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e commandExit) ExitCode() int { return int(e) }

const (
	svgIcon = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"></svg>`
	pngIcon = "\x89PNG\r\n\x1a\n" + "the rest of the image"
)

// guestApp is a machine's application entry: a quoted argument with a space, a
// %U that is dropped, %% that becomes %, and a literal backslash, which the
// format writes as four backslashes inside a quoted argument.
func guestApp(name string) string {
	return `[Desktop Entry]
Type=Application
Name=` + name + `
GenericName=Calculator
Comment=Perform arithmetic, scientific or financial calculations
Exec=gnome-calculator --title "My Screen" %U --note=%% "a\\\\b"
Icon=org.gnome.Calculator
Categories=Utility;Calculator;
Keywords=calculate;math;
StartupWMClass=Gnome-calculator
`
}

// guestFiles answers cat from a map of machine paths, as the agent's run
// operation does: exit status 1 when the file is absent.
func guestFiles(f *fakeRunner, files map[string]string) {
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op != "run" || len(req.Argv) != 3 || req.Argv[0] != "cat" || req.Argv[1] != "--" {
			return nil
		}
		content, ok := files[req.Argv[2]]
		if !ok {
			return commandExit(1)
		}
		_, err := io.WriteString(stdout, content)
		if err != nil {
			return err
		}
		return errAnswered
	}
}

// writeHostFile puts a file in the user's applications directory, as another
// program or an older nsl would have.
func writeHostFile(t *testing.T, a *app, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(a.applicationsDir(), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.applicationsDir(), name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// appEntryBody is a guest desktop entry with the keys a test sets.
func appEntryBody(keys ...string) string {
	return "[Desktop Entry]\nType=Application\nName=Calculator\n" + strings.Join(keys, "\n") + "\n"
}

func TestAppAddWritesTheHostEntry(t *testing.T) {
	a, f, m := withMachine(t)
	var out bytes.Buffer
	a.out = &out
	// No nsl on PATH: the entry names the running binary (a.self).
	t.Setenv("PATH", t.TempDir())
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop":            guestApp("Calculator"),
		"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": svgIcon,
	}
	guestFiles(f, files)
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err != nil {
		t.Fatal(err)
	}
	icon := filepath.Join(a.machinesDir(), "debian.apps", "org.gnome.Calculator.svg")
	want := `[Desktop Entry]
Type=Application
Name=Calculator (debian)
Exec=/test/nsl run -m debian --cd /home/u -- gnome-calculator --title "My Screen" --note=%% "a\\\\b"
TryExec=/test/nsl
Terminal=false
GenericName=Calculator
Comment=Perform arithmetic, scientific or financial calculations
Icon=` + icon + `
Categories=Utility;Calculator;
Keywords=calculate;math;
StartupWMClass=Gnome-calculator
X-Nsl-Machine=debian
X-Nsl-Machine-ID=` + m.ID + `
X-Nsl-App=org.gnome.Calculator
`
	entry := a.menuEntryPath("debian", "org.gnome.Calculator")
	if got, err := os.ReadFile(entry); err != nil || string(got) != want {
		t.Fatalf("%v\n%s", err, got)
	}
	if st, err := os.Stat(entry); err != nil || st.Mode().Perm() != 0644 {
		t.Fatal(st, err)
	}
	if st, err := os.Stat(icon); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, err)
	}
	if st, err := os.Stat(filepath.Dir(icon)); err != nil || st.Mode().Perm() != 0700 {
		t.Fatal(st, err)
	}
	if data, err := os.ReadFile(icon); err != nil || string(data) != svgIcon {
		t.Fatalf("%v %q", err, data)
	}
	if !strings.Contains(out.String(), "Added Calculator (debian) to the menu as nsl-debian-org.gnome.Calculator.desktop") {
		t.Fatal(out.String())
	}
	// The guest reads are cat as the machine account, never as root.
	read := false
	for _, r := range f.requests {
		if r.Op != "run" || len(r.Argv) != 3 || r.Argv[0] != "cat" || r.Argv[1] != "--" || r.Argv[2] != "/usr/share/applications/org.gnome.Calculator.desktop" {
			continue
		}
		read = true
		if r.Machine != "debian" || r.ID != m.ID || r.Root || r.Directory != "/" || r.IdleTimeout == nil {
			t.Fatalf("%+v", r)
		}
	}
	if !read {
		t.Fatal("did not read the entry with cat")
	}
}

// addedEntry runs app add for APP with the files a test provides and returns
// the host entry's path.
func addedEntry(t *testing.T, a *app, f *fakeRunner, app string) string {
	t.Helper()
	if err := a.execute([]string{"app", "add", app}); err != nil {
		t.Fatal(err)
	}
	return a.menuEntryPath("debian", app)
}

func TestAppAddFindsIcons(t *testing.T) {
	for _, tc := range []struct {
		name    string
		icon    string
		files   map[string]string
		key     string // the Icon the entry names, empty when it has none
		content string // the icon bytes, when the test tells sizes apart
	}{
		{
			name:  "scalable svg before a png",
			icon:  "org.gnome.Calculator",
			files: map[string]string{"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": svgIcon, "/usr/share/icons/hicolor/512x512/apps/org.gnome.Calculator.png": pngIcon},
			key:   "org.gnome.Calculator.svg",
		},
		{
			name:    "the largest png without an svg",
			icon:    "org.gnome.Calculator",
			files:   map[string]string{"/usr/share/icons/hicolor/48x48/apps/org.gnome.Calculator.png": pngIcon + "48", "/usr/share/icons/hicolor/256x256/apps/org.gnome.Calculator.png": pngIcon + "256"},
			key:     "org.gnome.Calculator.png",
			content: pngIcon + "256",
		},
		{
			name:    "an absolute path",
			icon:    "/usr/share/pixmaps/org.gnome.Calculator.png",
			files:   map[string]string{"/usr/share/pixmaps/org.gnome.Calculator.png": pngIcon},
			key:     "org.gnome.Calculator.png",
			content: pngIcon,
		},
		{
			name:  "an entry without an icon key",
			icon:  "",
			files: map[string]string{},
		},
		{
			name:  "a name that is found nowhere",
			icon:  "org.gnome.Calculator",
			files: map[string]string{},
		},
		{
			name:  "a bad png signature is skipped",
			icon:  "org.gnome.Calculator",
			files: map[string]string{"/usr/share/icons/hicolor/512x512/apps/org.gnome.Calculator.png": "not a png", "/usr/share/icons/hicolor/48x48/apps/org.gnome.Calculator.png": pngIcon},
			key:   "org.gnome.Calculator.png",
		},
		{
			name:  "an svg without <svg is refused",
			icon:  "org.gnome.Calculator",
			files: map[string]string{"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": "<html></html>"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f, _ := withMachine(t)
			files := map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": strings.Replace(guestApp("Calculator"), "Icon=org.gnome.Calculator", "Icon="+tc.icon, 1)}
			for k, v := range tc.files {
				files[k] = v
			}
			guestFiles(f, files)
			var stderr bytes.Buffer
			a.err = &stderr
			body, err := os.ReadFile(addedEntry(t, a, f, "org.gnome.Calculator"))
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if tc.key != "" {
				want = filepath.Join(a.machinesDir(), "debian.apps", tc.key)
			}
			if strings.Contains(string(body), "Icon=") != (want != "") || (want != "" && !strings.Contains(string(body), "\nIcon="+want+"\n")) {
				t.Fatalf("icon %q in\n%s", want, body)
			}
			noted := strings.Contains(stderr.String(), "no icon")
			if (tc.icon != "" && want == "") != noted {
				t.Fatalf("note %q, want icon %q", stderr.String(), want)
			}
			if want != "" {
				if st, err := os.Stat(want); err != nil || st.Mode().Perm() != 0600 {
					t.Fatal(st, err)
				}
			}
			if tc.content != "" {
				if data, err := os.ReadFile(want); err != nil || string(data) != tc.content {
					t.Fatalf("%v %q", err, data)
				}
			}
		})
	}
}

func TestAppAddPrefersTheAccountsOwnEntry(t *testing.T) {
	a, f, _ := withMachine(t)
	files := map[string]string{
		"/home/u/.local/share/applications/org.gnome.Calculator.desktop": guestApp("My Calculator"),
		"/usr/share/applications/org.gnome.Calculator.desktop":           guestApp("Package Calculator"),
	}
	guestFiles(f, files)
	body, err := os.ReadFile(addedEntry(t, a, f, "org.gnome.Calculator"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Name=My Calculator (debian)\n") {
		t.Fatalf("%s", body)
	}
	// A trailing .desktop names the same application, at the same path.
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator.desktop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.menuEntryPath("debian", "org.gnome.Calculator")); err != nil {
		t.Fatal(err)
	}
}

func TestAppAddRefusesWhatItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		app   string
		want  string
	}{
		{"shell syntax", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": appEntryBody(`Exec=sh -c 'echo hi'`)}, "org.gnome.Calculator", "shell syntax"},
		{"another field code", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": appEntryBody("Exec=gimp --file=%f")}, "org.gnome.Calculator", "field code"},
		{"a stray quote", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": appEntryBody(`Exec=calc --title="My Screen"`)}, "org.gnome.Calculator", "shell syntax"},
		{"an unsupported escape", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": appEntryBody(`Exec=calc "\q"`)}, "org.gnome.Calculator", "escape"},
		{"hidden", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": appEntryBody("Exec=calc", "Hidden=true")}, "org.gnome.Calculator", "Hidden"},
		{"a link", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": "[Desktop Entry]\nType=Link\nName=Calculator\nExec=calc\n"}, "org.gnome.Calculator", "not Application"},
		{"no exec", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": "[Desktop Entry]\nType=Application\nName=Calculator\n"}, "org.gnome.Calculator", "no Exec"},
		{"another first group", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": "[Desktop Action new]\nName=New\n[Desktop Entry]\nType=Application\nName=Calculator\nExec=calc\n"}, "org.gnome.Calculator", "[Desktop Entry]"},
		{"absent everywhere", map[string]string{}, "org.gnome.Calculator", "no org.gnome.Calculator.desktop in debian"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f, _ := withMachine(t)
			guestFiles(f, tc.files)
			err := a.execute([]string{"app", "add", tc.app})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
			if _, err := os.Lstat(a.applicationsDir()); !os.IsNotExist(err) {
				t.Fatal("wrote a host entry for a refused application")
			}
		})
	}
}

func TestAppAddRefusesAnInvalidName(t *testing.T) {
	a, _, _ := withMachine(t)
	for _, app := range []string{"../x", "x/y", "", strings.Repeat("a", 201), "x y"} {
		if err := a.execute([]string{"app", "add", app}); err == nil {
			t.Fatal("accepted APP", app)
		}
	}
	// A flag where APP belongs is not a name either.
	if err := a.execute([]string{"app", "add", "-x"}); err == nil {
		t.Fatal("accepted a flag as APP")
	}
}

func TestAppAddRefusesAnIsolatedOrUnpreparedMachine(t *testing.T) {
	a, f, m := withMachine(t)
	files := map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": guestApp("Calculator")}
	guestFiles(f, files)
	m.Tier = "isolated"
	if err := a.saveMachine(m); err != nil {
		t.Fatal(err)
	}
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "isolated") {
		t.Fatal(err)
	}
	if len(f.requests) != 0 {
		t.Fatal("read from an isolated machine")
	}
	m.Tier = "shared"
	m.Prepared = false
	if err := a.saveMachine(m); err != nil {
		t.Fatal(err)
	}
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatal(err)
	}
	// Until a machine exists there is no default machine to read from.
	if err := a.setDefault(""); err != nil {
		t.Fatal(err)
	}
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "no default machine") {
		t.Fatal(err)
	}
}

func TestAppAddNeverReplacesAFileItDidNotWrite(t *testing.T) {
	a, f, m := withMachine(t)
	files := map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": guestApp("Calculator")}
	guestFiles(f, files)
	entry := a.menuEntryPath("debian", "org.gnome.Calculator")
	foreign := []byte("[Desktop Entry]\nType=Application\nName=Mine\nExec=mine\n")
	writeHostFile(t, a, filepath.Base(entry), string(foreign))
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "did not write") {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(entry); err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("%v %q", err, got)
	}
	if _, err := os.Lstat(a.appsDir("debian")); !os.IsNotExist(err) {
		t.Fatal("wrote an icon for a refused entry")
	}
	// A symlink at the path is not owned, and nsl writes through nothing.
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, foreign, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, entry); err != nil {
		t.Fatal(err)
	}
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "did not write") {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("wrote through a symlink: %v %q", err, got)
	}
	// An entry of another ID is another machine's file.
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	writeHostFile(t, a, filepath.Base(entry), "[Desktop Entry]\nType=Application\nName=Calculator (debian)\n"+
		"Exec=/test/nsl run -m debian --cd /home/u -- calc\nTryExec=/test/nsl\nTerminal=false\n"+
		"X-Nsl-Machine=debian\nX-Nsl-Machine-ID="+strings.Repeat("0", 32)+"\nX-Nsl-App=org.gnome.Calculator\n")
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "did not write") {
		t.Fatal(err)
	}
	if _, owned, err := a.ownedEntry(entry, m, "org.gnome.Calculator"); err != nil || owned {
		t.Fatal("accepted an entry of another ID", err)
	}
}

func TestAppAddRefreshesAnOwnedEntry(t *testing.T) {
	a, f, _ := withMachine(t)
	svg := "/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg"
	png := "/usr/share/icons/hicolor/48x48/apps/org.gnome.Calculator.png"
	files := map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": guestApp("Calculator"), svg: svgIcon}
	guestFiles(f, files)
	entry := addedEntry(t, a, f, "org.gnome.Calculator")
	// The same APP again replaces the entry nsl wrote and its icon.
	files["/usr/share/applications/org.gnome.Calculator.desktop"] = guestApp("Calculator Deluxe")
	files[png] = pngIcon
	delete(files, svg)
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Name=Calculator Deluxe (debian)\n") {
		t.Fatalf("%s", body)
	}
	if !strings.Contains(string(body), "\nIcon="+filepath.Join(a.machinesDir(), "debian.apps", "org.gnome.Calculator.png")+"\n") {
		t.Fatalf("%s", body)
	}
	if _, err := os.Lstat(filepath.Join(a.machinesDir(), "debian.apps", "org.gnome.Calculator.svg")); !os.IsNotExist(err) {
		t.Fatal("kept the old svg icon")
	}
}

func TestAppRemoveDeletesTheEntryAndItsIcons(t *testing.T) {
	a, f, m := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop":            guestApp("Calculator"),
		"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": svgIcon,
	}
	guestFiles(f, files)
	entry := addedEntry(t, a, f, "org.gnome.Calculator")
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"app", "remove", "org.gnome.Calculator"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Removed nsl-debian-org.gnome.Calculator.desktop") {
		t.Fatal(out.String())
	}
	if _, err := os.Lstat(entry); !os.IsNotExist(err) {
		t.Fatal("kept the entry")
	}
	if _, err := os.Lstat(a.appsDir("debian")); !os.IsNotExist(err) {
		t.Fatal("kept the icons")
	}
	if err := a.execute([]string{"app", "remove", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "no menu entry") {
		t.Fatal(err)
	}
	// A file nsl did not write is refused, not deleted.
	foreign := "[Desktop Entry]\nType=Application\nName=Mine\nExec=mine\n"
	writeHostFile(t, a, filepath.Base(entry), foreign)
	if err := a.execute([]string{"app", "remove", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "did not write") {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(entry); err != nil || string(got) != foreign {
		t.Fatalf("%v %q", err, got)
	}
	if _, owned, err := a.ownedEntry(entry, m, "org.gnome.Calculator"); err != nil || owned {
		t.Fatal("a refused entry is not owned", err)
	}
}

func TestAppListShowsOnlyOwnedEntries(t *testing.T) {
	a, f, _ := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop": guestApp("Calculator"),
		"/usr/share/applications/galculator.desktop":           guestApp("Galculator"),
	}
	guestFiles(f, files)
	addedEntry(t, a, f, "org.gnome.Calculator")
	addedEntry(t, a, f, "galculator")
	// Files nsl did not write stay out of the list.
	writeHostFile(t, a, "nsl-debian-other.desktop", "[Desktop Entry]\nType=Application\nName=Other\nExec=other\n")
	other := strings.Replace(guestApp("Calculator"), "[Desktop Entry]\nType=Application\nName=Calculator\n",
		"[Desktop Entry]\nType=Application\nName=Calculator (debian-2)\nX-Nsl-Machine=debian-2\nX-Nsl-Machine-ID="+strings.Repeat("0", 32)+"\nX-Nsl-App=org.gnome.Calculator\n", 1)
	writeHostFile(t, a, "nsl-debian-2-org.gnome.Calculator.desktop", other)
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"app", "list"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "MACHINE  APP") {
		t.Fatalf("%q", lines)
	}
	// Sorted by machine and APP, with the Name last.
	for i, want := range [][3]string{{"debian", "galculator", "Galculator (debian)"}, {"debian", "org.gnome.Calculator", "Calculator (debian)"}} {
		if line := lines[i+1]; !strings.HasPrefix(line, want[0]) || !strings.Contains(line, want[1]) || !strings.HasSuffix(line, want[2]) {
			t.Fatalf("%q", line)
		}
	}
	// -m filters to one machine, and never lists another machine's entry.
	out.Reset()
	if err := a.execute([]string{"app", "list", "-m", "debian"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "debian-2") || !strings.Contains(out.String(), "galculator") {
		t.Fatal(out.String())
	}
	if err := a.execute([]string{"app", "list", "-m", "ghost"}); err == nil {
		t.Fatal("listed the entries of a machine that does not exist")
	}
	// A machine with no entries says how to add one.
	if err := a.setDefault(""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a.machinePath("debian")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.execute([]string{"app", "list"}); err != nil || out.String() != "No menu entries; add one with nsl app add [-m NAME] APP\n" {
		t.Fatal(err, out.String())
	}
	if err := a.execute([]string{"app", "list", "x"}); err == nil {
		t.Fatal("accepted an argument")
	}
}

func TestRemoveDeletesTheMachinesMenuEntries(t *testing.T) {
	a, f, _ := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop":            guestApp("Calculator"),
		"/usr/share/applications/galculator.desktop":                      guestApp("Galculator"),
		"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": svgIcon,
	}
	guestFiles(f, files)
	addedEntry(t, a, f, "org.gnome.Calculator")
	addedEntry(t, a, f, "galculator")
	foreign := "[Desktop Entry]\nType=Application\nName=Mine\nExec=mine\n"
	unrelated := writeHostFile(t, a, "nsl-debian-x.desktop", foreign)
	// The entries of a machine whose name shares the prefix stay.
	neighbour := strings.Replace(guestApp("Calculator"), "[Desktop Entry]\nType=Application\nName=Calculator\n",
		"[Desktop Entry]\nType=Application\nName=Calculator (debian-2)\nX-Nsl-Machine=debian-2\nX-Nsl-Machine-ID="+strings.Repeat("0", 32)+"\nX-Nsl-App=org.gnome.Calculator\n", 1)
	kept := writeHostFile(t, a, "nsl-debian-2-org.gnome.Calculator.desktop", neighbour)
	var out bytes.Buffer
	a.out = &out
	if err := a.remove([]string{"debian"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Its 2 menu entries are removed too.") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := a.remove([]string{"debian", "--yes"}); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"org.gnome.Calculator", "galculator"} {
		if _, err := os.Lstat(a.menuEntryPath("debian", app)); !os.IsNotExist(err) {
			t.Fatal("kept the entry of", app)
		}
	}
	if _, err := os.Lstat(a.appsDir("debian")); !os.IsNotExist(err) {
		t.Fatal("kept the icons")
	}
	for _, path := range []string{unrelated, kept} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatal("removed", path, err)
		}
	}
}

func TestExecArgs(t *testing.T) {
	for _, tc := range []struct {
		exec string
		want []string
	}{
		{"calc", []string{"calc"}},
		{"calc   --x", []string{"calc", "--x"}},
		{`calc "a b"`, []string{"calc", "a b"}},
		{`calc "a\\b"`, []string{"calc", `a\b`}},
		{`calc "say \"hi\""`, []string{"calc", `say "hi"`}},
		{"calc %U", []string{"calc"}},
		{"%f calc %i", []string{"calc"}},
		{"calc --note=%%", []string{"calc", "--note=%"}},
		{`calc ""`, []string{"calc", ""}},
	} {
		got, err := execArgs(tc.exec)
		if err != nil || strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("%q: %q %v", tc.exec, got, err)
		}
	}
	for _, bad := range []string{"", "%U", "cal'c", `calc a\b`, "calc > out", `calc "a b`, `calc "a\qb"`, "calc --file=%f", "calc %", "calc percent%"} {
		if got, err := execArgs(bad); err == nil {
			t.Fatalf("%q: %q", bad, got)
		}
	}
}

func TestQuoteExecArg(t *testing.T) {
	for _, tc := range []struct{ arg, want string }{
		{"calc", "calc"},
		{"--note=%", "--note=%"},
		{"a b", `"a b"`},
		{"", `""`},
		{`a\b`, `"a\\b"`},
		{`a"b`, `"a\"b"`},
		{"it's", `"it's"`},
		{"$(x)", `"\$(x)"`},
		{"a\tb", "\"a\tb\""},
	} {
		if got := quoteExecArg(tc.arg); got != tc.want {
			t.Fatalf("%q: %q, want %q", tc.arg, got, tc.want)
		}
	}
}

func TestDesktopValueEscapes(t *testing.T) {
	for _, tc := range []struct{ value, encoded string }{
		{"Calculator", "Calculator"},
		{`a\b`, `a\\b`},
		{"a\nb", `a\nb`},
		{" a", `\sa`},
		{"a  b", `a\s b`},
		{"a b", "a b"},
	} {
		if got := encodeDesktopValue(tc.value); got != tc.encoded {
			t.Fatalf("%q: %q, want %q", tc.value, got, tc.encoded)
		}
		back, err := decodeDesktopValue(tc.encoded)
		if err != nil || back != tc.value {
			t.Fatalf("%q: %q %v", tc.encoded, back, err)
		}
	}
	if _, err := decodeDesktopValue(`a\nb`); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeDesktopValue(`a\qb`); err == nil {
		t.Fatal("accepted an unknown escape")
	}
	content := "[Desktop Entry]\nType=Application\nName=A\\sB\nExec=calc\n"
	entry, err := parseDesktopEntry([]byte(content))
	if err != nil || entry.Name != "A B" {
		t.Fatal(entry, err)
	}
	// Spaces around the equals sign are not part of the key or the value.
	entry, err = parseDesktopEntry([]byte("[Desktop Entry]\nType = Application\nName =  Calc\nExec= calc\n"))
	if err != nil || entry.Name != "Calc" || entry.Exec != "calc" {
		t.Fatal(entry, err)
	}
	if _, err := parseDesktopEntry([]byte("[Desktop Entry]\nType=Application\nName=A\\nB\nExec=calc\n")); err == nil {
		t.Fatal("accepted a control character")
	}
}

func TestAppEntryNamesTheNslToRun(t *testing.T) {
	a, f, _ := withMachine(t)
	guestFiles(f, map[string]string{"/usr/share/applications/galculator.desktop": guestApp("Galculator")})
	bin := t.TempDir()
	nslPath := filepath.Join(bin, "nsl")
	if err := os.WriteFile(nslPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	body, err := os.ReadFile(addedEntry(t, a, f, "galculator"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nExec=" + nslPath + " run -m debian ", "\nTryExec=" + nslPath + "\n"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("missing %q in\n%s", want, body)
		}
	}
	// Without nsl on PATH the running binary is named instead.
	t.Setenv("PATH", t.TempDir())
	if body, err = os.ReadFile(addedEntry(t, a, f, "galculator")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "\nTryExec="+a.self+"\n") {
		t.Fatalf("%s", body)
	}
}

func TestAppCommandUsage(t *testing.T) {
	a, _, _ := withMachine(t)
	for _, args := range [][]string{{"app"}, {"app", "nope"}, {"app", "add"}, {"app", "add", "--y", "x"}, {"app", "list", "x"}, {"app", "remove"}} {
		if err := a.execute(args); err == nil {
			t.Fatal("accepted", args)
		}
	}
	if err := a.execute([]string{"help"}); err != nil {
		t.Fatal(err)
	}
}

func TestAppAddStopsOnACatFailure(t *testing.T) {
	a, f, _ := withMachine(t)
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "run" {
			return errors.New("the machine is busy")
		}
		return nil
	}
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "reading") {
		t.Fatal(err)
	}
	// An exit status other than 1 stops the command too.
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "run" {
			return commandExit(2)
		}
		return nil
	}
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Fatal(err)
	}
	// A guest file larger than the limit is refused.
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "run" {
			for range 70 {
				if _, err := io.WriteString(stdout, strings.Repeat("x", 1024)); err != nil {
					return err
				}
			}
			return errAnswered
		}
		return nil
	}
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatal(err)
	}
}
