package main

import (
	"encoding/binary"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// The host's locale variables name locales a machine may lack
// (docs/specs/agent.md#run). For a missing one glibc falls back to the ASCII
// "C" locale and programs warn; every machine image has C.UTF-8 instead.
const fallbackLocale = "C.UTF-8"

// localeEnv keeps the locale variables the machine can use: LANG naming a
// missing locale becomes C.UTF-8, and such an LC_* variable is dropped so that
// LANG applies.
func (a *agent) localeEnv(name string, env map[string]string) map[string]string {
	var t *tree
	defer func() {
		if t != nil {
			t.close()
		}
	}()
	available := func(locale string) bool {
		if t == nil {
			var err error
			if t, err = openTree(a.path(machinesDir + "/" + name)); err != nil {
				return false
			}
		}
		return hasLocale(t, locale)
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		switch {
		case k != "LANG" && !strings.HasPrefix(k, "LC_"):
			out[k] = v
		case available(v):
			out[k] = v
		case k == "LANG":
			out[k] = fallbackLocale
		}
	}
	return out
}

// hasLocale reports whether glibc in the tree can load locale: compiled under
// /usr/lib/locale or listed in its locale-archive, by name or with the
// codeset normalized as glibc does ("UTF-8" becomes "utf8").
func hasLocale(t *tree, locale string) bool {
	if locale == "C" || locale == "POSIX" {
		return true
	}
	if locale == "" || strings.ContainsAny(locale, "/") || locale == "." || locale == ".." {
		return false
	}
	names := []string{locale}
	if n := normalizeLocale(locale); n != locale {
		names = append(names, n)
	}
	for _, n := range names {
		if fd, err := t.open("usr/lib/locale/"+n+"/LC_CTYPE", unix.O_PATH, 0); err == nil {
			unix.Close(fd)
			return true
		}
	}
	archived := archiveLocales(t)
	for _, n := range names {
		if archived[n] {
			return true
		}
	}
	return false
}

// normalizeLocale lowercases the codeset and drops its punctuation, as glibc's
// _nl_normalize_codeset does; an all-digit codeset gains an "iso" prefix.
func normalizeLocale(locale string) string {
	base, codeset, ok := strings.Cut(locale, ".")
	if !ok {
		return locale
	}
	codeset, modifier, _ := strings.Cut(codeset, "@")
	var b strings.Builder
	digits := true
	for _, r := range strings.ToLower(codeset) {
		switch {
		case r >= 'a' && r <= 'z':
			digits = false
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	normal := b.String()
	if digits && normal != "" {
		normal = "iso" + normal
	}
	if modifier != "" {
		modifier = "@" + modifier
	}
	return base + "." + normal + modifier
}

// archiveLocales lists the names in the tree's locale-archive. It reads only
// the header, the name hash table and the strings they point to, never the
// locale data, which can be hundreds of megabytes.
func archiveLocales(t *tree) map[string]bool {
	fd, err := t.open("usr/lib/locale/locale-archive", unix.O_RDONLY, 0)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), "locale-archive")
	defer f.Close()
	// struct locarhead from glibc's locarchive.h: fourteen uint32 fields.
	head := make([]byte, 56)
	if _, err = f.ReadAt(head, 0); err != nil {
		return nil
	}
	le := binary.LittleEndian
	if le.Uint32(head) != 0xde020109 {
		return nil
	}
	tableOffset, tableSize := int64(le.Uint32(head[8:])), int64(le.Uint32(head[16:]))
	if tableSize > 1<<16 {
		return nil
	}
	// struct namehashent: hashval, name_offset and locrec_offset.
	table := make([]byte, tableSize*12)
	if _, err = f.ReadAt(table, tableOffset); err != nil {
		return nil
	}
	names := map[string]bool{}
	name := make([]byte, 256)
	for i := int64(0); i < tableSize; i++ {
		entry := table[i*12:]
		offset, record := int64(le.Uint32(entry[4:])), le.Uint32(entry[8:])
		if offset == 0 || record == 0 {
			continue
		}
		n, _ := f.ReadAt(name, offset)
		if end := strings.IndexByte(string(name[:n]), 0); end > 0 {
			names[string(name[:end])] = true
		}
	}
	return names
}
