package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
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
// %U file field, %% that becomes %, and a literal backslash, which the format
// writes as four backslashes inside a quoted argument.
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
		if req.Op != "run" {
			return nil
		}
		if len(req.Argv) >= 4 && req.Argv[0] == "ls" {
			switch req.Argv[1] {
			case "-1":
				if req.Argv[2] != "--" {
					return nil
				}
				dir := filepath.Clean(req.Argv[3])
				var names []string
				seen := map[string]bool{}
				prefix := dir + "/"
				for path := range files {
					if !strings.HasPrefix(path, prefix) {
						continue
					}
					rest := strings.TrimPrefix(path, prefix)
					name := strings.SplitN(rest, "/", 2)[0]
					if name != "" && !seen[name] {
						seen[name] = true
						names = append(names, name)
					}
				}
				sort.Strings(names)
				if len(names) == 0 {
					return commandExit(2)
				}
				_, err := io.WriteString(stdout, strings.Join(names, "\n")+"\n")
				if err != nil {
					return err
				}
				return errAnswered
			case "-1d":
				if req.Argv[2] != "--" {
					return nil
				}
				missing := false
				var found []string
				for _, path := range req.Argv[3:] {
					if _, ok := files[path]; ok {
						found = append(found, path)
					} else {
						missing = true
					}
				}
				if len(found) != 0 {
					if _, err := io.WriteString(stdout, strings.Join(found, "\n")+"\n"); err != nil {
						return err
					}
				}
				if missing {
					return commandExit(2)
				}
				return errAnswered
			}
		}
		if len(req.Argv) != 3 || req.Argv[0] != "cat" || req.Argv[1] != "--" {
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
Exec=/test/nsl run -m debian --cd /home/u --host-files U:3:2 -- gnome-calculator --title "My Screen" %U --note=%% "a\\\\b"
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

func TestAppAddCopiesMimeTypes(t *testing.T) {
	a, f, _ := withMachine(t)
	body := appEntryBody("Exec=evince %F", "MimeType=image/png;;image/jpeg;image/png;")
	guestFiles(f, map[string]string{"/usr/share/applications/org.gnome.Evince.desktop": body})
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"app", "add", "--mime-types", "org.gnome.Evince"}); err != nil {
		t.Fatal(err)
	}
	entry, err := os.ReadFile(a.menuEntryPath("debian", "org.gnome.Evince"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(entry)
	if !strings.Contains(text, "MimeType=image/png;image/jpeg;\n") {
		t.Fatalf("missing normalized MimeType in\n%s", text)
	}
	if !strings.Contains(text, "--host-files F:1:0") {
		t.Fatalf("lost --host-files in\n%s", text)
	}
	if !strings.Contains(out.String(), "for 2 file types") {
		t.Fatal(out.String())
	}
}

func TestAppAddDoesNotCopyMimeTypesWithoutFlag(t *testing.T) {
	a, f, _ := withMachine(t)
	body := appEntryBody("Exec=evince %F", "MimeType=image/png;image/jpeg;")
	guestFiles(f, map[string]string{"/usr/share/applications/org.gnome.Evince.desktop": body})
	if err := a.execute([]string{"app", "add", "org.gnome.Evince"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(a.menuEntryPath("debian", "org.gnome.Evince"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "MimeType=") {
		t.Fatalf("host entry copied MimeType without the flag:\n%s", content)
	}
}

func TestAppAddRefreshDropsMimeTypesWithoutFlag(t *testing.T) {
	a, f, _ := withMachine(t)
	body := appEntryBody("Exec=evince %F", "MimeType=image/png;image/jpeg;")
	guestFiles(f, map[string]string{"/usr/share/applications/org.gnome.Evince.desktop": body})
	if err := a.execute([]string{"app", "add", "--mime-types", "org.gnome.Evince"}); err != nil {
		t.Fatal(err)
	}
	entry := a.menuEntryPath("debian", "org.gnome.Evince")
	content, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "MimeType=") {
		t.Fatalf("first entry has no MimeType:\n%s", content)
	}
	if err := a.execute([]string{"app", "add", "org.gnome.Evince"}); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "MimeType=") {
		t.Fatalf("refresh kept MimeType:\n%s", content)
	}
}

func TestAppAddRefusesMimeTypes(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"no MimeType", appEntryBody("Exec=evince %F", "Icon=org.gnome.Calculator"), "no MimeType"},
		{"only empty items", appEntryBody("Exec=evince %F", "MimeType=;;", "Icon=org.gnome.Calculator"), "only empty items"},
		{"Exec without a file field", appEntryBody("Exec=calc", "MimeType=application/pdf", "Icon=org.gnome.Calculator"), "no %f, %F, %u or %U"},
		{"wildcard", appEntryBody("Exec=evince %F", "MimeType=image/*", "Icon=org.gnome.Calculator"), "TYPE/SUBTYPE"},
		{"parameter", appEntryBody("Exec=evince %F", "MimeType=text/plain;charset=utf-8", "Icon=org.gnome.Calculator"), "TYPE/SUBTYPE"},
		{"no slash", appEntryBody("Exec=evince %F", "MimeType=image", "Icon=org.gnome.Calculator"), "TYPE/SUBTYPE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f, _ := withMachine(t)
			guestFiles(f, map[string]string{
				"/usr/share/applications/org.gnome.Calculator.desktop":            tc.body,
				"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": svgIcon,
			})
			err := a.execute([]string{"app", "add", "--mime-types", "org.gnome.Calculator"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
			if _, err := os.Lstat(a.applicationsDir()); !os.IsNotExist(err) {
				t.Fatal("wrote a host entry for refused MIME types")
			}
			noIcon(t, a)
		})
	}
}

func TestMenuMimeTypesPartLength(t *testing.T) {
	field := &execField{Code: "F", Index: 1}
	valid, err := menuMimeTypes(strings.Repeat("a", 127)+"/png", field)
	if err != nil || valid != strings.Repeat("a", 127)+"/png;" {
		t.Fatalf("127-character part: %q %v", valid, err)
	}
	if _, err := menuMimeTypes(strings.Repeat("a", 128)+"/png", field); err == nil {
		t.Fatal("accepted a 128-character part")
	}
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

func TestAppAddWritesFField(t *testing.T) {
	a, f, _ := withMachine(t)
	guestFiles(f, map[string]string{
		"/usr/share/applications/evince.desktop": `[Desktop Entry]
Type=Application
Name=Document Viewer
Exec=evince %f
`,
	})
	body, err := os.ReadFile(addedEntry(t, a, f, "evince"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "--host-files f:1:0 -- evince %f") {
		t.Fatalf("missing f field in\n%s", body)
	}
}

func TestAppAddFindsIconsInOtherThemes(t *testing.T) {
	a, f, _ := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop": strings.Replace(guestApp("Calculator"), "Icon=org.gnome.Calculator", "Icon=sample-icon", 1),
		"/usr/share/icons/Adwaita/index.theme": `[Icon Theme]
Directories=scalable/apps,apps/48
[scalable/apps]
Context=Applications
Type=Scalable
Size=64
[apps/48]
Context=Applications
Type=Fixed
Size=48
`,
		"/usr/share/icons/breeze/index.theme": `[Icon Theme]
Directories=apps/96,apps/128
[apps/96]
Context=Applications
Type=Fixed
Size=96
[apps/128]
Context=Applications
Type=Fixed
Size=128
`,
		"/usr/share/icons/broken/index.theme":                    `not an index theme`,
		"/usr/share/icons/Adwaita/scalable/apps/sample-icon.svg": svgIcon,
		"/usr/share/icons/breeze/apps/128/sample-icon.png":       pngIcon,
		"/usr/share/pixmaps/sample-icon.svg":                     svgIcon,
	}
	guestFiles(f, files)
	var stderr bytes.Buffer
	a.err = &stderr
	body, err := os.ReadFile(addedEntry(t, a, f, "org.gnome.Calculator"))
	if err != nil {
		t.Fatal(err)
	}
	wantIcon := filepath.Join(a.machinesDir(), "debian.apps", "org.gnome.Calculator.svg")
	if !strings.Contains(string(body), "Icon="+wantIcon+"\n") {
		t.Fatalf("missing icon in\n%s", body)
	}
	if data, err := os.ReadFile(wantIcon); err != nil || string(data) != svgIcon {
		t.Fatalf("wrong other-theme icon bytes: %v %q", err, data)
	}
	var batch []string
	for _, req := range f.requests {
		if req.Op == "run" && len(req.Argv) >= 3 && req.Argv[0] == "ls" && req.Argv[1] == "-1d" {
			batch = append([]string{}, req.Argv[3:]...)
		}
	}
	if len(batch) == 0 {
		t.Fatal("did not run the batched icon lookup")
	}
	wantPrefix := []string{
		"/usr/share/icons/Adwaita/scalable/apps/sample-icon.svg",
		"/usr/share/icons/Adwaita/scalable/apps/sample-icon.png",
		"/usr/share/icons/Adwaita/apps/48/sample-icon.svg",
		"/usr/share/icons/Adwaita/apps/48/sample-icon.png",
		"/usr/share/icons/breeze/apps/128/sample-icon.svg",
		"/usr/share/icons/breeze/apps/128/sample-icon.png",
	}
	for i, want := range wantPrefix {
		if i >= len(batch) || batch[i] != want {
			t.Fatalf("candidate %d: got %q, want %q in %v", i, batch[i], want, batch)
		}
	}
	if batch[len(batch)-2] != "/usr/share/pixmaps/sample-icon.svg" || batch[len(batch)-1] != "/usr/share/pixmaps/sample-icon.png" {
		t.Fatalf("pixmaps were not last: %v", batch)
	}

	// A hicolor icon wins and sends no theme listing request.
	a2, f2, _ := withMachine(t)
	files2 := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop":   strings.Replace(guestApp("Calculator"), "Icon=org.gnome.Calculator", "Icon=sample-icon", 1),
		"/usr/share/icons/hicolor/scalable/apps/sample-icon.svg": svgIcon,
	}
	guestFiles(f2, files2)
	if _, err := os.ReadFile(addedEntry(t, a2, f2, "org.gnome.Calculator")); err != nil {
		t.Fatal(err)
	}
	for _, req := range f2.requests {
		if req.Op == "run" && len(req.Argv) >= 2 && req.Argv[0] == "ls" {
			t.Fatalf("hicolor unexpectedly listed themes: %+v", req)
		}
	}
}

func TestAppAddRejectsDotThemeNames(t *testing.T) {
	a, f, _ := withMachine(t)
	validTheme := `[Icon Theme]
Directories=apps
[apps]
Context=Applications
Type=Scalable
Size=48
`
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op != "run" {
			return nil
		}
		if len(req.Argv) >= 3 && req.Argv[0] == "cat" && req.Argv[1] == "--" {
			switch req.Argv[2] {
			case "/usr/share/applications/org.gnome.Calculator.desktop":
				entry := strings.Replace(guestApp("Calculator"), "Icon=org.gnome.Calculator", "Icon=sample-icon", 1)
				_, err := io.WriteString(stdout, entry)
				if err != nil {
					return err
				}
				return errAnswered
			case "/usr/share/icons/.hidden/index.theme", "/usr/share/icons/../index.theme":
				_, err := io.WriteString(stdout, validTheme)
				if err != nil {
					return err
				}
				return errAnswered
			default:
				if req.Argv[2] == "/usr/share/icons/../apps/sample-icon.svg" ||
					req.Argv[2] == "/usr/share/icons/.hidden/apps/sample-icon.svg" {
					_, err := io.WriteString(stdout, svgIcon)
					if err != nil {
						return err
					}
					return errAnswered
				}
				return commandExit(1)
			}
		}
		if len(req.Argv) == 4 && req.Argv[0] == "ls" && req.Argv[1] == "-1" && req.Argv[2] == "--" && req.Argv[3] == "/usr/share/icons" {
			_, err := io.WriteString(stdout, "..\n.hidden\n")
			if err != nil {
				return err
			}
			return errAnswered
		}
		if len(req.Argv) >= 4 && req.Argv[0] == "ls" && req.Argv[1] == "-1d" && req.Argv[2] == "--" {
			_, err := io.WriteString(stdout, strings.Join(req.Argv[3:], "\n")+"\n")
			if err != nil {
				return err
			}
			return errAnswered
		}
		return nil
	}
	bodyPath := addedEntry(t, a, f, "org.gnome.Calculator")
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "\nIcon=") {
		t.Fatalf("dot-name themes gave an icon:\n%s", body)
	}
	for _, req := range f.requests {
		if req.Op != "run" {
			continue
		}
		if len(req.Argv) >= 3 && req.Argv[0] == "cat" && req.Argv[1] == "--" {
			raw := req.Argv[2]
			cleaned := path.Clean(raw)
			if strings.Contains(raw, "/../") || strings.HasSuffix(raw, "/..") || strings.Contains(raw, "/.hidden/") {
				t.Fatalf("unsafe dot theme cat request: %+v", req)
			}
			if !strings.HasSuffix(cleaned, "/.local/share/applications/org.gnome.Calculator.desktop") &&
				cleaned != "/usr/share/applications/org.gnome.Calculator.desktop" &&
				cleaned != "/usr/local/share/applications/org.gnome.Calculator.desktop" &&
				!strings.HasPrefix(cleaned, "/usr/share/icons/") && !strings.HasPrefix(cleaned, "/usr/share/pixmaps/") {
				t.Fatalf("unsafe theme cat request: %+v", req)
			}
		}
		if len(req.Argv) >= 3 && req.Argv[0] == "ls" {
			for _, arg := range req.Argv[3:] {
				raw := arg
				cleaned := path.Clean(raw)
				if strings.Contains(raw, "/../") || strings.HasSuffix(raw, "/..") || strings.Contains(raw, "/.hidden/") {
					t.Fatalf("unsafe dot theme ls request: %+v", req)
				}
				if raw == "/usr/share/icons" {
					continue
				}
				if !strings.HasPrefix(cleaned, "/usr/share/icons/") && !strings.HasPrefix(cleaned, "/usr/share/pixmaps/") {
					t.Fatalf("unsafe theme ls request: %+v", req)
				}
			}
		}
	}
}

