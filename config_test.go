package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// 61.5 GiB and 32 CPUs.
var testHost = hostFacts{memoryKiB: 123 << 19, cpus: 32}

var configForms = map[string]string{
	"vm.memory":             "a whole number of GiB from 1 to 128",
	"vm.cpus":               "a whole number of CPUs from 1 to 64",
	"machines.autostart":    "true or false",
	"machines.idle_timeout": "a whole number of minutes from 0 to 1440",
	"isolated.memory":       "a whole number of GiB from 1 to 128",
	"isolated.cpus":         "a whole number of CPUs from 1 to 64",
}

func shown(c *config) map[string]string {
	got := map[string]string{}
	for _, k := range c.keys() {
		value, from := k.show()
		got[k.name] = value + " " + from
	}
	return got
}

func defaultsShown() map[string]string {
	return map[string]string{
		"vm.memory":             "30 default (half of host memory)",
		"vm.cpus":               "32 default (host CPUs)",
		"machines.autostart":    "true default",
		"machines.idle_timeout": "15 default",
		"isolated.memory":       "2 default",
		"isolated.cpus":         "2 default",
	}
}

func TestConfigAbsentFileUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nsl", "nsl.conf")
	c, err := readConfig(path, testHost)
	if err != nil {
		t.Fatal(err)
	}
	if c.present || c.path != path || !reflect.DeepEqual(shown(c), defaultsShown()) {
		t.Fatal(c.present, c.path, shown(c))
	}
	if !c.autostart.value || c.autostart.line != 0 || c.idleTimeout != (intSetting{15, source{}}) || c.isolatedMemory.value != 2 || c.isolatedCPUs.value != 2 {
		t.Fatalf("%+v", c)
	}
	for _, text := range []string{"", "\n\n", "# only a comment\n", "[vm]\n[machines]\n[isolated]\n"} {
		c, err = parseConfig(path, []byte(text), testHost)
		if err != nil || !c.present || !reflect.DeepEqual(shown(c), defaultsShown()) {
			t.Fatalf("%q: %v %v", text, err, c)
		}
	}
}

func TestConfigHostDefaults(t *testing.T) {
	for _, tc := range []struct {
		host         hostFacts
		memory, cpus int
	}{
		{hostFacts{1 << 20, 1}, 2, 1},
		{hostFacts{3 << 20, 4}, 2, 4},
		{hostFacts{5 << 20, 2}, 2, 2},
		{hostFacts{6 << 20, 8}, 3, 8},
		{hostFacts{123 << 19, 64}, 30, 64},
		{hostFacts{257 << 20, 65}, 128, 64},
		{hostFacts{1 << 30, 96}, 128, 64},
	} {
		c := defaultConfig("nsl.conf", tc.host)
		if c.vmMemory.value != tc.memory || c.vmCPUs.value != tc.cpus || c.vmMemory.line != 0 || c.vmCPUs.line != 0 {
			t.Fatalf("%+v: memory %d cpus %d", tc.host, c.vmMemory.value, c.vmCPUs.value)
		}
	}
}

func TestMemTotal(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int64
	}{
		{"MemTotal:       64487424 kB\nMemFree:         1024 kB\n", 64487424},
		{"MemFree: 1024 kB\nMemTotal: 3145728 kB", 3145728},
		{"MemFree: 1024 kB\n", 0},
		{"", 0},
		{"MemTotal:\n", 0},
		{"MemTotal: kB\n", 0},
		{"MemTotal: 12x kB\n", 0},
		{"MemTotal: 1024\n", 0},
		{"MemTotal: 1024 MB\n", 0},
		{"MemTotal: 0 kB\n", 0},
		{"MemTotal: -5 kB\n", 0},
		{"MemTotal: 99999999999999999999 kB\n", 0},
		{"MemTotal: bad kB\nMemTotal: 1024 kB\n", 0},
	} {
		got, err := memTotal(tc.text)
		if tc.want == 0 && err == nil || tc.want != 0 && (err != nil || got != tc.want) {
			t.Fatalf("%q: %d %v", tc.text, got, err)
		}
	}
}

