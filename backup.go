package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const gib = int64(1024 * 1024 * 1024)

var backupNames = []string{"disk.qcow2", "keys/identity", "keys/identity.pub", "known_hosts"}

type backupFile struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type backupManifest struct {
	Version      int                   `json:"version"`
	Architecture string                `json:"architecture"`
	GuestID      string                `json:"guest_id"`
	UID          int                   `json:"uid"`
	GID          int                   `json:"gid"`
	CPUs         int                   `json:"cpus"`
	Memory       int                   `json:"memory_gib"`
	Disk         int                   `json:"disk_gib"`
	ImageSHA256  string                `json:"image_sha256"`
	Initialized  bool                  `json:"initialized"`
	Files        map[string]backupFile `json:"files"`
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Inspect references before giving an imported image to QEMU. A backup must
// contain the disk data itself; it cannot ask QEMU to open other host paths.
// See https://www.qemu.org/docs/master/interop/qcow2.html.
func standaloneDisk(path string, diskGiB int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := make([]byte, 104)
	if _, err = io.ReadFull(f, h); err != nil {
		return err
	}
	u32 := func(i int) uint32 { return binary.BigEndian.Uint32(h[i : i+4]) }
	u64 := func(i int) uint64 { return binary.BigEndian.Uint64(h[i : i+8]) }
	if !bytes.Equal(h[:4], []byte{'Q', 'F', 'I', 0xfb}) || (u32(4) != 2 && u32(4) != 3) {
		return errors.New("backup disk must be qcow2 version 2 or 3")
	}
	if u64(8) != 0 || u32(16) != 0 || u32(32) != 0 || u32(60) != 0 {
		return errors.New("backup disk must have no backing file, encryption or internal snapshots")
	}
	if u32(20) < 9 || u32(20) > 21 || u64(24) != uint64(int64(diskGiB)*gib) {
		return errors.New("backup disk size or cluster geometry mismatch")
	}
	header := uint32(72)
	if u32(4) == 3 {
		// Only the compression and extended-L2 feature bits are allowed.
		if u64(72) & ^uint64(24) != 0 || u64(88)&2 != 0 {
			return errors.New("backup disk is dirty, corrupt or uses unsupported/external data features")
		}
		header = u32(100)
	}
	cluster := uint32(1) << u32(20)
	if header < 72 || (u32(4) == 3 && header < 104) || header%8 != 0 || header > cluster-8 {
		return errors.New("invalid qcow2 header length")
	}
	data := make([]byte, cluster)
	if _, err = f.ReadAt(data, 0); err != nil {
		return err
	}
	for off := uint64(header); off+8 <= uint64(cluster); {
		kind := binary.BigEndian.Uint32(data[off : off+4])
		size := uint64(binary.BigEndian.Uint32(data[off+4 : off+8]))
		if kind == 0 {
			return nil
		}
		if kind == 0x44415441 || kind == 0xe2792aca || kind == 0x0537be77 {
			return errors.New("external disk reference or encryption extension in backup")
		}
		off += 8 + ((size + 7) &^ uint64(7))
		if off > uint64(cluster) {
			return errors.New("invalid qcow2 extension length")
		}
	}
	return errors.New("unterminated qcow2 extensions")
}

func fileDigest(path string) (backupFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return backupFile{}, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	return backupFile{Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}, err
}

func (a *app) export(name, destination string) error {
	e, err := a.owned(name)
	if err != nil {
		return err
	}
	l, err := a.lock(name)
	if err != nil {
		return err
	}
	defer unlock(l)
	e, err = a.owned(name)
	if err != nil {
		return err
	}
	if !e.Prepared {
		return errors.New("cannot export incomplete preparation; use recover first")
	}
	state, err := a.unitState(e, unit(e))
	if err != nil {
		return err
	}
	if state != "inactive" && state != "failed" {
		return fmt.Errorf("stop %s before exporting it", name)
	}
	if _, err = os.Lstat(destination); !os.IsNotExist(err) {
		return errors.New("backup destination exists or cannot be inspected")
	}
	if err = a.runtimeFiles(e); err != nil {
		return err
	}
	if err = privateFile(filepath.Join(a.dir(name), "keys/identity.pub"), a.uid, 0022); err != nil {
		return err
	}
	disk := filepath.Join(a.dir(name), "disk.qcow2")
	if err = standaloneDisk(disk, e.Disk); err != nil {
		return err
	}
	if err = a.call(nil, a.err, "qemu-img", "check", "-f", "qcow2", disk); err != nil {
		return fmt.Errorf("disk check before export: %w", err)
	}
	staging, err := os.MkdirTemp(a.dir(name), ".export-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	copyDisk := filepath.Join(staging, "disk.qcow2")
	if err = a.call(nil, a.err, "qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", disk, copyDisk); err != nil {
		return err
	}
	if err = standaloneDisk(copyDisk, e.Disk); err != nil {
		return err
	}
	manifest := backupManifest{Version: 1, Architecture: "amd64", GuestID: guestID(e), UID: e.Owner, GID: e.GID, CPUs: e.CPUs, Memory: e.Memory, Disk: e.Disk, ImageSHA256: e.Digest, Initialized: e.Initialized, Files: map[string]backupFile{}}
	paths := map[string]string{}
	for _, name := range backupNames {
		path := filepath.Join(a.dir(e.Name), name)
		if name == "disk.qcow2" {
			path = copyDisk
		}
		if name == "known_hosts" {
			if _, err = os.Lstat(path); os.IsNotExist(err) {
				continue
			}
		}
		if err = privateFile(path, a.uid, 0022); err != nil {
			return err
		}
		entry, err := fileDigest(path)
		if err != nil {
			return err
		}
		manifest.Files[name] = entry
		paths[name] = path
	}
	if err = manifest.validate(a); err != nil {
		return err
	}
	output, err := os.CreateTemp(filepath.Dir(destination), ".nsl-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	defer output.Close()
	archive := tar.NewWriter(output)
	metadata, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = archive.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(metadata)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err = archive.Write(metadata); err != nil {
		return err
	}
	for _, name := range backupNames {
		path, ok := paths[name]
		if !ok {
			continue
		}
		if err = writeBackupEntry(archive, name, path, manifest.Files[name]); err != nil {
			return err
		}
	}
	if err = archive.Close(); err != nil {
		return err
	}
	if err = output.Sync(); err != nil {
		return err
	}
	if err = output.Close(); err != nil {
		return err
	}
	// Hard-link publication is atomic and refuses replacement even if another
	// process creates the destination while we are streaming the archive.
	if err = os.Link(output.Name(), destination); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(destination)); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Exported %s to %s (contains private credentials; host shares excluded)\n", name, destination)
	return nil
}
func writeBackupEntry(w *tar.Writer, name, path string, entry backupFile) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// GNU numeric fields support disks above 8 GiB without a PAX extension;
	// restore deliberately rejects PAX metadata and sparse-file extensions.
	if err = w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: entry.Size, Typeflag: tar.TypeReg, Format: tar.FormatGNU}); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), f)
	if err != nil {
		return err
	}
	if n != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
		return fmt.Errorf("%s changed during export", name)
	}
	return nil
}
func (m *backupManifest) validate(a *app) error {
	if m.Version != 1 || m.Architecture != "amd64" || m.UID != a.uid || m.GID != a.gid || m.UID <= 0 || m.GID <= 0 || m.UID >= 1<<31 || m.GID >= 1<<31 {
		return errors.New("backup requires version 1, x86_64 and the same host UID/primary GID")
	}
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(m.GuestID) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(m.ImageSHA256) || m.CPUs < 1 || m.CPUs > 64 || m.Memory < 1 || m.Memory > 128 || m.Disk < 4 || m.Disk > 4096 {
		return errors.New("invalid backup identity or resource limits")
	}
	for _, name := range backupNames[:3] {
		if _, ok := m.Files[name]; !ok {
			return fmt.Errorf("missing %s in backup manifest", name)
		}
	}
	for name, entry := range m.Files {
		limit := int64(65536)
		switch name {
		case "disk.qcow2":
			limit = int64(m.Disk)*gib*5/4 + 64*1024*1024
		case "keys/identity", "keys/identity.pub", "known_hosts":
		default:
			return fmt.Errorf("unexpected backup file %q", name)
		}
		if entry.Size < 1 || entry.Size > limit || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(entry.SHA256) {
			return fmt.Errorf("invalid backup file size/hash for %s", name)
		}
	}
	return nil
}

