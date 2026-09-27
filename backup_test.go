package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func backupFixture(t *testing.T) (*app, *fakeRunner, *environment, string) {
	t.Helper()
	a, f, e := fixture(t)
	e.Project = t.TempDir()
	e.Desktop = true
	e.Initialized = true
	if err := a.save(e); err != nil {
		t.Fatal(err)
	}
	disk := make([]byte, 65536)
	copy(disk, []byte{'Q', 'F', 'I', 0xfb})
	binary.BigEndian.PutUint32(disk[4:], 3)
	binary.BigEndian.PutUint32(disk[20:], 16)
	binary.BigEndian.PutUint64(disk[24:], uint64(int64(e.Disk)*gib))
	binary.BigEndian.PutUint32(disk[100:], 104)
	if err := os.WriteFile(filepath.Join(a.dir(e.Name), "disk.qcow2"), disk, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.dir(e.Name), "known_hosts"), []byte("nsl-"+e.ID+" ssh-ed25519 AAAA\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return a, f, e, filepath.Join(t.TempDir(), "dev.nsl")
}

func TestBackupRestoresWithoutCacheAndKeepsGuestBinding(t *testing.T) {
	a, _, e, path := backupFixture(t)
	if err := a.execute([]string{"export", e.Name, path}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("private archive: %v %v", st, err)
	}
	b, f := testApp(t)
	if err = b.execute([]string{"restore", "restored", path}); err != nil {
		t.Fatal(err)
	}
	restored, err := b.owned("restored")
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID == e.ID || cid(restored) == cid(e) || unit(restored) == unit(e) || guestID(restored) != e.ID || restored.Project != "" || restored.Desktop || !restored.Prepared || !restored.Initialized {
		t.Fatalf("incorrect restored identity/grants: %+v", restored)
	}
	for _, name := range backupNames {
		original, err := os.ReadFile(filepath.Join(a.dir(e.Name), name))
		if err != nil {
			t.Fatal(err)
		}
		copy, err := os.ReadFile(filepath.Join(b.dir(restored.Name), name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, copy) {
			t.Fatalf("%s changed", name)
		}
	}
	imageFiles, err := os.ReadDir(filepath.Join(b.home, "images"))
	if err != nil || len(imageFiles) != 0 {
		t.Fatalf("restore used cache: %v %v", imageFiles, err)
	}
	var credential struct {
		ID string `json:"id"`
	}
	data, _ := os.ReadFile(filepath.Join(b.dir(restored.Name), "boot.json"))
	if err = json.Unmarshal(data, &credential); err != nil || credential.ID != guestID(e) {
		t.Fatalf("boot binding: %s %v", data, err)
	}
	config, _ := os.ReadFile(filepath.Join(b.dir(restored.Name), "ssh.config"))
	if !strings.Contains(string(config), "HostKeyAlias nsl-"+e.ID) || !strings.Contains(string(config), b.socket(restored)) {
		t.Fatal("SSH config lost trust binding or new socket")
	}
	if err = b.start(restored); err != nil {
		t.Fatal(err)
	}
	if err = b.stop(restored); err != nil {
		t.Fatal(err)
	}
	again := filepath.Join(t.TempDir(), "again.nsl")
	if err = b.export(restored.Name, again); err != nil {
		t.Fatal(err)
	}
	if err = b.restore("second", []string{again, "--project", e.Project, "--desktop"}); err != nil {
		t.Fatal(err)
	}
	second, err := b.owned("second")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == restored.ID || second.GuestID != e.ID || second.Project != e.Project || !second.Desktop {
		t.Fatalf("repeated restore: %+v", second)
	}
	for _, c := range f.calls {
		if c.Bin == "ssh-keygen" && c.Args[0] != "-y" {
			t.Fatal("regenerated backup key")
		}
	}
}

func TestExportRefusesRunningAndExistingDestination(t *testing.T) {
	a, f, e, path := backupFixture(t)
	running(f, e)
	if err := a.export(e.Name, path); err == nil {
		t.Fatal("exported running VM")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created archive while running")
	}
	f.states[unit(e)] = "inactive"
	if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.export(e.Name, path); err == nil {
		t.Fatal("overwrote destination")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "keep me" {
		t.Fatal("changed destination")
	}
	f.calls = nil
	if err := a.restore(e.Name, []string{path}); err == nil {
		t.Fatal("overwrote environment")
	}
	if len(f.calls) != 0 {
		t.Fatal("called backend for duplicate restore")
	}
}

type archiveEntry struct {
	header tar.Header
	data   []byte
}

func loadTestArchive(t *testing.T, path string) []archiveEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(data))
	var entries []archiveEntry
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, archiveEntry{*h, b})
	}
	return entries
}
func saveTestArchive(t *testing.T, path string, entries []archiveEntry) {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, entry := range entries {
		if err := writer.WriteHeader(&entry.header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}
func changeManifest(t *testing.T, entries []archiveEntry, edit func(*backupManifest)) []archiveEntry {
	t.Helper()
	var m backupManifest
	if err := json.Unmarshal(entries[0].data, &m); err != nil {
		t.Fatal(err)
	}
	edit(&m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	entries[0].data = b
	entries[0].header.Size = int64(len(b))
	return entries
}
func TestMalformedBackupsNeverPublishOrCallBackend(t *testing.T) {
	a, _, e, path := backupFixture(t)
	if err := a.export(e.Name, path); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func([]archiveEntry) []archiveEntry{
		"checksum":  func(es []archiveEntry) []archiveEntry { es[1].data[150] ^= 1; return es },
		"traversal": func(es []archiveEntry) []archiveEntry { es[1].header.Name = "../escape"; return es },
		"absolute":  func(es []archiveEntry) []archiveEntry { es[1].header.Name = "/tmp/escape"; return es },
		"duplicate": func(es []archiveEntry) []archiveEntry { return append(es, es[1]) },
		"missing":   func(es []archiveEntry) []archiveEntry { return es[:len(es)-1] },
		"symlink": func(es []archiveEntry) []archiveEntry {
			es[1].header.Typeflag = tar.TypeSymlink
			es[1].header.Linkname = "/tmp/escape"
			es[1].header.Size = 0
			es[1].data = nil
			return es
		},
		"hardlink": func(es []archiveEntry) []archiveEntry {
			es[1].header.Typeflag = tar.TypeLink
			es[1].header.Linkname = "keys/identity"
			es[1].header.Size = 0
			es[1].data = nil
			return es
		},
		"wrong uid": func(es []archiveEntry) []archiveEntry {
			return changeManifest(t, es, func(m *backupManifest) { m.UID++ })
		},
		"wrong gid": func(es []archiveEntry) []archiveEntry {
			return changeManifest(t, es, func(m *backupManifest) { m.GID++ })
		},
		"version": func(es []archiveEntry) []archiveEntry {
			return changeManifest(t, es, func(m *backupManifest) { m.Version++ })
		},
		"architecture": func(es []archiveEntry) []archiveEntry {
			return changeManifest(t, es, func(m *backupManifest) { m.Architecture = "arm64" })
		},
		"unexpected manifest path": func(es []archiveEntry) []archiveEntry {
			return changeManifest(t, es, func(m *backupManifest) { m.Files["../../escape"] = m.Files["disk.qcow2"] })
		},
		"resource limit": func(es []archiveEntry) []archiveEntry {
			return changeManifest(t, es, func(m *backupManifest) { m.Disk = 4097 })
		},
		"oversized key": func(es []archiveEntry) []archiveEntry {
			return changeManifest(t, es, func(m *backupManifest) { f := m.Files["keys/identity"]; f.Size = 65537; m.Files["keys/identity"] = f })
		},
		"external disk": func(es []archiveEntry) []archiveEntry {
			binary.BigEndian.PutUint64(es[1].data[8:], 200)
			hash := sha256.Sum256(es[1].data)
			return changeManifest(t, es, func(m *backupManifest) {
				f := m.Files["disk.qcow2"]
				f.SHA256 = hex.EncodeToString(hash[:])
				m.Files["disk.qcow2"] = f
			})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			entries := mutate(loadTestArchive(t, path))
			bad := filepath.Join(t.TempDir(), "bad.nsl")
			saveTestArchive(t, bad, entries)
			b, f := testApp(t)
			if err := b.restore("bad", []string{bad}); err == nil {
				t.Fatal("accepted malformed archive")
			}
			if _, err := os.Stat(b.dir("bad")); !os.IsNotExist(err) {
				t.Fatal("published invalid environment")
			}
			temps, _ := filepath.Glob(filepath.Join(b.home, ".restore-*"))
			if len(temps) != 0 {
				t.Fatal("left staging directory")
			}
			if len(f.calls) != 0 {
				t.Fatalf("called backend for invalid archive: %+v", f.calls)
			}
		})
	}
}
func TestBackupRejectsTruncationAndTrailingData(t *testing.T) {
	a, _, e, path := backupFixture(t)
	if err := a.export(e.Name, path); err != nil {
		t.Fatal(err)
	}
	valid, _ := os.ReadFile(path)
	for name, data := range map[string][]byte{"truncated": valid[:len(valid)/2], "trailing": append(valid, 1)} {
		t.Run(name, func(t *testing.T) {
			bad := filepath.Join(t.TempDir(), "bad.nsl")
			if err := os.WriteFile(bad, data, 0600); err != nil {
				t.Fatal(err)
			}
			b, _ := testApp(t)
			if err := b.restore("bad", []string{bad}); err == nil {
				t.Fatal("accepted invalid length")
			}
		})
	}
}
func TestStandaloneDiskRejectsUnsafeHeaders(t *testing.T) {
	a, _, e, _ := backupFixture(t)
	original, _ := os.ReadFile(filepath.Join(a.dir(e.Name), "disk.qcow2"))
	mutations := map[string]func([]byte){
		"backing":            func(b []byte) { binary.BigEndian.PutUint64(b[8:], 100) },
		"external":           func(b []byte) { binary.BigEndian.PutUint64(b[72:], 4) },
		"dirty":              func(b []byte) { binary.BigEndian.PutUint64(b[72:], 1) },
		"corrupt":            func(b []byte) { binary.BigEndian.PutUint64(b[72:], 2) },
		"size":               func(b []byte) { binary.BigEndian.PutUint64(b[24:], 1) },
		"encrypt":            func(b []byte) { binary.BigEndian.PutUint32(b[32:], 2) },
		"snapshot":           func(b []byte) { binary.BigEndian.PutUint32(b[60:], 1) },
		"external extension": func(b []byte) { binary.BigEndian.PutUint32(b[104:], 0x44415441) },
		"extension overflow": func(b []byte) {
			binary.BigEndian.PutUint32(b[104:], 123)
			binary.BigEndian.PutUint32(b[108:], 0xffffffff)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), original...)
			mutate(b)
			path := filepath.Join(t.TempDir(), "disk")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if err := standaloneDisk(path, e.Disk); err == nil {
				t.Fatal("accepted unsafe disk")
			}
		})
	}
}