func TestAppAddSkipsOversizedIconCandidates(t *testing.T) {
	a, f, _ := withMachine(t)
	hugeDir := "apps/" + strings.Repeat("a", 70000)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop": strings.Replace(guestApp("Calculator"), "Icon=org.gnome.Calculator", "Icon=ICON", 1),
		"/usr/share/icons/LargeTheme/index.theme": `[Icon Theme]
Directories=` + hugeDir + `
[` + hugeDir + `]
Context=Applications
Type=Scalable
Size=48
`,
		"/usr/share/pixmaps/ICON.png": pngIcon,
	}
	guestFiles(f, files)
	body, err := os.ReadFile(addedEntry(t, a, f, "org.gnome.Calculator"))
	if err != nil {
		t.Fatal(err)
	}
	wantIcon := filepath.Join(a.machinesDir(), "debian.apps", "org.gnome.Calculator.png")
	if !strings.Contains(string(body), "Icon="+wantIcon+"\n") {
		t.Fatalf("oversized candidate lookup found no pixmap icon in\n%s", body)
	}
	if data, err := os.ReadFile(wantIcon); err != nil || string(data) != pngIcon {
		t.Fatalf("wrong oversized-candidate pixmap bytes: %v %q", err, data)
	}
	for _, req := range f.requests {
		for _, arg := range req.Argv {
			if len(arg) > 4096 {
				t.Fatalf("request argument is %d bytes, exceeds 4096: %+v", len(arg), req)
			}
		}
	}
}