func readBackup(input io.Reader, staging string, a *app) (*backupManifest, error) {
	archive := tar.NewReader(input)
	header, err := archive.Next()
	if err != nil {
		return nil, err
	}
	if header.Name != "manifest.json" || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 65536 || len(header.PAXRecords) != 0 || header.Linkname != "" {
		return nil, errors.New("backup must start with a bounded regular manifest.json")
	}
	var manifest backupManifest
	decoder := json.NewDecoder(archive)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil {
		return nil, err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing backup manifest data")
	}
	if err = manifest.validate(a); err != nil {
		return nil, err
	}
	if err = os.Mkdir(filepath.Join(staging, "keys"), 0700); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for {
		header, err = archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		entry, ok := manifest.Files[header.Name]
		if !ok || seen[header.Name] || header.Typeflag != tar.TypeReg || header.Size != entry.Size || len(header.PAXRecords) != 0 || header.Linkname != "" {
			return nil, fmt.Errorf("unexpected, duplicate or invalid backup entry %q", header.Name)
		}
		seen[header.Name] = true
		f, err := os.OpenFile(filepath.Join(staging, header.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(f, hash), archive)
		if copyErr == nil {
			copyErr = f.Sync()
		}
		closeErr := f.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if n != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return nil, fmt.Errorf("backup checksum mismatch for %s", header.Name)
		}
	}
	if len(seen) != len(manifest.Files) {
		return nil, errors.New("backup archive is missing files")
	}
	// Reject appended archives or unexpected data beyond the tar end markers.
	trailing, err := io.Copy(io.Discard, io.LimitReader(input, 1))
	if err != nil {
		return nil, err
	}
	if trailing != 0 {
		return nil, errors.New("trailing data after backup archive")
	}
	if err = syncDir(filepath.Join(staging, "keys")); err != nil {
		return nil, err
	}
	return &manifest, syncDir(staging)
}

