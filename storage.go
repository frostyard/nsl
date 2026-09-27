package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (a *app) removing(name string) string { return filepath.Join(a.home, "removing", name) }
func (a *app) nameAvailable(name string) error {
	for _, path := range []string{a.dir(name), a.removing(name)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errors.New("environment exists or removal is incomplete; use recover or remove to finish it")
		}
	}
	return nil
}
func (a *app) requireStopped(e *environment) error {
	for _, name := range []string{unit(e), portUnit(e)} {
		state, err := a.unitState(e, name)
		if err != nil {
			return err
		}
		if state != "inactive" && state != "failed" {
			return fmt.Errorf("stop %s before changing storage", e.Name)
		}
	}
	return nil
}
func (a *app) remove(name string, args []string) error {
	if err := checkName(name); err != nil {
		return err
	}
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(a.err)
	yes := fs.Bool("yes", false, "permanently remove this stopped environment")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: remove NAME [--yes]")
	}
	if err := a.init(); err != nil {
		return err
	}
	manager, err := fileLock(filepath.Join(a.home, "lock"))
	if err != nil {
		return err
	}
	defer unlock(manager)
	removedRoot := filepath.Join(a.home, "removing")
	if err = os.MkdirAll(removedRoot, 0700); err != nil {
		return err
	}
	if err = checkPrivateDir(removedRoot, a.uid); err != nil {
		return err
	}
	dir := a.dir(name)
	pending := false
	if _, err = os.Lstat(a.removing(name)); err == nil {
		if _, err = os.Lstat(dir); !os.IsNotExist(err) {
			return errors.New("both active and removing state exist; inspect before deletion")
		}
		dir = a.removing(name)
		pending = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = checkPrivateDir(dir, a.uid); err != nil {
		return err
	}
	// Only an empty tombstone may outlive its final metadata unlink. Never
	// recursively remove unidentified content after an interrupted deletion.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if pending && len(entries) == 0 {
		if !*yes {
			fmt.Fprintf(a.out, "Would finish removing %s; use --yes\n", name)
			return nil
		}
		if err = os.Remove(dir); err != nil {
			return err
		}
		if err = syncDir(removedRoot); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "Removed %s\n", name)
		return nil
	}
	e, err := a.ownedAt(name, dir)
	if err != nil {
		return err
	}
	l, err := fileLock(filepath.Join(dir, "lock"))
	if err != nil {
		return err
	}
	defer unlock(l)
	checked, err := a.ownedAt(name, dir)
	if err != nil {
		return err
	}
	if checked.ID != e.ID {
		return errors.New("environment identity changed before removal")
	}
	e = checked
	if err = a.requireStopped(e); err != nil {
		return err
	}
	if err = a.protectProjects(a.dir(name)); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s: %s\n", name, dir)
	if !*yes {
		fmt.Fprintln(a.out, "Would permanently delete the guest disk, keys and configuration. Host projects, image cache and external backups remain. Use --yes to remove.")
		return nil
	}
	if !pending {
		// Close any surviving owned command master before moving its configuration.
		if err = a.stopLocked(e); err != nil {
			return err
		}
		if err = os.Rename(dir, a.removing(name)); err != nil {
			return err
		}
		if err = syncDir(filepath.Dir(dir)); err != nil {
			return err
		}
		if err = syncDir(removedRoot); err != nil {
			return err
		}
		dir = a.removing(name)
	}
	runtime := filepath.Dir(a.socket(e))
	if _, err = os.Lstat(runtime); err == nil {
		if err = checkPrivateDir(runtime, a.uid); err != nil {
			return err
		}
		if err = os.RemoveAll(runtime); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = purgeEnvironment(dir); err != nil {
		return fmt.Errorf("removal incomplete; retry nsl remove %s --yes: %w", name, err)
	}
	if err = syncDir(removedRoot); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Removed %s\n", name)
	return nil
}
func (a *app) protectProjects(dir string) error {
	entries, err := os.ReadDir(filepath.Join(a.home, "environments"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		e, err := a.owned(entry.Name())
		if err != nil {
			return err
		}
		if e.Project == "" {
			continue
		}
		rel, err := filepath.Rel(dir, e.Project)
		if err != nil {
			return err
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return fmt.Errorf("%s shares a project inside this environment's state; move the project before removal", e.Name)
		}
	}
	return nil
}
func purgeEnvironment(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "environment.json" {
			continue
		}
		// RemoveAll unlinks symlinks instead of traversing their targets.
		if err = os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if err = syncDir(dir); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(dir, "environment.json")); err != nil {
		return err
	}
	if err = syncDir(dir); err != nil {
		return err
	}
	return os.Remove(dir)
}
func (a *app) listRemoving() error {
	root := filepath.Join(a.home, "removing")
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return nil
	}
	if err := checkPrivateDir(root, a.uid); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = checkName(entry.Name()); err != nil {
			return err
		}
		dir := a.removing(entry.Name())
		if err = checkPrivateDir(dir, a.uid); err != nil {
			return err
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(files) != 0 {
			if _, err = a.ownedAt(entry.Name(), dir); err != nil {
				return err
			}
		}
		fmt.Fprintf(a.out, "%s\tRemoving\t\n", entry.Name())
	}
	return nil
}