func TestBackupValidationFailuresPreserveSourceAndDestination(t *testing.T) {
	a, f, e, path := backupFixture(t)
	f.failConvert = true
	if err := a.export(e.Name, path); err == nil {
		t.Fatal("ignored failed export conversion")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("published failed export")
	}
	f.failConvert = false
	if err := a.export(e.Name, path); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"check", "key"} {
		t.Run(failure, func(t *testing.T) {
			b, f := testApp(t)
			f.failCheck = failure == "check"
			f.wrongKey = failure == "key"
			if err := b.restore("bad", []string{path}); err == nil {
				t.Fatal("accepted invalid disk/key")
			}
			if _, err := os.Stat(b.dir("bad")); !os.IsNotExist(err) {
				t.Fatal("published rejected disk/key")
			}
		})
	}
	if _, err := a.owned(e.Name); err != nil {
		t.Fatal("source metadata damaged", err)
	}
}

func TestLargeBackupHeaderDoesNotRequirePAX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	// The short fixture intentionally fails the data-length check after writing
	// the header. Inspect that header without streaming nine GiB in a unit test.
	err := writeBackupEntry(writer, "disk.qcow2", path, backupFile{Size: 9 * gib, SHA256: strings.Repeat("0", 64)})
	if err == nil {
		t.Fatal("did not check short data")
	}
	header, err := tar.NewReader(bytes.NewReader(data.Bytes())).Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "disk.qcow2" || header.Size != 9*gib || len(header.PAXRecords) != 0 || header.Typeflag != tar.TypeReg {
		t.Fatalf("large export would be rejected by restore: %+v", header)
	}
}
