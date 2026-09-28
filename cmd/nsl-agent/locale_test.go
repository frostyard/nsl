package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeArchive writes a glibc locale-archive whose name table lists names.
func writeArchive(t *testing.T, path string, names ...string) {
	t.Helper()
	le := binary.LittleEndian
	size := len(names) + 1 // an unused slot, as a real hash table has
	strings := uint32(56 + 12*size)
	head := make([]byte, 56)
	le.PutUint32(head, 0xde020109)
	le.PutUint32(head[8:], 56)
	le.PutUint32(head[12:], uint32(len(names)))
	le.PutUint32(head[16:], uint32(size))
	le.PutUint32(head[20:], strings)
	table := make([]byte, 12*size)
	var text []byte
	for i, name := range names {
		le.PutUint32(table[12*(i+1)+4:], strings+uint32(len(text)))
		le.PutUint32(table[12*(i+1)+8:], 1)
		text = append(append(text, name...), 0)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append(head, table...), text...), 0644); err != nil {
		t.Fatal(err)
	}
}

func compiledLocale(t *testing.T, tree, name string) {
	t.Helper()
	dir := tree + "/usr/lib/locale/" + name
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/LC_CTYPE", nil, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLocaleEnvKeepsOnlyLocalesTheMachineHas(t *testing.T) {
	ta := newTestAgent(t, "shared")
	ta.addMachine(t, "debian", machineID)
	tree := ta.root + machinesDir + "/debian"
	compiledLocale(t, tree, "C.utf8")
	compiledLocale(t, tree, "de_DE.utf8")
	writeArchive(t, tree+"/usr/lib/locale/locale-archive", "en_GB.utf8", "sr_RS.utf8@latin")
	// Outside the machine: a locale that must not count.
	compiledLocale(t, ta.root, "fr_FR.utf8")
	env := map[string]string{"TERM": "xterm", "LANGUAGE": "en_US:en", "LANG": "en_US.UTF-8", "LC_TIME": "en_GB.UTF-8",
		"LC_PAPER": "de_DE.UTF-8", "LC_NAME": "sr_RS.UTF-8@latin", "LC_MONETARY": "xx_XX.UTF-8",
		"LC_ADDRESS": "../../../../usr/lib/locale/fr_FR.utf8", "LC_MEASUREMENT": "C", "LC_ALL": "fr_FR.UTF-8"}
	want := map[string]string{"TERM": "xterm", "LANGUAGE": "en_US:en", "LANG": fallbackLocale, "LC_TIME": "en_GB.UTF-8",
		"LC_PAPER": "de_DE.UTF-8", "LC_NAME": "sr_RS.UTF-8@latin", "LC_MEASUREMENT": "C"}
	if got := ta.localeEnv("debian", env); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// A machine's own locale is kept.
	if got := ta.localeEnv("debian", map[string]string{"LANG": "de_DE.UTF-8"}); got["LANG"] != "de_DE.UTF-8" {
		t.Fatal(got)
	}
}

func TestLocaleEnvWithoutLocaleData(t *testing.T) {
	ta := newTestAgent(t, "shared")
	ta.addMachine(t, "opensuse", machineID)
	writeArchive(t, ta.root+machinesDir+"/opensuse/usr/lib/locale/locale-archive") // empty
	got := ta.localeEnv("opensuse", map[string]string{"LANG": "en_US.UTF-8", "LC_ALL": "en_US.UTF-8", "TERM": "foot"})
	if !reflect.DeepEqual(got, map[string]string{"LANG": fallbackLocale, "TERM": "foot"}) {
		t.Fatal(got)
	}
	// A corrupt archive lists nothing.
	os.WriteFile(ta.root+machinesDir+"/opensuse/usr/lib/locale/locale-archive", []byte("not an archive"), 0644)
	if got := ta.localeEnv("opensuse", map[string]string{"LANG": "en_US.UTF-8"}); got["LANG"] != fallbackLocale {
		t.Fatal(got)
	}
	if got := ta.localeEnv("missing", map[string]string{"LANG": "en_US.UTF-8", "TERM": "xterm"}); got["LANG"] != fallbackLocale || got["TERM"] != "xterm" {
		t.Fatal(got)
	}
}

func TestNormalizeLocale(t *testing.T) {
	for in, want := range map[string]string{
		"en_US.UTF-8": "en_US.utf8", "de_DE.ISO-8859-1": "de_DE.iso88591", "ja_JP.eucJP": "ja_JP.eucjp",
		"sr_RS.UTF-8@latin": "sr_RS.utf8@latin", "en_US": "en_US", "xx.8859": "xx.iso8859", "C.UTF-8": "C.utf8",
	} {
		if got := normalizeLocale(in); got != want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}