func (a *app) restore(name string, args []string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if len(args) < 1 {
		return errors.New("usage: restore NAME FILE.nsl [--project DIR] [--desktop]")
	}
	if runtime.GOARCH != "amd64" {
		return errors.New("VM restore currently supports x86_64 only")
	}
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(a.err)
	project := fs.String("project", "", "host project mounted at /work")
	desktop := fs.Bool("desktop", false, "allow Waypipe sessions")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected restore arguments")
	}
	projectDir, err := projectPath(*project)
	if err != nil {
		return err
	}
	if err = a.init(); err != nil {
		return err
	}
	manager, err := fileLock(filepath.Join(a.home, "lock"))
	if err != nil {
		return err
	}
	defer unlock(manager)
	if _, err = os.Lstat(a.dir(name)); !os.IsNotExist(err) {
		return errors.New("environment already exists or cannot be inspected")
	}
	input, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer input.Close()
	st, err := input.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("backup must be a regular file")
	}
	staging, err := os.MkdirTemp(a.home, ".restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	m, err := readBackup(input, staging, a)
	if err != nil {
		return err
	}
	disk := filepath.Join(staging, "disk.qcow2")
	if err = standaloneDisk(disk, m.Disk); err != nil {
		return err
	}
	if err = a.call(nil, a.err, "qemu-img", "check", "-f", "qcow2", disk); err != nil {
		return fmt.Errorf("restored disk check failed: %w", err)
	}
	public, err := a.capture(5*time.Second, "ssh-keygen", "-y", "-P", "", "-f", filepath.Join(staging, "keys/identity"))
	if err != nil {
		return fmt.Errorf("invalid backup client key: %w", err)
	}
	saved, err := os.ReadFile(filepath.Join(staging, "keys/identity.pub"))
	if err != nil {
		return err
	}
	fields, actual := strings.Fields(string(saved)), strings.Fields(string(public))
	if len(fields) < 2 || len(actual) < 2 || strings.ContainsAny(strings.TrimSpace(string(saved)), "\r\n") || fields[0] != "ssh-ed25519" || fields[0] != actual[0] || fields[1] != actual[1] {
		return errors.New("backup client keypair does not match")
	}
	e := &environment{Schema: 2, Name: name, GuestID: m.GuestID, Owner: a.uid, GID: a.gid, Project: projectDir, Desktop: *desktop, CPUs: m.CPUs, Memory: m.Memory, Disk: m.Disk, Digest: m.ImageSHA256, Initialized: m.Initialized}
	if err = a.allocateID(e); err != nil {
		return err
	}
	metadata, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(staging, "environment.json"), metadata, 0600); err != nil {
		return err
	}
	// Reserve the name exclusively, including against paths created outside
	// nsl while validation was running. Rename replaces only our empty directory.
	if err = os.Mkdir(a.dir(name), 0700); err != nil {
		return err
	}
	if err = syscall.Rename(staging, a.dir(name)); err != nil {
		_ = os.Remove(a.dir(name))
		return err
	} // manager lock protects name allocation
	if err = syncDir(filepath.Dir(a.dir(name))); err != nil {
		return err
	}
	l, err := a.lock(name)
	if err != nil {
		return err
	}
	defer unlock(l)
	if err = a.prepare(e); err != nil {
		return fmt.Errorf("restore verified but preparation incomplete; use nsl recover %s: %w", name, err)
	}
	fmt.Fprintf(a.out, "Restored %s; use nsl shell %s\n", name, name)
	return nil
}
