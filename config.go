package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode/utf8"
)

const configLimit = 64 << 10

type hostFacts struct {
	memoryKiB int64
	cpus      int
}

// source records where a value came from; line 0 means the default.
type source struct {
	line   int
	reason string
}

type intSetting struct {
	value int
	source
}

type boolSetting struct {
	value bool
	source
}

type config struct {
	path                         string
	present                      bool
	vmMemory, vmCPUs             intSetting
	autostart                    boolSetting
	idleTimeout                  intSetting
	isolatedMemory, isolatedCPUs intSetting
}

type configKey struct {
	name     string
	number   *intSetting
	flag     *boolSetting
	min, max int
	unit     string
}

// keys lists the settings in their fixed order with pointers into c.
func (c *config) keys() []configKey {
	return []configKey{
		{"vm.memory", &c.vmMemory, nil, 1, 128, "GiB"},
		{"vm.cpus", &c.vmCPUs, nil, 1, 64, "CPUs"},
		{"machines.autostart", nil, &c.autostart, 0, 0, ""},
		{"machines.idle_timeout", &c.idleTimeout, nil, 0, 1440, "minutes"},
		{"isolated.memory", &c.isolatedMemory, nil, 1, 128, "GiB"},
		{"isolated.cpus", &c.isolatedCPUs, nil, 1, 64, "CPUs"},
	}
}

func (k configKey) set(value string, line int) error {
	if k.flag != nil {
		if value != "true" && value != "false" {
			return fmt.Errorf("%s must be true or false, got %q", k.name, value)
		}
		*k.flag = boolSetting{value == "true", source{line: line}}
		return nil
	}
	n, err := strconv.Atoi(value)
	if strings.Trim(value, "0123456789") != "" || err != nil || n < k.min || n > k.max {
		return fmt.Errorf("%s must be a whole number of %s from %d to %d, got %q", k.name, k.unit, k.min, k.max, value)
	}
	*k.number = intSetting{n, source{line: line}}
	return nil
}

func (k configKey) show() (value, from string) {
	var s source
	if k.flag != nil {
		value, s = strconv.FormatBool(k.flag.value), k.flag.source
	} else {
		value, s = strconv.Itoa(k.number.value), k.number.source
	}
	switch {
	case s.line > 0:
		return value, fmt.Sprintf("file (line %d)", s.line)
	case s.reason != "":
		return value, "default (" + s.reason + ")"
	}
	return value, "default"
}

func defaultConfig(path string, host hostFacts) *config {
	// 2<<20 KiB is 2 GiB, so the VM gets half of MemTotal in whole GiB.
	return &config{
		path:           path,
		vmMemory:       intSetting{int(min(max(host.memoryKiB/(2<<20), 2), 128)), source{reason: "half of host memory"}},
		vmCPUs:         intSetting{min(max(host.cpus, 1), 64), source{reason: "host CPUs"}},
		autostart:      boolSetting{true, source{}},
		idleTimeout:    intSetting{15, source{}},
		isolatedMemory: intSetting{2, source{}},
		isolatedCPUs:   intSetting{2, source{}},
	}
}

func configPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	// The XDG base directory spec treats relative values as invalid.
	if !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "nsl", "nsl.conf"), nil
}

func memTotal(meminfo string) (int64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "MemTotal:" {
			continue
		}
		if len(f) == 3 && f[2] == "kB" {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil && n > 0 {
				return n, nil
			}
		}
		break
	}
	return 0, errors.New("/proc/meminfo has no valid MemTotal")
}

func readHostFacts() (hostFacts, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return hostFacts{}, err
	}
	kib, err := memTotal(string(b))
	if err != nil {
		return hostFacts{}, err
	}
	return hostFacts{memoryKiB: kib, cpus: runtime.NumCPU()}, nil
}

func readConfig(path string, host hostFacts) (*config, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return defaultConfig(path, host), nil
	}
	st, err := os.Stat(path)
	// A dangling symlink is a broken setup, not an absent file.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: symlink target does not exist", path)
	}
	if err != nil {
		return nil, err
	}
	// Refuse FIFOs and devices before opening them can block.
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, configLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > configLimit {
		return nil, fmt.Errorf("%s: larger than 64 KiB", path)
	}
	return parseConfig(path, b, host)
}

func parseConfig(path string, data []byte, host hostFacts) (*config, error) {
	c := defaultConfig(path, host)
	c.present = true
	keys := map[string]configKey{}
	for _, k := range c.keys() {
		keys[k.name] = k
	}
	sections, seen := map[string]int{}, map[string]int{}
	section := ""
	for i, raw := range strings.Split(string(data), "\n") {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("%s:%d: %s", path, i+1, fmt.Sprintf(format, args...))
		}
		if !utf8.ValidString(raw) {
			return nil, fail("not valid UTF-8")
		}
		if strings.IndexByte(raw, 0) >= 0 {
			return nil, fail("contains a NUL byte")
		}
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' && line[len(line)-1] == ']' {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name != "vm" && name != "machines" && name != "isolated" {
				return nil, fail("unknown section [%s]; expected [vm], [machines] or [isolated]", name)
			}
			if first, ok := sections[name]; ok {
				return nil, fail("duplicate section [%s] (first on line %d)", name, first)
			}
			sections[name], section = i+1, name
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" {
			return nil, fail("expected [section], key = value or # comment, got %q", line)
		}
		if section == "" {
			return nil, fail("key %q is outside a section", key)
		}
		k, ok := keys[section+"."+key]
		if !ok {
			var valid []string
			for _, other := range c.keys() {
				if s, name, _ := strings.Cut(other.name, "."); s == section {
					valid = append(valid, name)
				}
			}
			return nil, fail("unknown key %q in [%s]; expected %s", key, section, strings.Join(valid, " or "))
		}
		if first, ok := seen[k.name]; ok {
			return nil, fail("duplicate key %s (first on line %d)", k.name, first)
		}
		seen[k.name] = i + 1
		if err := k.set(value, i+1); err != nil {
			return nil, fail("%v", err)
		}
	}
	return c, nil
}

func (a *app) loadConfig() (*config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	host := a.host
	if host == nil {
		h, err := readHostFacts()
		if err != nil {
			return nil, err
		}
		host = &h
	}
	return readConfig(path, *host)
}

func (a *app) configCommand() error {
	c, err := a.loadConfig()
	if err != nil {
		return err
	}
	state := ""
	if !c.present {
		state = " (absent)"
	}
	fmt.Fprintf(a.out, "Configuration file: %s%s\n\n", c.path, state)
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SETTING\tVALUE\tSOURCE")
	for _, k := range c.keys() {
		value, from := k.show()
		fmt.Fprintf(w, "%s\t%s\t%s\n", k.name, value, from)
	}
	return w.Flush()
}
