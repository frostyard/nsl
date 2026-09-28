package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

// A machine archive (ADR-0006) is a tar of manifest.json and rootfs.tar.zst.
// Export reserves manifestSpace for the manifest, streams the root filesystem
// once, and fills in both headers when its digest and size are known.
const (
	archiveFormat = 1
	manifestSpace = 4096
	manifestLimit = 64 << 10
	block         = 512
)

type archiveManifest struct {
	Format       int              `json:"format"`
	Architecture string           `json:"architecture"`
	Machine      string           `json:"machine"`
	Account      protocol.Account `json:"account"`
	BuildID      string           `json:"build_id"`
	Created      string           `json:"created"`
	Rootfs       archiveFile      `json:"rootfs"`
}

type archiveFile struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func (m *archiveManifest) validate(uid, gid int) error {
	if m.Format != archiveFormat {
		return fmt.Errorf("unsupported archive format %d; this nsl reads format %d", m.Format, archiveFormat)
	}
	if m.Architecture != "x86-64" {
		return errors.New("the archive is not an x86-64 machine")
	}
	if m.Account.UID != uid || m.Account.GID != gid {
		return fmt.Errorf("the archive's account has UID %d and GID %d; yours are %d and %d", m.Account.UID, m.Account.GID, uid, gid)
	}
	if _, err := time.Parse(time.RFC3339, m.Created); err != nil || !protocol.ValidName(m.Machine) || !protocol.ValidAccount(m.Account.User) ||
		!protocol.ValidAccount(m.Account.Group) || !protocol.ValidUID(uid) || !protocol.ValidUID(gid) || !protocol.ValidBuildID(m.BuildID) ||
		!protocol.ValidDigest(m.Rootfs.Digest) || m.Rootfs.Size < 1 || m.Rootfs.Size > protocol.ArchiveLimit {
		return errors.New("invalid archive manifest")
	}
	return nil
}

// tarHeader is one 512-byte GNU header, whose base-256 sizes need no extension.
func tarHeader(name string, size int64, modified time.Time) ([]byte, error) {
	var b bytes.Buffer
	err := tar.NewWriter(&b).WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: size,
		ModTime: modified.Truncate(time.Second), Format: tar.FormatGNU})
	if err == nil && b.Len() != block {
		err = errors.New("unexpected archive header length")
	}
	return b.Bytes(), err
}

type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func (a *app) export(args []string) error {
	if len(args) != 2 || !protocol.ValidName(args[0]) {
		return errors.New("usage: export NAME FILE")
	}
	destination, err := filepath.Abs(args[1])
	if err != nil {
		return err
	}
	if _, err = os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s exists; nsl never replaces a file", destination)
	}
	m, err := a.machine(args[0])
	if err != nil {
		return err
	}
	if err = a.requirePrepared(m); err != nil {
		return err
	}
	l, m, err := a.lockMachine(m)
	if err != nil {
		return err
	}
	defer unlock(l)
	v, err := a.machineVM(m, false)
	if err != nil {
		return err
	}
	// Staged next to the destination, so publishing is a hard link.
	f, err := os.CreateTemp(filepath.Dir(destination), ".nsl-export-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	offset := int64(block + manifestSpace + block)
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	digest, size := sha256.New(), &counter{}
	err = a.agentStream(v, protocol.Request{Op: "export", Machine: m.Name, ID: m.ID}, nil, io.MultiWriter(f, digest, size), 24*time.Hour)
	var agentErr *protocol.Error
	if errors.As(err, &agentErr) && agentErr.Code == protocol.CodeBusy {
		return fmt.Errorf("stop %s first: %w", m.Name, err)
	}
	if err != nil {
		return err
	}
	if size.n == 0 {
		return errors.New("the VM sent an empty root filesystem")
	}
	// Pad the entry, then end the archive with two zero blocks.
	if _, err = f.Write(make([]byte, (block-size.n%block)%block+2*block)); err != nil {
		return err
	}
	now := time.Now().UTC()
	manifest := archiveManifest{Format: archiveFormat, Architecture: "x86-64", Machine: m.Name,
		Account: protocol.Account{User: m.User, Group: m.Group, UID: m.UID, GID: m.GID}, BuildID: m.BuildID,
		Created: now.Format(time.RFC3339), Rootfs: archiveFile{Digest: "sha256:" + hex.EncodeToString(digest.Sum(nil)), Size: size.n}}
	if err = manifest.validate(a.uid, a.gid); err != nil {
		return err
	}
	if err = writeHeaders(f, manifest, now); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// A hard link publishes atomically and never replaces a path created meanwhile.
	if err = os.Link(f.Name(), destination); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(destination)); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Exported %s to %s (%.1f MB). The archive is unencrypted and can contain credentials.\n", m.Name, destination, float64(size.n)/1e6)
	return nil
}