func TestAppAddFindsPixmapsOnlyIcon(t *testing.T) {
	a, f, _ := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop": strings.Replace(guestApp("Calculator"), "Icon=org.gnome.Calculator", "Icon=sample-icon", 1),
		"/usr/share/icons/SomeTheme/index.theme": `[Icon Theme]
Directories=apps/48
[apps/48]
Context=Applications
Type=Fixed
Size=48
`,
		"/usr/share/pixmaps/sample-icon.png": pngIcon,
	}
	guestFiles(f, files)
	body, err := os.ReadFile(addedEntry(t, a, f, "org.gnome.Calculator"))
	if err != nil {
		t.Fatal(err)
	}
	wantIcon := filepath.Join(a.machinesDir(), "debian.apps", "org.gnome.Calculator.png")
	if !strings.Contains(string(body), "Icon="+wantIcon+"\n") {
		t.Fatalf("missing pixmaps icon in\n%s", body)
	}
	if data, err := os.ReadFile(wantIcon); err != nil || string(data) != pngIcon {
		t.Fatalf("wrong pixmaps bytes: %v %q", err, data)
	}
}

func TestAppAddFindsScaledDirectoriesIcon(t *testing.T) {
	a, f, _ := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop": strings.Replace(guestApp("Calculator"), "Icon=org.gnome.Calculator", "Icon=sample-icon", 1),
		"/usr/share/icons/ScaledTheme/index.theme": `[Icon Theme]
ScaledDirectories=scalable/apps
[scalable/apps]
Context=Applications
Type=Scalable
Size=64
Scale=2
`,
		"/usr/share/icons/ScaledTheme/scalable/apps/sample-icon.png": pngIcon,
	}
	guestFiles(f, files)
	body, err := os.ReadFile(addedEntry(t, a, f, "org.gnome.Calculator"))
	if err != nil {
		t.Fatal(err)
	}
	wantIcon := filepath.Join(a.machinesDir(), "debian.apps", "org.gnome.Calculator.png")
	if !strings.Contains(string(body), "Icon="+wantIcon+"\n") {
		t.Fatalf("missing scaled-directory icon in\n%s", body)
	}
	if data, err := os.ReadFile(wantIcon); err != nil || string(data) != pngIcon {
		t.Fatalf("wrong scaled-directory bytes: %v %q", err, data)
	}
}