func diskGiB(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var header [32]byte
	if _, err = io.ReadFull(f, header[:]); err != nil {
		return 0, err
	}
	size := binary.BigEndian.Uint64(header[24:])
	if string(header[:4]) != "QFI\xfb" || size < uint64(4*gib) || size > uint64(4096*gib) || size%uint64(gib) != 0 {
		return 0, errors.New("invalid qcow2 capacity")
	}
	return int(size / uint64(gib)), nil
}
func (a *app) resize(name string, args []string) error {
	fs := flag.NewFlagSet("resize", flag.ContinueOnError)
	fs.SetOutput(a.err)
	target := fs.Int("disk", 0, "new virtual capacity in GiB (growth only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *target < 4 || *target > 4096 {
		return errors.New("usage: resize NAME --disk GiB (4–4096)")
	}
	e, err := a.owned(name)
	if err != nil {
		return err
	}
	l, e, err := a.lockOwned(e)
	if err != nil {
		return err
	}
	defer unlock(l)
	if !e.Prepared {
		return errors.New("recover incomplete preparation before resizing")
	}
	if err = a.requireStopped(e); err != nil {
		return err
	}
	if *target < e.Disk {
		return errors.New("disk shrinking is not supported")
	}
	if e.ResizeTarget != 0 && e.ResizeTarget != *target {
		return fmt.Errorf("finish pending growth to %d GiB first", e.ResizeTarget)
	}
	if e.ResizeTarget == 0 {
		disk := filepath.Join(a.dir(name), "disk.qcow2")
		if err = privateFile(disk, a.uid, 0077); err != nil {
			return err
		}
		if err = standaloneDisk(disk, e.Disk); err != nil {
			return err
		}
		if *target == e.Disk {
			fmt.Fprintf(a.out, "%s already has a %d GiB virtual disk\n", name, e.Disk)
			return nil
		}
		e.ResizeTarget = *target
		if err = a.save(e); err != nil {
			return err
		}
	}
	if err = a.finishGrowth(e); err != nil {
		return fmt.Errorf("growth pending; retry resize or run nsl recover %s: %w", name, err)
	}
	fmt.Fprintf(a.out, "Grew %s to %d GiB. Start the VM to grow its root filesystem.\n", name, e.Disk)
	return nil
}
func (a *app) finishGrowth(e *environment) error {
	if err := a.requireStopped(e); err != nil {
		return err
	}
	disk := filepath.Join(a.dir(e.Name), "disk.qcow2")
	if err := privateFile(disk, a.uid, 0077); err != nil {
		return err
	}
	current, err := diskGiB(disk)
	if err != nil {
		return err
	}
	if current != e.Disk && current != e.ResizeTarget {
		return errors.New("disk size differs from both committed and pending capacity; inspect before continuing")
	}
	if err = standaloneDisk(disk, current); err != nil {
		return err
	}
	if err = a.call(nil, a.err, "qemu-img", "check", "-f", "qcow2", disk); err != nil {
		return err
	}
	if current != e.ResizeTarget {
		if err = a.call(nil, a.err, "qemu-img", "resize", "-f", "qcow2", disk, fmt.Sprintf("%dG", e.ResizeTarget)); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(disk, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = standaloneDisk(disk, e.ResizeTarget); err != nil {
		return err
	}
	if err = a.call(nil, a.err, "qemu-img", "check", "-f", "qcow2", disk); err != nil {
		return err
	}
	e.Disk = e.ResizeTarget
	e.ResizeTarget = 0
	return a.save(e)
}