func TestConfigRanges(t *testing.T) {
	path := "/cfg/nsl.conf"
	check := func(name, value string, ok bool) {
		t.Helper()
		section, key, _ := strings.Cut(name, ".")
		c, err := parseConfig(path, []byte("["+section+"]\n"+key+" = "+value+"\n"), testHost)
		if ok {
			if err != nil || shown(c)[name] != value+" file (line 2)" {
				t.Fatalf("%s = %q: %v", name, value, err)
			}
			return
		}
		want := fmt.Sprintf("%s:2: %s must be %s, got %q", path, name, configForms[name], value)
		if c != nil || err == nil || err.Error() != want {
			t.Fatalf("%s = %q: got %v, want %s", name, value, err, want)
		}
	}
	for _, r := range []struct {
		name     string
		min, max int
	}{{"vm.memory", 1, 128}, {"vm.cpus", 1, 64}, {"machines.idle_timeout", 0, 1440}, {"isolated.memory", 1, 128}, {"isolated.cpus", 1, 64}} {
		check(r.name, strconv.Itoa(r.min), true)
		check(r.name, strconv.Itoa(r.max), true)
		check(r.name, strconv.Itoa(r.min-1), false)
		check(r.name, strconv.Itoa(r.max+1), false)
		check(r.name, "9223372036854775808", false)
		check(r.name, "99999999999999999999999", false)
	}
	check("machines.autostart", "true", true)
	check("machines.autostart", "false", true)
}

func TestConfigInvalidValues(t *testing.T) {
	path := "/cfg/nsl.conf"
	for _, tc := range []struct{ name, value string }{
		{"vm.memory", "8G"},
		{"vm.memory", "8GiB"},
		{"vm.memory", "+8"},
		{"vm.memory", "-8"},
		{"vm.memory", ""},
		{"vm.memory", `"8"`},
		{"vm.memory", "'8'"},
		{"vm.memory", "8 # GiB"},
		{"vm.memory", "8 9"},
		{"vm.memory", "8.0"},
		{"vm.memory", "0x10"},
		{"vm.memory", "1e2"},
		{"vm.memory", "８"},
		{"vm.cpus", "four"},
		{"machines.idle_timeout", "15m"},
		{"isolated.cpus", "2 CPUs"},
		{"machines.autostart", "yes"},
		{"machines.autostart", "1"},
		{"machines.autostart", "True"},
		{"machines.autostart", "FALSE"},
		{"machines.autostart", ""},
		{"machines.autostart", `"true"`},
		{"machines.autostart", "true # on"},
	} {
		section, key, _ := strings.Cut(tc.name, ".")
		c, err := parseConfig(path, []byte("["+section+"]\n"+key+" = "+tc.value+"\n"), testHost)
		want := fmt.Sprintf("%s:2: %s must be %s, got %q", path, tc.name, configForms[tc.name], tc.value)
		if c != nil || err == nil || err.Error() != want {
			t.Fatalf("%s = %q: got %v, want %s", tc.name, tc.value, err, want)
		}
	}
}

func TestConfigCommentsAndWhitespace(t *testing.T) {
	text := "# nsl settings\r\n" +
		"\r\n" +
		"  \t\r\n" +
		" [ vm ] \t\r\n" +
		"\tmemory=8\r\n" +
		"  # memory = 999\r\n" +
		"cpus\t=\t4   \r\n" +
		"[machines]\n" +
		"#[network]\n" +
		"autostart = false\n" +
		"idle_timeout =0\n" +
		"\t[isolated]\n" +
		"   memory   =   16\n" +
		"cpus = 1"
	c, err := parseConfig("/cfg/nsl.conf", []byte(text), testHost)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"vm.memory":             "8 file (line 5)",
		"vm.cpus":               "4 file (line 7)",
		"machines.autostart":    "false file (line 10)",
		"machines.idle_timeout": "0 file (line 11)",
		"isolated.memory":       "16 file (line 13)",
		"isolated.cpus":         "1 file (line 14)",
	}
	if got := shown(c); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if c.vmMemory != (intSetting{8, source{line: 5}}) || c.autostart != (boolSetting{false, source{line: 10}}) || c.idleTimeout.value != 0 || c.isolatedMemory.value != 16 {
		t.Fatalf("%+v", c)
	}
}