func TestAppAddBatchesLargeThemeCandidateLists(t *testing.T) {
	a, f, _ := withMachine(t)
	var dirs strings.Builder
	var names []string
	for i := 0; i < 1024; i++ {
		name := fmt.Sprintf("apps/very-long-directory-name-%04d", i)
		if i != 0 {
			dirs.WriteByte(',')
		}
		dirs.WriteString(name)
		names = append(names, name)
	}
	var theme strings.Builder
	theme.WriteString("[Icon Theme]\nDirectories=")
	theme.WriteString(dirs.String())
	theme.WriteByte('\n')
	for _, name := range names {
		fmt.Fprintf(&theme, "[%s]\nContext=Applications\nType=Fixed\nSize=48\n", name)
	}
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop": strings.Replace(guestApp("Calculator"), "Icon=org.gnome.Calculator", "Icon=sample-icon", 1),
		"/usr/share/icons/LargeTheme/index.theme":              theme.String(),
	}
	lastDir := names[len(names)-1]
	files["/usr/share/icons/LargeTheme/"+lastDir+"/sample-icon.png"] = pngIcon
	guestFiles(f, files)
	body, err := os.ReadFile(addedEntry(t, a, f, "org.gnome.Calculator"))
	if err != nil {
		t.Fatal(err)
	}
	wantIcon := filepath.Join(a.machinesDir(), "debian.apps", "org.gnome.Calculator.png")
	if !strings.Contains(string(body), "Icon="+wantIcon+"\n") {
		t.Fatalf("batched lookup found no icon in\n%s", body)
	}
	if data, err := os.ReadFile(wantIcon); err != nil || string(data) != pngIcon {
		t.Fatalf("wrong batched icon bytes: %v %q", err, data)
	}
	batchCount := 0
	for _, req := range f.requests {
		if req.Op == "run" && len(req.Argv) >= 3 && req.Argv[0] == "ls" && req.Argv[1] == "-1d" {
			batchCount++
			if len(req.Argv) < 4 {
				t.Fatalf("empty icon batch: %+v", req)
			}
		}
	}
	if batchCount < 2 {
		t.Fatalf("got only %d ls -1d request(s)", batchCount)
	}
}

