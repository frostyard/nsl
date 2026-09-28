package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

// standaloneDisk checks a qcow2 disk before QEMU opens it: no backing file,
// encryption, external data or snapshots, and the expected capacity.
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
		return errors.New("data disk must be qcow2 version 2 or 3")
	}
	if u64(8) != 0 || u32(16) != 0 || u32(32) != 0 || u32(60) != 0 {
		return errors.New("data disk must have no backing file, encryption or internal snapshots")
	}
	if u32(20) < 9 || u32(20) > 21 || u64(24) != uint64(int64(diskGiB)*gib) {
		return errors.New("data disk size or cluster geometry mismatch")
	}
	header := uint32(72)
	if u32(4) == 3 {
		// Only the compression and extended-L2 feature bits are allowed.
		if u64(72) & ^uint64(24) != 0 || u64(88)&2 != 0 {
			return errors.New("data disk is dirty, corrupt or uses unsupported/external data features")
		}
		header = u32(100)
	}
	cluster := uint32(1) << u32(20)
	if header < 72 || (u32(4) == 3 && header < 104) || header%8 != 0 || header > cluster-8 {
		return errors.New("invalid qcow2 header length")
	}
	data := make([]byte, cluster)
	if _, err = f.ReadAt(data, 0); err != nil && err != io.EOF {
		return err
	}
	for off := uint64(header); off+8 <= uint64(cluster); {
		kind := binary.BigEndian.Uint32(data[off : off+4])
		size := uint64(binary.BigEndian.Uint32(data[off+4 : off+8]))
		if kind == 0 {
			return nil
		}
		if kind == 0x44415441 || kind == 0xe2792aca || kind == 0x0537be77 {
			return errors.New("external disk reference or encryption extension in the data disk")
		}
		off += 8 + ((size + 7) &^ uint64(7))
		if off > uint64(cluster) {
			return errors.New("invalid qcow2 extension length")
		}
	}
	return errors.New("unterminated qcow2 extensions")
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
	if string(header[:4]) != "QFI\xfb" || size < uint64(gib) || size > uint64(4096*gib) || size%uint64(gib) != 0 {
		return 0, errors.New("invalid qcow2 capacity")
	}
	return int(size / uint64(gib)), nil
}

func (a *app) checkDataDisk(v *vmRecord) error {
	disk := filepath.Join(v.dir, "data.qcow2")
	if err := privateFile(disk, a.uid, 0077); err != nil {
		return err
	}
	if err := standaloneDisk(disk, v.DataGiB); err != nil {
		return err
	}
	if err := a.call(nil, a.err, "qemu-img", "check", "-q", "-f", "qcow2", disk); err != nil {
		return fmt.Errorf("data disk check failed; the disk is preserved for inspection: %w", err)
	}
	return nil
}

func (a *app) requireStopped(v *vmRecord) error {
	state, err := a.vmState(v)
	if err == nil && state != "stopped" && state != "failed" {
		err = errors.New("the VM is running; run nsl shutdown first")
	}
	return err
}

func (a *app) resize(args []string) error {
	fs := flag.NewFlagSet("resize", flag.ContinueOnError)
	fs.SetOutput(a.err)
	target := fs.Int("disk", 0, "new data disk capacity in GiB (growth only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *target < 1 || *target > 4096 {
		return errors.New("usage: resize --disk GiB (up to 4096)")
	}
	v, err := a.loadVM()
	if err != nil {
		return err
	}
	if v == nil {
		return errors.New("there is no nsl VM yet")
	}
	l, v, err := a.lockVM(v)
	if err != nil {
		return err
	}
	defer unlock(l)
	if err = a.requireStopped(v); err != nil {
		return err
	}
	if *target < v.DataGiB {
		return errors.New("shrinking the data disk is not supported")
	}
	if v.ResizeTarget != 0 && v.ResizeTarget != *target {
		return fmt.Errorf("finish pending growth to %d GiB first", v.ResizeTarget)
	}
	if v.ResizeTarget == 0 {
		if err = a.checkDataDisk(v); err != nil {
			return err
		}
		if *target == v.DataGiB {
			fmt.Fprintf(a.out, "The data disk already has %d GiB\n", v.DataGiB)
			return nil
		}
		// Record the intent first, so an interruption resumes rather than guesses.
		v.ResizeTarget = *target
		if err = a.saveVM(v); err != nil {
			return err
		}
	}
	if err = a.finishGrowth(v); err != nil {
		return fmt.Errorf("growth pending; retry resize or run nsl recover: %w", err)
	}
	fmt.Fprintf(a.out, "Grew the data disk to %d GiB; the VM grows its filesystem when it starts\n", v.DataGiB)
	return nil
}