func TestConfigErrors(t *testing.T) {
	path := "/home/u/.config/nsl/nsl.conf"
	for _, tc := range []struct{ text, want string }{
		{"[vm]\nmemory 8\n", `2: expected [section], key = value or # comment, got "memory 8"`},
		{"[vm\n", `1: expected [section], key = value or # comment, got "[vm"`},
		{"[vm]\n= 8\n", `2: expected [section], key = value or # comment, got "= 8"`},
		{"[vm] # resources\n", `1: expected [section], key = value or # comment, got "[vm] # resources"`},
		{"[network]\n", "1: unknown section [network]; expected [vm], [machines] or [isolated]"},
		{"[VM]\n", "1: unknown section [VM]; expected [vm], [machines] or [isolated]"},
		{"[]\n", "1: unknown section []; expected [vm], [machines] or [isolated]"},
		{"[vm]\n[machines]\n[ vm ]\n", "3: duplicate section [vm] (first on line 1)"},
		{"# resources\nmemory = 8\n[vm]\n", `2: key "memory" is outside a section`},
		{"[vm]\nmemroy = 8\n", `2: unknown key "memroy" in [vm]; expected memory or cpus`},
		{"[vm]\nMemory = 8\n", `2: unknown key "Memory" in [vm]; expected memory or cpus`},
		{"[vm]\nvm.memory = 8\n", `2: unknown key "vm.memory" in [vm]; expected memory or cpus`},
		{"[isolated]\nidle_timeout = 5\n", `2: unknown key "idle_timeout" in [isolated]; expected memory or cpus`},
		{"[machines]\nautostart_all = true\n", `2: unknown key "autostart_all" in [machines]; expected autostart or idle_timeout`},
		{"[vm]\nmemory = 8\n\nmemory=8\n", "4: duplicate key vm.memory (first on line 2)"},
		{"[vm]\nmemory = 0\n[network]\n", `2: vm.memory must be a whole number of GiB from 1 to 128, got "0"`},
		{"[vm]\nmemory = 129\n", `2: vm.memory must be a whole number of GiB from 1 to 128, got "129"`},
		{"[vm]\nmemory = 8\xff\n", "2: not valid UTF-8"},
		{"# \xc3\n", "1: not valid UTF-8"},
		{"[vm]\x00\n", "1: contains a NUL byte"},
		{"[vm]\n# \x00\n", "2: contains a NUL byte"},
	} {
		c, err := parseConfig(path, []byte(tc.text), testHost)
		if c != nil || err == nil || err.Error() != path+":"+tc.want {
			t.Fatalf("%q: got %v, want %s:%s", tc.text, err, path, tc.want)
		}
	}
}

func TestConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fallback := filepath.Join(home, ".config", "nsl", "nsl.conf")
	for _, tc := range []struct{ xdg, want string }{
		{"/xdg/config", "/xdg/config/nsl/nsl.conf"},
		{"relative/config", fallback},
		{"", fallback},
	} {
		t.Setenv("XDG_CONFIG_HOME", tc.xdg)
		if got, err := configPath(); err != nil || got != tc.want {
			t.Fatalf("%q: %s %v", tc.xdg, got, err)
		}
	}
	os.Unsetenv("XDG_CONFIG_HOME")
	if got, err := configPath(); err != nil || got != fallback {
		t.Fatal(got, err)
	}
}

func TestConfigFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nsl.conf")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(path, testHost); err == nil || err.Error() != path+": not a regular file" {
		t.Fatal(err)
	}
	notDir := filepath.Join(dir, "file", "nsl.conf")
	os.WriteFile(filepath.Dir(notDir), nil, 0600)
	if _, err := readConfig(notDir, testHost); err == nil || !strings.Contains(err.Error(), notDir) {
		t.Fatal("treated an unreadable path as absent:", err)
	}
	large := filepath.Join(dir, "large.conf")
	line := "# " + strings.Repeat("x", 1021) + "\n"
	os.WriteFile(large, []byte(strings.Repeat(line, 64)), 0600)
	if c, err := readConfig(large, testHost); err != nil || !c.present {
		t.Fatal("rejected a 64 KiB file:", err)
	}
	os.WriteFile(large, []byte(strings.Repeat(line, 64)+"\n"), 0600)
	if _, err := readConfig(large, testHost); err == nil || err.Error() != large+": larger than 64 KiB" {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.conf")
	os.WriteFile(target, []byte("[vm]\nmemory = 8\n"), 0600)
	link := filepath.Join(dir, "link.conf")
	os.Symlink(target, link)
	if c, err := readConfig(link, testHost); err != nil || c.path != link || c.vmMemory != (intSetting{8, source{line: 2}}) {
		t.Fatal("did not follow symlink:", err)
	}
	dangling := filepath.Join(dir, "dangling.conf")
	os.Symlink(filepath.Join(dir, "missing.conf"), dangling)
	if _, err := readConfig(dangling, testHost); err == nil || err.Error() != dangling+": symlink target does not exist" {
		t.Fatal("treated a dangling symlink as absent:", err)
	}
	if os.Getuid() != 0 {
		os.Chmod(target, 0)
		if _, err := readConfig(target, testHost); err == nil || !strings.Contains(err.Error(), target) {
			t.Fatal("unreadable file:", err)
		}
	}
}

func TestConfigCommand(t *testing.T) {
	a, _ := testApp(t)
	var out bytes.Buffer
	a.out, a.host = &out, &testHost
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "nsl", "nsl.conf")
	if err := a.execute([]string{"config"}); err != nil {
		t.Fatal(err)
	}
	want := "Configuration file: " + path + " (absent)\n\n" +
		"SETTING                VALUE  SOURCE\n" +
		"vm.memory              30     default (half of host memory)\n" +
		"vm.cpus                32     default (host CPUs)\n" +
		"machines.autostart     true   default\n" +
		"machines.idle_timeout  15     default\n" +
		"isolated.memory        2      default\n" +
		"isolated.cpus          2      default\n"
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}
	os.Mkdir(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("# resources\n[vm]\ncpus = 4\n[machines]\nidle_timeout = 0\nautostart = false\n"), 0600)
	out.Reset()
	if err := a.execute([]string{"config"}); err != nil {
		t.Fatal(err)
	}
	want = "Configuration file: " + path + "\n\n" +
		"SETTING                VALUE  SOURCE\n" +
		"vm.memory              30     default (half of host memory)\n" +
		"vm.cpus                4      file (line 3)\n" +
		"machines.autostart     false  file (line 6)\n" +
		"machines.idle_timeout  0      file (line 5)\n" +
		"isolated.memory        2      default\n" +
		"isolated.cpus          2      default\n"
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}
	if err := a.execute([]string{"config", "extra"}); err == nil || err.Error() != "usage: config" {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("[vm]\nmemory = 8G\n"), 0600)
	out.Reset()
	err := a.execute([]string{"config"})
	if err == nil || err.Error() != path+`:2: vm.memory must be a whole number of GiB from 1 to 128, got "8G"` || out.Len() != 0 {
		t.Fatal(err, out.String())
	}
}