func writeHeaders(f *os.File, manifest archiveManifest, modified time.Time) error {
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if len(content) > manifestSpace {
		return errors.New("archive manifest too large")
	}
	// The manifest's reserved space ends in whitespace, which JSON ignores.
	content = append(content, bytes.Repeat([]byte{' '}, manifestSpace-len(content))...)
	header, err := tarHeader("manifest.json", manifestSpace, modified)
	if err != nil {
		return err
	}
	rootfs, err := tarHeader("rootfs.tar.zst", manifest.Rootfs.Size, modified)
	if err != nil {
		return err
	}
	_, err = f.WriteAt(append(append(header, content...), rootfs...), 0)
	return err
}

// openArchive reads an archive's manifest and positions a reader at the start
// of its root filesystem.
func openArchive(path string, uid, gid int) (*os.File, *tar.Reader, *archiveManifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	fail := func(err error) (*os.File, *tar.Reader, *archiveManifest, error) {
		f.Close()
		return nil, nil, nil, err
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return fail(errors.New("the archive must be a regular file"))
	}
	r := tar.NewReader(f)
	h, err := r.Next()
	if err != nil {
		return fail(fmt.Errorf("not an nsl machine archive: %w", err))
	}
	if h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size < 1 || h.Size > manifestLimit || len(h.PAXRecords) != 0 || h.Linkname != "" {
		return fail(errors.New("not an nsl machine archive: it must start with manifest.json"))
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return fail(err)
	}
	var m archiveManifest
	if err = protocol.DecodeStrict(content, &m); err != nil {
		return fail(fmt.Errorf("archive manifest: %w", err))
	}
	if err = m.validate(uid, gid); err != nil {
		return fail(err)
	}
	h, err = r.Next()
	if err != nil {
		return fail(err)
	}
	if h.Name != "rootfs.tar.zst" || h.Typeflag != tar.TypeReg || h.Size != m.Rootfs.Size || len(h.PAXRecords) != 0 || h.Linkname != "" {
		return fail(errors.New("the archive's second entry must be rootfs.tar.zst of the manifest's size"))
	}
	return f, r, &m, nil
}

// checkArchive verifies the root filesystem's checksum and that nothing but
// zero padding follows it.
func checkArchive(path string, uid, gid int) (*archiveManifest, error) {
	f, r, m, err := openArchive(path, uid, gid)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	digest := sha256.New()
	if n, err := io.Copy(digest, r); err != nil || n != m.Rootfs.Size {
		return nil, fmt.Errorf("the archive is truncated: %v", err)
	}
	if "sha256:"+hex.EncodeToString(digest.Sum(nil)) != m.Rootfs.Digest {
		return nil, errors.New("the archive's root filesystem does not match its checksum; the archive is damaged")
	}
	if _, err = r.Next(); err != io.EOF {
		return nil, errors.New("unexpected entries after rootfs.tar.zst")
	}
	rest, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(rest) > 1<<20 || len(bytes.Trim(rest, "\x00")) != 0 {
		return nil, errors.New("trailing data after the archive")
	}
	return m, nil
}

func (a *app) importArchive(args []string) error {
	if len(args) < 2 || !protocol.ValidName(args[0]) {
		return errors.New("usage: import NAME FILE [--isolated]")
	}
	name, path := args[0], args[1]
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(a.err)
	isolated := fs.Bool("isolated", false, "run the machine in its own VM without host access")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	switch {
	case fs.NArg() != 0:
		return errors.New("usage: import NAME FILE [--isolated]")
	case runtime.GOARCH != "amd64":
		return errors.New("machines are x86-64 only")
	}
	if err := a.initMachines(); err != nil {
		return err
	}
	manifest, err := checkArchive(path, a.uid, a.gid)
	if err != nil {
		return err
	}
	f, rootfs, _, err := openArchive(path, a.uid, a.gid)
	if err != nil {
		return err
	}
	defer f.Close()
	// The trust tier comes from the flags, never from the archive.
	m := &machineRecord{Schema: 1, Name: name, ID: randomID(), Tier: tier(*isolated), User: manifest.Account.User, Group: manifest.Account.Group,
		UID: a.uid, GID: a.gid, Created: time.Now().UTC().Format(time.RFC3339)}
	// The agent verifies the digest again as it receives the bytes.
	req := protocol.Request{Op: "import", Rootfs: &protocol.Rootfs{Digest: manifest.Rootfs.Digest, Size: manifest.Rootfs.Size, BuildID: manifest.BuildID}}
	if err = a.newMachine(m, req, rootfs, 24*time.Hour, false); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Imported %s from %s (%s, exported as %s)\n", name, path, m.BuildID, manifest.Machine)
	return nil
}