func (a *app) finishGrowth(v *vmRecord) error {
	if err := a.requireStopped(v); err != nil {
		return err
	}
	disk := filepath.Join(v.dir, "data.qcow2")
	if err := privateFile(disk, a.uid, 0077); err != nil {
		return err
	}
	current, err := diskGiB(disk)
	if err != nil {
		return err
	}
	if current != v.DataGiB && current != v.ResizeTarget {
		return errors.New("data disk size differs from both committed and pending capacity; inspect before continuing")
	}
	if err = standaloneDisk(disk, current); err != nil {
		return err
	}
	if err = a.call(nil, a.err, "qemu-img", "check", "-q", "-f", "qcow2", disk); err != nil {
		return err
	}
	if current != v.ResizeTarget {
		if err = a.call(nil, a.err, "qemu-img", "resize", "-q", "-f", "qcow2", disk, fmt.Sprintf("%dG", v.ResizeTarget)); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(disk, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = standaloneDisk(disk, v.ResizeTarget); err != nil {
		return err
	}
	v.DataGiB, v.ResizeTarget = v.ResizeTarget, 0
	return a.saveVM(v)
}

// update selects the VM image for each VM's next start: the catalogue's, or a
// local one.
func (a *app) update(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(a.err)
	image := fs.String("image", "", "local VM image (raw)")
	digest := fs.String("digest", "", "sha256:HEX of the local image")
	offline := fs.Bool("offline", false, "use only verified cached data")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*image == "") != (*digest == "") || (*image != "" && *offline) {
		return errors.New("usage: update [--offline] | update --image FILE --digest sha256:HEX")
	}
	if *image == "" {
		cached, err := a.imageClient().pullVM(*offline)
		if err != nil {
			return err
		}
		return a.selectVMImage(strings.TrimPrefix(cached.ref.Digest, "sha256:"), cached.buildID)
	}
	if !digestPattern.MatchString(*digest) {
		return errors.New("--digest must be sha256: followed by 64 lowercase hex digits")
	}
	hexDigest := strings.TrimPrefix(*digest, "sha256:")
	if err := a.init(); err != nil {
		return err
	}
	if err := a.importFile(*image, a.vmImagePath(hexDigest), hexDigest); err != nil {
		return err
	}
	return a.selectVMImage(hexDigest, *digest)
}

// selectVMImage makes a cached VM image replace the VM's root at its next
// start; it never touches a running VM.
func (a *app) selectVMImage(hexDigest, label string) error {
	v, err := a.ensureVM()
	if err != nil {
		return err
	}
	l, v, err := a.lockVM(v)
	if err != nil {
		return err
	}
	defer unlock(l)
	if v.Image == hexDigest {
		v.PendingImage = ""
		fmt.Fprintln(a.out, "That VM image is already in effect")
	} else {
		v.PendingImage = hexDigest
		fmt.Fprintln(a.out, "Selected VM image "+label+"; it replaces the VM's root at its next start")
	}
	return a.saveVM(v)
}

// ensureVMImage selects the catalogue's VM image when no VM image is selected.
func (a *app) ensureVMImage(offline bool) error {
	v, err := a.loadVM()
	if err != nil || (v != nil && (v.Image != "" || v.PendingImage != "")) {
		return err
	}
	cached, err := a.imageClient().pullVM(offline)
	if err != nil {
		return err
	}
	return a.selectVMImage(strings.TrimPrefix(cached.ref.Digest, "sha256:"), cached.buildID)
}

// importFile copies a local file into the cache under its verified digest,
// without replacing an existing entry.
func (a *app) importFile(source, destination, digest string) error {
	if _, err := os.Lstat(destination); err == nil {
		// Already cached: verify the cached bytes, never the new source.
		if err = privateFile(destination, a.uid, 0222); err != nil {
			return err
		}
		source = destination
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if st, err := input.Stat(); err != nil || !st.Mode().IsRegular() {
		return errors.New("the image must be a regular file")
	}
	var f *os.File
	hash := sha256.New()
	target := io.Writer(hash)
	if source != destination {
		if f, err = os.CreateTemp(filepath.Dir(destination), ".import-*"); err != nil {
			return err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		target = io.MultiWriter(f, hash)
	}
	if _, err = io.Copy(target, input); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("image SHA256 mismatch")
	}
	if f == nil {
		return nil
	}
	if err = f.Chmod(0444); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	if err == nil {
		// Link rather than rename: a concurrent import of the same digest wins once.
		if err = os.Link(f.Name(), destination); errors.Is(err, os.ErrExist) {
			err = nil
		}
	}
	return err
}

// pending describes changes that apply at the VM's next start.
func (a *app) pending(v *vmRecord, c *config, running bool) []string {
	var out []string
	if v == nil {
		return nil
	}
	if running && v.Memory != 0 && c.vmMemory.value != v.Memory {
		out = append(out, fmt.Sprintf("vm.memory %d GiB (running with %d)", c.vmMemory.value, v.Memory))
	}
	if running && v.CPUs != 0 && c.vmCPUs.value != v.CPUs {
		out = append(out, fmt.Sprintf("vm.cpus %d (running with %d)", c.vmCPUs.value, v.CPUs))
	}
	if v.PendingImage != "" {
		out = append(out, "VM image sha256:"+v.PendingImage[:12])
	}
	if v.ResizeTarget != 0 {
		out = append(out, fmt.Sprintf("data disk growth to %d GiB (run nsl recover)", v.ResizeTarget))
	}
	return out
}

func (a *app) list() error {
	v, err := a.loadVM()
	if err != nil {
		return err
	}
	if v == nil {
		fmt.Fprintln(a.out, "No nsl VM yet")
		return nil
	}
	c, err := a.loadConfig()
	if err != nil {
		return err
	}
	state, err := a.vmState(v)
	if err != nil {
		return err
	}
	image := v.ImageBuild
	if image == "" && v.Image != "" {
		image = "sha256:" + v.Image[:12]
	}
	if image == "" {
		image = "-"
	}
	resources := "-"
	if state == "running" {
		resources = fmt.Sprintf("%d CPUs, %d GiB", v.CPUs, v.Memory)
	}
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VM\tSTATE\tIMAGE\tRESOURCES\tDATA DISK")
	fmt.Fprintf(w, "shared\t%s\t%s\t%s\t%d GiB\n", state, image, resources, v.DataGiB)
	if err = w.Flush(); err != nil {
		return err
	}
	for _, p := range a.pending(v, c, state == "running") {
		fmt.Fprintln(a.out, "Pending at the next VM start:", p)
	}
	fmt.Fprintln(a.out)
	return a.listMachines(a.out, v)
}