func TestGuestReadsCharacterizeLargeThemeBatches(t *testing.T) {
	a, f, m := withMachine(t)
	icon := strings.Repeat("x", 3900)

	oversizeDir := func(letter byte) string {
		component := strings.Repeat(string([]byte{letter}), 255)
		return strings.Join([]string{component, component, component, component}, "/")
	}
	startDir := oversizeDir('a')
	insideDir := oversizeDir('b')
	endDir := oversizeDir('c')

	var names []string
	var dirs strings.Builder
	dirs.WriteString("[Icon Theme]\nDirectories=")
	addDir := func(name string) {
		if len(names) != 0 {
			dirs.WriteByte(',')
		}
		names = append(names, name)
		dirs.WriteString(name)
	}
	addDir(startDir)
	for i := 0; i < 80; i++ {
		addDir(fmt.Sprintf("apps/before-%04d", i))
	}
	addDir(insideDir)
	for i := 0; i < 80; i++ {
		addDir(fmt.Sprintf("apps/after-%04d", i))
	}
	addDir(endDir)

	var theme strings.Builder
	theme.WriteString(dirs.String())
	theme.WriteByte('\n')
	for _, name := range names {
		fmt.Fprintf(&theme, "[%s]\nContext=Applications\nType=Fixed\nSize=48\n", name)
	}
	guestFiles(f, map[string]string{
		"/usr/share/icons/CharacterizedTheme/index.theme": theme.String(),
	})

	var fallback []iconCandidate
	for _, name := range names {
		base := "/usr/share/icons/CharacterizedTheme/" + name + "/" + icon
		fallback = append(fallback, iconCandidate{base + ".svg", "svg"}, iconCandidate{base + ".png", "png"})
	}
	fallback = append(fallback,
		iconCandidate{"/usr/share/pixmaps/" + icon + ".svg", "svg"},
		iconCandidate{"/usr/share/pixmaps/" + icon + ".png", "png"},
	)

	startBase := "/usr/share/icons/CharacterizedTheme/" + startDir + "/" + icon
	insideBase := "/usr/share/icons/CharacterizedTheme/" + insideDir + "/" + icon
	endBase := "/usr/share/icons/CharacterizedTheme/" + endDir + "/" + icon
	for _, path := range []string{startBase + ".svg", insideBase + ".png", endBase + ".svg"} {
		if len(path) <= 4096 {
			t.Fatalf("characterization candidate is only %d bytes: %q", len(path), path)
		}
	}

	var remaining []iconCandidate
	for _, candidate := range fallback {
		if len(candidate.path) <= 4096 {
			remaining = append(remaining, candidate)
		}
	}
	var want [][]string
	const batchLimit = 32768
	for start := 0; start < len(remaining); {
		end, size := start, 0
		for end < len(remaining) {
			length := len(remaining[end].path) + 1
			if size+length > batchLimit && end != start {
				break
			}
			size += length
			end++
			if size >= batchLimit {
				break
			}
		}
		argv := []string{"ls", "-1d", "--"}
		argv = append(argv, candidatePaths(remaining[start:end])...)
		want = append(want, argv)
		start = end
	}

	v, err := a.startMachine(m)
	if err != nil {
		t.Fatal(err)
	}
	if icon, err := a.readGuestIcon(v, m, icon); err != nil || icon != nil {
		t.Fatalf("icon=%+v err=%v", icon, err)
	}

	var got [][]string
	for _, req := range f.requests {
		if req.Op == "run" && len(req.Argv) >= 3 && req.Argv[0] == "ls" && req.Argv[1] == "-1d" {
			got = append(got, append([]string{}, req.Argv...))
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ls -1d batches differ\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestReadGuestIconFallbackContinuesAfterInvalidIcon(t *testing.T) {
	a, f, m := withMachine(t)
	guestFiles(f, map[string]string{
		"/usr/share/icons/Adwaita/index.theme": `[Icon Theme]
Directories=scalable/apps,apps/48
[scalable/apps]
Context=Applications
Type=Scalable
Size=64
[apps/48]
Context=Applications
Type=Fixed
Size=48
`,
		"/usr/share/icons/Adwaita/scalable/apps/sample-icon.svg": "not an svg",
		"/usr/share/icons/Adwaita/apps/48/sample-icon.png":       pngIcon,
	})

	v, err := a.startMachine(m)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	a.err = &stderr
	icon, err := a.readGuestIcon(v, m, "sample-icon")
	if err != nil {
		t.Fatal(err)
	}
	if icon == nil || icon.ext != "png" || !bytes.Equal(icon.data, []byte(pngIcon)) {
		t.Fatalf("did not continue to the next valid candidate: %+v", icon)
	}
	want := "nsl: /usr/share/icons/Adwaita/scalable/apps/sample-icon.svg in debian is not a SVG image\n"
	if stderr.String() != want {
		t.Fatalf("stderr got %q, want %q", stderr.String(), want)
	}
}

func TestGuestFileSuccessOversizeText(t *testing.T) {
	a, f, m := withMachine(t)
	v, err := a.startMachine(m)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 10
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "run" {
			_, _ = io.WriteString(stdout, strings.Repeat("x", limit+1))
			return errAnswered
		}
		return nil
	}
	_, found, err := a.guestFile(v, m, "/tmp/oversize", limit)
	if found {
		t.Fatal("oversized guest file was found")
	}
	want := "/tmp/oversize in debian is larger than 10 bytes"
	if err == nil || err.Error() != want {
		t.Fatalf("error got %v, want %q", err, want)
	}
}

func TestGuestListingExitAndOversizeText(t *testing.T) {
	a, f, m := withMachine(t)
	v, err := a.startMachine(m)
	if err != nil {
		t.Fatal(err)
	}

	for _, code := range []int{0, 1, 2} {
		f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
			if req.Op == "run" {
				_, _ = io.WriteString(stdout, "one\ntwo\n")
				return commandExit(code)
			}
			return nil
		}
		listing, err := a.guestListing(v, m, []string{"ls", "-1", "--", "/tmp"}, 64)
		if err != nil || string(listing) != "one\ntwo\n" {
			t.Fatalf("exit %d: listing=%q err=%v", code, listing, err)
		}
	}

	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "run" {
			return commandExit(3)
		}
		return nil
	}
	if _, err := a.guestListing(v, m, []string{"ls", "-1", "--", "/tmp"}, 64); err == nil || err.Error() != "listing in debian: exit status 3" {
		t.Fatalf("other exit error got %v", err)
	}

	const limit = 10
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "run" {
			if _, err := io.WriteString(stdout, strings.Repeat("x", limit+2)); err != nil {
				return err
			}
			return errAnswered
		}
		return nil
	}
	if _, err := a.guestListing(v, m, []string{"ls", "-1", "--", "/tmp"}, limit); err == nil || err.Error() != "listing -- /tmp in debian is larger than 10 bytes" {
		t.Fatalf("oversize listing error got %v", err)
	}
}

