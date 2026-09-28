package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frostyard/nsl/internal/protocol"
)

const exported = "compressed root filesystem"

// exportTo exports debian with a fake agent that streams exported.
func exportTo(t *testing.T, a *app, f *fakeRunner) string {
	t.Helper()
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "export" {
			io.WriteString(stdout, exported)
		}
		return nil
	}
	path := filepath.Join(t.TempDir(), "debian.nsl")
	if err := a.execute([]string{"export", "debian", path}); err != nil {
		t.Fatal(err)
	}
	f.agent = nil
	return path
}

func TestExportWritesAPrivateArchive(t *testing.T) {
	a, f, m := withMachine(t)
	path := exportTo(t, a, f)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal(err)
	}
	if last := f.requests[len(f.requests)-1]; last.Op != "export" || last.ID != m.ID {
		t.Fatalf("%+v", last)
	}
	b, _ := os.ReadFile(path)
	r := tar.NewReader(bytes.NewReader(b))
	h, err := r.Next()
	if err != nil || h.Name != "manifest.json" {
		t.Fatal(err, h)
	}
	var manifest archiveManifest
	content, _ := io.ReadAll(r)
	if err = protocol.DecodeStrict(content, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Format != 1 || manifest.Machine != "debian" || manifest.Account.User != "u" || manifest.Account.UID != a.uid ||
		manifest.BuildID != m.BuildID || manifest.Rootfs.Size != int64(len(exported)) || manifest.Rootfs.Digest != hashBytes([]byte(exported)) {
		t.Fatalf("%+v", manifest)
	}
	if h, err = r.Next(); err != nil || h.Name != "rootfs.tar.zst" {
		t.Fatal(err, h)
	}
	if body, _ := io.ReadAll(r); string(body) != exported {
		t.Fatal(string(body))
	}
	if _, err = r.Next(); err != io.EOF {
		t.Fatal(err)
	}
	if _, err := checkArchive(path, a.uid, a.gid); err != nil {
		t.Fatal(err)
	}
	// Never replace a path, and leave nothing behind after a refusal.
	before := len(f.requests)
	if err := a.execute([]string{"export", "debian", path}); err == nil || !strings.Contains(err.Error(), "exists") || len(f.requests) != before {
		t.Fatal(err)
	}
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "export" {
			return &protocol.Error{Code: protocol.CodeBusy, Message: "debian is running; stop it first"}
		}
		return nil
	}
	other := filepath.Join(filepath.Dir(path), "other.nsl")
	if err := a.execute([]string{"export", "debian", other}); err == nil || !strings.Contains(err.Error(), "stop debian first") {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatal("left staging files:", entries)
	}
}

func TestImportUsesTheArchiveAndNotItsTier(t *testing.T) {
	a, f, _ := withMachine(t)
	path := exportTo(t, a, f)
	var received []byte
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "import" {
			received, _ = io.ReadAll(stdin)
		}
		return nil
	}
	if err := a.execute([]string{"import", "copy", path}); err != nil {
		t.Fatal(err)
	}
	req := f.requests[len(f.requests)-1]
	if req.Op != "import" || string(received) != exported || req.Rootfs.Digest != hashBytes([]byte(exported)) ||
		req.Rootfs.Size != int64(len(exported)) || req.Account.User != "u" || req.Account.UID != a.uid || req.TimeZone == "" {
		t.Fatalf("%+v %q", req, received)
	}
	m, err := a.machine("copy")
	if err != nil || !m.Prepared || m.Tier != "shared" || m.Image != "" || m.BuildID != "nsl-machine-debian-trixie-x86-64-r1" {
		t.Fatal(err, m)
	}
	if current, _ := a.defaultMachine(); current != "debian" {
		t.Fatal("an import took the default")
	}
	if err := a.execute([]string{"import", "copy", path}); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatal(err)
	}
	// The tier comes from the flag: the same archive becomes an isolated machine.
	if err := a.execute([]string{"import", "iso", path, "--isolated"}); err != nil {
		t.Fatal(err)
	}
	if m, err := a.machine("iso"); err != nil || m.Tier != "isolated" {
		t.Fatal(err, m)
	}
	if v, err := a.loadVMAt(a.isolatedVMDir("iso")); err != nil || v == nil || v.Machine.Name != "iso" {
		t.Fatal(err, v)
	}
}

// archiveWith writes an archive from a manifest, a root filesystem and trailing bytes.
func archiveWith(t *testing.T, manifest archiveManifest, rootfs, trailing []byte) string {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	content, _ := json.Marshal(manifest)
	w.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(content))})
	w.Write(content)
	w.WriteHeader(&tar.Header{Name: "rootfs.tar.zst", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(rootfs))})
	w.Write(rootfs)
	w.Close()
	b.Write(trailing)
	path := filepath.Join(t.TempDir(), "archive.nsl")
	os.WriteFile(path, b.Bytes(), 0600)
	return path
}

func TestImportRefusesBadArchives(t *testing.T) {
	a, f, _ := withMachine(t)
	good := func() archiveManifest {
		return archiveManifest{Format: 1, Architecture: "x86-64", Machine: "debian", Account: protocol.Account{User: "u", Group: "u", UID: a.uid, GID: a.gid},
			BuildID: "nsl-machine-debian-trixie-x86-64-r1", Created: "2026-09-28T00:00:00Z", Rootfs: archiveFile{Digest: hashBytes([]byte("rootfs")), Size: 6}}
	}
	if _, err := checkArchive(archiveWith(t, good(), []byte("rootfs"), nil), a.uid, a.gid); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		change   func(*archiveManifest)
		rootfs   string
		trailing string
		message  string
	}{
		"format":       {func(m *archiveManifest) { m.Format = 2 }, "rootfs", "", "unsupported archive format 2"},
		"uid":          {func(m *archiveManifest) { m.Account.UID++ }, "rootfs", "", "UID"},
		"architecture": {func(m *archiveManifest) { m.Architecture = "arm64" }, "rootfs", "", "x86-64"},
		"damaged":      {nil, "rootfZ", "", "damaged"},
		"size":         {func(m *archiveManifest) { m.Rootfs.Size = 7 }, "rootfs", "", "second entry"},
		"trailing":     {nil, "rootfs", "junk", "trailing"},
		"account":      {func(m *archiveManifest) { m.Account.User = "Root" }, "rootfs", "", "invalid archive manifest"},
	} {
		m := good()
		if tc.change != nil {
			tc.change(&m)
		}
		before := len(f.requests)
		err := a.execute([]string{"import", "x" + strings.ReplaceAll(name, " ", ""), archiveWith(t, m, []byte(tc.rootfs), []byte(tc.trailing))})
		if err == nil || !strings.Contains(err.Error(), tc.message) || len(f.requests) != before {
			t.Fatalf("%s: %v", name, err)
		}
	}
	notTar := filepath.Join(t.TempDir(), "x")
	os.WriteFile(notTar, []byte("hello"), 0600)
	if err := a.execute([]string{"import", "x", notTar}); err == nil {
		t.Fatal("imported a non-archive")
	}
	// An agent refusal leaves no machine behind.
	f.agent = func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error {
		if req.Op == "import" {
			return &protocol.Error{Code: protocol.CodeRefused, Message: "the archive has no account u"}
		}
		return nil
	}
	err := a.execute([]string{"import", "refused", archiveWith(t, good(), []byte("rootfs"), nil)})
	var agentErr *protocol.Error
	if !errors.As(err, &agentErr) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(a.machinePath("refused")); !os.IsNotExist(err) {
		t.Fatal("kept a refused import")
	}
}
