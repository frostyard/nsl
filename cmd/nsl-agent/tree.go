package main

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/frostyard/nsl/internal/protocol"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/sys/unix"
)

// rootfsLimit bounds an uncompressed machine root filesystem.
const rootfsLimit int64 = 16 << 30

// tree writes inside a machine's root as the machine would see it: absolute
// symlinks and ".." resolve within the tree, never into the VM.
type tree struct{ fd int }

func openTree(dir string) (*tree, error) {
	fd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return &tree{fd}, nil
}

func (t *tree) close() { unix.Close(t.fd) }

func (t *tree) open(rel string, flags uint64, mode uint64) (int, error) {
	fd, err := unix.Openat2(t.fd, rel, &unix.OpenHow{Flags: flags | unix.O_CLOEXEC, Mode: mode,
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: "/" + rel, Err: err}
	}
	return fd, nil
}

func (t *tree) parent(rel string) (int, string, error) {
	dir, base := path.Split(rel)
	if dir == "" {
		dir = "."
	}
	fd, err := t.open(dir, unix.O_PATH|unix.O_DIRECTORY, 0)
	return fd, base, err
}

func (t *tree) readFile(rel string) ([]byte, error) {
	fd, err := t.open(rel, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// writeFile replaces rel atomically with a regular file.
func (t *tree) writeFile(rel string, data []byte, mode uint32) error {
	dir, base, err := t.parent(rel)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	temporary := ".nsl-" + randomHex(8)
	fd, err := unix.Openat(dir, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return &os.PathError{Op: "create", Path: "/" + rel, Err: err}
	}
	f := os.NewFile(uintptr(fd), rel)
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(os.FileMode(mode))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = unix.Renameat(dir, temporary, dir, base)
	}
	if err != nil {
		unix.Unlinkat(dir, temporary, 0)
		return &os.PathError{Op: "write", Path: "/" + rel, Err: err}
	}
	return nil
}

// symlink replaces rel with a symlink to target.
func (t *tree) symlink(target, rel string) error {
	dir, base, err := t.parent(rel)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	if err = unix.Unlinkat(dir, base, 0); err != nil && err != unix.ENOENT {
		return &os.PathError{Op: "unlink", Path: "/" + rel, Err: err}
	}
	if err = unix.Symlinkat(target, dir, base); err != nil {
		return &os.PathError{Op: "symlink", Path: "/" + rel, Err: err}
	}
	return nil
}

func (t *tree) mkdir(rel string, mode uint32) error {
	dir, base, err := t.parent(rel)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	if err = unix.Mkdirat(dir, base, mode); err != nil && err != unix.EEXIST {
		return &os.PathError{Op: "mkdir", Path: "/" + rel, Err: err}
	}
	return nil
}

func invalid(format string, args ...any) error {
	return &protocol.Error{Code: protocol.CodeFailed, Message: "invalid root filesystem: " + fmt.Sprintf(format, args...)}
}

// entryName normalizes a tar member name to a clean relative path, or refuses it.
func entryName(name string) (string, error) {
	if strings.HasPrefix(name, "/") || strings.IndexByte(name, 0) >= 0 {
		return "", invalid("absolute or NUL path %q", name)
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", invalid("path %q leaves the tree", name)
	}
	return clean, nil
}

// validateRootfs refuses an archive that could write outside its tree or that
// has more than one interpretation, before anything is extracted.
func validateRootfs(archive string, limit int64) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	dec, err := zstd.NewReader(f, zstd.WithDecoderMaxWindow(128<<20), zstd.WithDecoderMaxMemory(128<<20), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return err
	}
	defer dec.Close()
	stream := &io.LimitedReader{R: dec, N: limit + 1}
	r := tar.NewReader(stream)
	seen := map[string]bool{}
	files := map[string]bool{}
	links := map[string]bool{}
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalid("%v", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := entryName(h.Name)
		if err != nil {
			return err
		}
		if seen[name] {
			return invalid("duplicate entry %q", name)
		}
		seen[name] = true
		if strings.HasPrefix(path.Base(name), ".wh.") {
			return invalid("OCI whiteout %q", name)
		}
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if links[dir] {
				return invalid("%q is below the symlink %q", name, dir)
			}
		}
		switch h.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg, tar.TypeFifo:
			if name == "." {
				return invalid("the root is not a directory")
			}
			files[name] = true
		case tar.TypeSymlink:
			if name == "." || h.Linkname == "" || strings.IndexByte(h.Linkname, 0) >= 0 {
				return invalid("bad symlink %q", name)
			}
			links[name] = true
		case tar.TypeLink:
			target, err := entryName(h.Linkname)
			if err != nil {
				return err
			}
			if !files[target] {
				return invalid("hard link %q to %q, which is not an earlier file", name, h.Linkname)
			}
			files[name] = true
		default:
			return invalid("unsupported entry type %q for %q", h.Typeflag, name)
		}
	}
	// Anything after the end-of-archive marker must be tar's zero padding.
	var block [4096]byte
	for {
		n, err := stream.Read(block[:])
		for _, b := range block[:n] {
			if b != 0 {
				return invalid("trailing data after the archive")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalid("%v", err)
		}
	}
	if stream.N <= 0 {
		return invalid("larger than %d GiB uncompressed", limit>>30)
	}
	if !seen["."] && len(seen) == 0 {
		return errors.New("invalid root filesystem: empty archive")
	}
	return nil
}