func TestParseIconThemeCharacterizesDirectoryOrder(t *testing.T) {
	content := []byte(`[Icon Theme]
Directories=equal-a,scalable-b,equal-b,scalable-a,equal-c
[equal-a]
Context=Applications
Type=Fixed
Size=48
[equal-b]
Context=Applications
Type=Fixed
Size=24
Scale=2
[equal-c]
Context=Applications
Type=Fixed
Size=48
Scale=1
[scalable-b]
Context=Applications
Type=Scalable
Size=32
[scalable-a]
Context=Applications
Type=Scalable
Size=128
`)
	dirs, ok := parseIconTheme(content)
	if !ok {
		t.Fatal("parseIconTheme refused the theme")
	}
	var got []string
	for _, dir := range dirs {
		got = append(got, dir.name)
	}
	want := []string{"scalable-b", "scalable-a", "equal-a", "equal-b", "equal-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("directory order got %v, want %v", got, want)
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

func TestAppAddCopiesTerminalAndTheGuestPath(t *testing.T) {
	a, f, _ := withMachine(t)
	body := appEntryBody("Exec=calc --flag", "Terminal=true", "Path=/opt/calc/bin")
	guestFiles(f, map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": body})
	entry, err := os.ReadFile(addedEntry(t, a, f, "org.gnome.Calculator"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nTerminal=true\n", " --cd /opt/calc/bin -- calc --flag\n"} {
		if !strings.Contains(string(entry), want) {
			t.Fatalf("missing %q in\n%s", want, entry)
		}
	}
}

func TestAppAddIgnoresLocalizedKeys(t *testing.T) {
	a, f, _ := withMachine(t)
	body := appEntryBody("Name[de]=Taschenrechner", "Exec=calc", "Comment=Calculate", "Comment[de]=Rechnen")
	guestFiles(f, map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": body})
	entry, err := os.ReadFile(addedEntry(t, a, f, "org.gnome.Calculator"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\nName=Calculator (debian)\n", "\nComment=Calculate\n"} {
		if !strings.Contains(string(entry), want) {
			t.Fatalf("missing %q in\n%s", want, entry)
		}
	}
	for _, unwanted := range []string{"Taschenrechner", "Rechnen"} {
		if strings.Contains(string(entry), unwanted) {
			t.Fatalf("copied the localized value %q into\n%s", unwanted, entry)
		}
	}
}

func TestAppAddStopsOnAnOversizedIcon(t *testing.T) {
	a, f, _ := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop":            guestApp("Calculator"),
		"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": "<svg>" + strings.Repeat("x", appIconLimit),
	}
	guestFiles(f, files)
	if err := a.execute([]string{"app", "add", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatal(err)
	}
	if _, err := os.Lstat(a.applicationsDir()); !os.IsNotExist(err) {
		t.Fatal("wrote an entry after a failed icon read")
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
		{"an unsupported escape", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": appEntryBody(`Exec=calc "\\q"`)}, "org.gnome.Calculator", "quotes the unsupported escape"},
		{"hidden", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": appEntryBody("Exec=calc", "Hidden=true")}, "org.gnome.Calculator", "Hidden"},
		{"a link", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": "[Desktop Entry]\nType=Link\nName=Calculator\nExec=calc\n"}, "org.gnome.Calculator", "not Application"},
		{"no exec", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": "[Desktop Entry]\nType=Application\nName=Calculator\n"}, "org.gnome.Calculator", "no Exec"},
		{"another first group", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": "[Desktop Action new]\nName=New\n[Desktop Entry]\nType=Application\nName=Calculator\nExec=calc\n"}, "org.gnome.Calculator", "[Desktop Entry]"},
		{"a duplicate key", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": appEntryBody("Exec=calc", "Comment=one", "Comment=two")}, "org.gnome.Calculator", "appears twice"},
		{"invalid UTF-8", map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": "[Desktop Entry]\nType=Application\nName=Calc\xff\nExec=calc\n"}, "org.gnome.Calculator", "not UTF-8"},
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

// noIcon checks that a refusal wrote neither the icon nor the machine's icon
// directory, although the guest has an icon.
func noIcon(t *testing.T, a *app) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(a.appsDir("debian"), "org.gnome.Calculator.svg")); !os.IsNotExist(err) {
		t.Fatal("wrote an icon for a refused entry")
	}
	if _, err := os.Lstat(a.appsDir("debian")); !os.IsNotExist(err) {
		t.Fatal("created the icon directory for a refused entry")
	}
}

func TestAppAddNeverReplacesAFileItDidNotWrite(t *testing.T) {
	a, f, m := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop":            guestApp("Calculator"),
		"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": svgIcon,
	}
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
	noIcon(t, a)
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
	noIcon(t, a)
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
	noIcon(t, a)
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
	// A second real machine, with an entry of its own.
	if err := a.create(append([]string{"debian-2"}, machineImage(t, "second rootfs")...)); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop": strings.Replace(guestApp("Calculator"), "StartupWMClass=Gnome-calculator\n", "StartupWMClass=Gnome-calculator\nMimeType=image/png;image/jpeg;image/png;;\n", 1),
		"/usr/share/applications/galculator.desktop":           guestApp("Galculator"),
	}
	guestFiles(f, files)
	for _, args := range [][]string{
		{"app", "add", "--mime-types", "org.gnome.Calculator"},
		{"app", "add", "galculator"},
		{"app", "add", "-m", "debian-2", "galculator"},
	} {
		if err := a.execute(args); err != nil {
			t.Fatal(err)
		}
	}
	// Files nsl did not write stay out of the list.
	writeHostFile(t, a, "nsl-debian-other.desktop", "[Desktop Entry]\nType=Application\nName=Other\nExec=other\n")
	other := strings.Replace(guestApp("Calculator"), "[Desktop Entry]\nType=Application\nName=Calculator\n",
		"[Desktop Entry]\nType=Application\nName=Calculator (debian-2)\nX-Nsl-Machine=debian-2\nX-Nsl-Machine-ID="+strings.Repeat("0", 32)+"\nX-Nsl-App=org.gnome.Calculator\n", 1)
	writeHostFile(t, a, "nsl-debian-2-org.gnome.Calculator.desktop", other)
	var out bytes.Buffer
	a.out = &out
	// Every machine's entries, sorted by machine and APP, with the Name last.
	rows := [][4]string{{"debian", "galculator", "Galculator (debian)", "-"}, {"debian", "org.gnome.Calculator", "Calculator (debian)", "2"},
		{"debian-2", "galculator", "Galculator (debian-2)", "-"}}
	listed := func(machine string) []string {
		t.Helper()
		out.Reset()
		args := []string{"app", "list"}
		if machine != "" {
			args = append(args, "-m", machine)
		}
		if err := a.execute(args); err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(out.String()), "\n")
	}
	lines := listed("")
	if len(lines) != len(rows)+1 || !strings.Contains(lines[0], "MACHINE") || !strings.Contains(lines[0], "FILE TYPES") {
		t.Fatalf("%q", lines)
	}
	for i, want := range rows {
		if fields := strings.Fields(lines[i+1]); len(fields) < 4 || fields[0] != want[0] || fields[1] != want[1] || !strings.Contains(lines[i+1], want[2]) || !strings.HasSuffix(lines[i+1], " "+want[3]) {
			t.Fatalf("%q", lines[i+1])
		}
	}
	// -m lists one machine's entries only.
	for machine, want := range map[string][][3]string{
		"debian":   {{"debian", "galculator", "-"}, {"debian", "org.gnome.Calculator", "2"}},
		"debian-2": {{"debian-2", "galculator", "-"}},
	} {
		lines := listed(machine)
		if len(lines) != len(want)+1 {
			t.Fatalf("%s: %q", machine, lines)
		}
		for i, w := range want {
			if fields := strings.Fields(lines[i+1]); len(fields) < 4 || fields[0] != w[0] || fields[1] != w[1] || !strings.HasSuffix(lines[i+1], " "+w[2]) {
				t.Fatalf("%s: %q", machine, lines[i+1])
			}
		}
	}
	if err := a.execute([]string{"app", "list", "-m", "ghost"}); err == nil {
		t.Fatal("listed the entries of a machine that does not exist")
	}
	// With no owned entries the list says how to add one, and the files nsl did
	// not write still do not appear.
	for _, args := range [][]string{{"app", "remove", "org.gnome.Calculator"}, {"app", "remove", "galculator"}, {"app", "remove", "-m", "debian-2", "galculator"}} {
		if err := a.execute(args); err != nil {
			t.Fatal(err)
		}
	}
	if got := listed(""); len(got) != 1 || got[0] != "No menu entries; add one with nsl app add [-m NAME] APP" {
		t.Fatalf("%q", got)
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
		exec  string
		want  []string
		field *execField
	}{
		{"calc", []string{"calc"}, nil},
		{"calc   --x", []string{"calc", "--x"}, nil},
		{`calc "a b"`, []string{"calc", "a b"}, nil},
		{`calc "a\\b"`, []string{"calc", `a\b`}, nil},
		{`calc "say \"hi\""`, []string{"calc", `say "hi"`}, nil},
		{"calc %U", []string{"calc", "%U"}, &execField{Code: "U", Index: 1}},
		{"%f calc %i", []string{"calc"}, nil},
		{"calc %f %U", []string{"calc", "%f"}, &execField{Code: "f", Index: 1}},
		{"calc --note=%%", []string{"calc", "--note=%"}, nil},
		{`calc ""`, []string{"calc", ""}, nil},
	} {
		got, field, err := execArgs(tc.exec)
		if err != nil || strings.Join(got, "|") != strings.Join(tc.want, "|") || !reflect.DeepEqual(field, tc.field) {
			t.Fatalf("%q: %q %+v %v", tc.exec, got, field, err)
		}
	}
	for _, bad := range []string{"", "%U", "cal'c", `calc a\b`, "calc > out", `calc "a b`, `calc "a\qb"`, "calc --file=%f", "calc %", "calc percent%",
		`tool "one"two`, `tool "a""b"`} {
		if got, _, err := execArgs(bad); err == nil {
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
	if !strings.Contains(appUsage, "app add [-m NAME] [--mime-types] APP") {
		t.Fatal(appUsage)
	}
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

// editHostEntry writes a host entry without its Exec and with Hidden=true, as a
// user might: nsl still owns it through its markers.
func editHostEntry(t *testing.T, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" && !strings.HasPrefix(line, "Exec=") {
			lines = append(lines, line)
		}
	}
	lines = append(lines, "Hidden=true", "")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestAppKeepsAnEditedOwnedEntry(t *testing.T) {
	a, f, _ := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop":            guestApp("Calculator"),
		"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": svgIcon,
	}
	guestFiles(f, files)
	entry := addedEntry(t, a, f, "org.gnome.Calculator")
	// An entry the user edited is still owned: the markers decide.
	editHostEntry(t, entry)
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"app", "list"}); err != nil || !strings.Contains(out.String(), "org.gnome.Calculator  Calculator (debian)") {
		t.Fatal(err, out.String())
	}
	if err := a.execute([]string{"app", "remove", "org.gnome.Calculator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(entry); !os.IsNotExist(err) {
		t.Fatal("kept an edited entry")
	}
	// The same holds when the machine goes, edited entry and all.
	entry = addedEntry(t, a, f, "org.gnome.Calculator")
	editHostEntry(t, entry)
	if err := a.remove([]string{"debian", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(entry); !os.IsNotExist(err) {
		t.Fatal("kept an edited entry of a removed machine")
	}
}

func TestAppHandlesALargeEntry(t *testing.T) {
	a, f, _ := withMachine(t)
	// A Comment that fills the guest read's bound, and a quoted argument of
	// backslashes. The host entry is longer than the guest entry, so it passes
	// the bound the read uses, and must still count as owned.
	head := "[Desktop Entry]\nType=Application\nName=Calculator\nExec=gnome-calculator \"a" +
		strings.Repeat(`\\`, 2000) + "b\"\nComment="
	body := head + strings.Repeat("c", appEntryLimit-len(head)-60) + "\n"
	if len(body) >= appEntryLimit {
		t.Fatalf("the guest entry is %d bytes, not under the read's bound", len(body))
	}
	files := map[string]string{"/usr/share/applications/org.gnome.Calculator.desktop": body}
	guestFiles(f, files)
	entry := addedEntry(t, a, f, "org.gnome.Calculator")
	st, err := os.Stat(entry)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() <= appEntryLimit || st.Size() > hostEntryLimit {
		t.Fatalf("the host entry is %d bytes", st.Size())
	}
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"app", "list"}); err != nil || !strings.Contains(out.String(), "org.gnome.Calculator") {
		t.Fatal(err, out.String())
	}
	if err := a.execute([]string{"app", "remove", "org.gnome.Calculator"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(entry); !os.IsNotExist(err) {
		t.Fatal("kept the entry")
	}
}

func TestAppRemoveRefusesAForeignIconDirectory(t *testing.T) {
	a, f, _ := withMachine(t)
	files := map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop":            guestApp("Calculator"),
		"/usr/share/icons/hicolor/scalable/apps/org.gnome.Calculator.svg": svgIcon,
	}
	guestFiles(f, files)
	addedEntry(t, a, f, "org.gnome.Calculator")
	// nsl's icon directory is replaced by a symlink to another program's.
	outside := t.TempDir()
	keep := filepath.Join(outside, "org.gnome.Calculator.svg")
	if err := os.WriteFile(keep, []byte(svgIcon), 0600); err != nil {
		t.Fatal(err)
	}
	dir := a.appsDir("debian")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if err := a.execute([]string{"app", "remove", "org.gnome.Calculator"}); err == nil || !strings.Contains(err.Error(), "state directory") {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != svgIcon {
		t.Fatal("touched a directory nsl does not own:", err)
	}
}

func TestRemovePreviewReportsAMenuEntryFailure(t *testing.T) {
	a, _, _ := withMachine(t)
	// A file where the applications directory belongs makes the count fail.
	if err := os.MkdirAll(a.dataHome, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.applicationsDir(), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.remove([]string{"debian"}); err == nil {
		t.Fatal("ignored a failed menu entry count")
	}
	if _, err := a.machine("debian"); err != nil {
		t.Fatal("the preview changed the machine:", err)
	}
}

func TestRemoveMenuEntriesCharacterization(t *testing.T) {
	a, f, m := withMachine(t)
	guestFiles(f, map[string]string{
		"/usr/share/applications/org.gnome.Calculator.desktop": guestApp("Calculator"),
		"/usr/share/applications/galculator.desktop":           guestApp("Galculator"),
	})
	owned := []string{
		addedEntry(t, a, f, "org.gnome.Calculator"),
		addedEntry(t, a, f, "galculator"),
	}

	unmarked := writeHostFile(t, a, "nsl-debian-unmarked.desktop", "[Desktop Entry]\nType=Application\nName=Unmarked\nExec=unmarked\n")
	wrongID := writeHostFile(t, a, "nsl-debian-wrong-id.desktop", "[Desktop Entry]\nType=Application\nName=Wrong ID\nX-Nsl-Machine=debian\nX-Nsl-Machine-ID=wrong\nX-Nsl-App=wrong-id\n")
	outside := filepath.Join(t.TempDir(), "link-target")
	if err := os.WriteFile(outside, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(a.applicationsDir(), "nsl-debian-link.desktop")
	if err := os.MkdirAll(a.applicationsDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	noSuffix := writeHostFile(t, a, "nsl-debian-nosuffix", "[Desktop Entry]\nType=Application\nName=No Suffix\nX-Nsl-Machine=debian\nX-Nsl-Machine-ID="+m.ID+"\nX-Nsl-App=no-suffix\n")

	if err := a.create(append([]string{"debian-2"}, machineImage(t, "second rootfs")...)); err != nil {
		t.Fatal(err)
	}
	m2, err := a.machine("debian-2")
	if err != nil {
		t.Fatal(err)
	}
	otherPrefix := writeHostFile(t, a, "nsl-debian-2-other.desktop", "[Desktop Entry]\nType=Application\nName=Other Machine\nX-Nsl-Machine=debian-2\nX-Nsl-Machine-ID="+m2.ID+"\nX-Nsl-App=other\n")

	appsDir := a.appsDir("debian")
	if err := os.MkdirAll(appsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "kept-by-remove-all.tmp"), []byte("icon"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := a.removeMenuEntries(m); err != nil {
		t.Fatal(err)
	}
	for _, path := range owned {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("kept owned entry %s: %v", path, err)
		}
	}
	for _, path := range []string{unmarked, wrongID, link, noSuffix, otherPrefix} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("removed foreign or non-entry %s: %v", path, err)
		}
	}
	if _, err := os.Lstat(appsDir); !os.IsNotExist(err) {
		t.Fatalf("kept the machine .apps directory: %v", err)
	}
}

func TestRemoveMenuEntriesWithoutApplicationsDir(t *testing.T) {
	a, _, m := withMachine(t)
	appsDir := a.appsDir(m.Name)
	if err := os.MkdirAll(appsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "icon.svg"), []byte(svgIcon), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.removeMenuEntries(m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(appsDir); !os.IsNotExist(err) {
		t.Fatalf("kept the machine .apps directory: %v", err)
	}
}

func TestMenuEntriesCharacterization(t *testing.T) {
	t.Run("two machines and foreign files", func(t *testing.T) {
		a, f, m := withMachine(t)
		if err := a.create(append([]string{"debian-2"}, machineImage(t, "second rootfs")...)); err != nil {
			t.Fatal(err)
		}
		m2, err := a.machine("debian-2")
		if err != nil {
			t.Fatal(err)
		}
		guestFiles(f, map[string]string{
			"/usr/share/applications/zeta.desktop":  guestApp("Zeta"),
			"/usr/share/applications/alpha.desktop": guestApp("Alpha"),
		})
		for _, args := range [][]string{
			{"app", "add", "zeta"},
			{"app", "add", "alpha"},
			{"app", "add", "-m", "debian-2", "zeta"},
			{"app", "add", "-m", "debian-2", "alpha"},
		} {
			if err := a.execute(args); err != nil {
				t.Fatal(err)
			}
		}
		writeHostFile(t, a, "nsl-debian-foreign.desktop", "[Desktop Entry]\nType=Application\nName=Foreign\nExec=foreign\n")
		writeHostFile(t, a, "nsl-debian-2-foreign.desktop", "[Desktop Entry]\nType=Application\nName=Foreign 2\nX-Nsl-Machine=debian-2\nX-Nsl-Machine-ID=wrong\nX-Nsl-App=foreign-2\n")

		entries, err := a.menuEntries([]*machineRecord{m2, m})
		if err != nil {
			t.Fatal(err)
		}
		got := make([][2]string, len(entries))
		for i, entry := range entries {
			got[i] = [2]string{entry.Machine, entry.App}
		}
		want := [][2]string{{"debian-2", "alpha"}, {"debian-2", "zeta"}, {"debian", "alpha"}, {"debian", "zeta"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("menu entries: got %v, want %v", got, want)
		}
	})

	t.Run("absent directory", func(t *testing.T) {
		a, _, m := withMachine(t)
		entries, err := a.menuEntries([]*machineRecord{m})
		if err != nil {
			t.Fatal(err)
		}
		if entries == nil || len(entries) != 0 {
			t.Fatalf("absent applications directory: got %#v", entries)
		}
	})
}

func TestMenuScanReadDirErrors(t *testing.T) {
	a, _, m := withMachine(t)
	if err := os.MkdirAll(a.dataHome, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.applicationsDir(), []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.menuEntries([]*machineRecord{m}); err == nil {
		t.Fatal("menuEntries ignored a ReadDir error")
	}
	if err := a.removeMenuEntries(m); err == nil {
		t.Fatal("removeMenuEntries ignored a ReadDir error")
	}
}
