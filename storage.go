package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
)

// diskGiB returns a raw disk's capacity in whole GiB.
func diskGiB(path string) (int, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if size := st.Size(); st.Mode().IsRegular() && size >= gib && size <= 4096*gib && size%gib == 0 {
		return int(size / gib), nil
	}
	return 0, errors.New("invalid data disk capacity")
}

// checkDataDisk checks the data disk before QEMU opens it: a private regular
// file with the committed capacity. vmspawn attaches it as raw, so nothing on
// the disk can make QEMU open another file.
func (a *app) checkDataDisk(v *vmRecord) error {
	disk := filepath.Join(v.dir, "data.raw")
	if err := privateFile(disk, a.uid, 0077); err != nil {
		return err
	}
	size, err := diskGiB(disk)
	if err == nil && size != v.DataGiB {
		err = fmt.Errorf("it has %d GiB, not %d", size, v.DataGiB)
	}
	if err != nil {
		return fmt.Errorf("data disk check failed; the disk is preserved for inspection: %w", err)
	}
	return nil
}

func (a *app) requireStopped(v *vmRecord) error {
	state, err := a.vmState(v)
	if err == nil && state != "stopped" && state != "failed" {
		err = fmt.Errorf("the %s VM is running; run nsl shutdown first", v.label())
	}
	return err
}

func (a *app) resize(args []string) error {
	usage := errors.New("usage: resize [NAME] --disk GiB (up to 4096)")
	var name []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[:1], args[1:]
	}
	fs := flag.NewFlagSet("resize", flag.ContinueOnError)
	fs.SetOutput(a.err)
	target := fs.Int("disk", 0, "new data disk capacity in GiB (growth only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *target < 1 || *target > 4096 {
		return usage
	}
	v, err := a.namedVM(name)
	if err != nil {
		return err
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
	disk := filepath.Join(v.dir, "data.raw")
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
	f, err := os.OpenFile(disk, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	// Extending a raw disk only appends a hole; the VM grows its filesystem.
	if current != v.ResizeTarget {
		err = f.Truncate(int64(v.ResizeTarget) * gib)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if current, err = diskGiB(disk); err == nil && current != v.ResizeTarget {
		err = errors.New("the data disk did not reach its pending capacity")
	}
	if err != nil {
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

// selectVMImage makes a cached VM image replace every VM's root at its next
// start; it never touches a running VM.
func (a *app) selectVMImage(hexDigest, label string) error {
	if _, err := a.ensureVM(); err != nil {
		return err
	}
	all, err := a.allVMs()
	if err != nil {
		return err
	}
	for _, v := range all {
		l, v, err := a.lockVM(v)
		if err != nil {
			return err
		}
		if v.Image == hexDigest {
			v.PendingImage = ""
		} else {
			v.PendingImage = hexDigest
		}
		err = a.saveVM(v)
		unlock(l)
		if err != nil {
			return err
		}
	}
	fmt.Fprintln(a.out, "Selected VM image "+label+"; it replaces each VM's root at its next start")
	return nil
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
	cpus, memory := c.resources(v)
	section := "vm"
	if v.Role == "isolated" {
		section = "isolated"
	}
	if running && v.Memory != 0 && memory != v.Memory {
		out = append(out, fmt.Sprintf("%s.memory %d GiB (running with %d)", section, memory, v.Memory))
	}
	if running && v.CPUs != 0 && cpus != v.CPUs {
		out = append(out, fmt.Sprintf("%s.cpus %d (running with %d)", section, cpus, v.CPUs))
	}
	if v.PendingImage != "" {
		out = append(out, "VM image sha256:"+v.PendingImage[:12])
	}
	if v.ResizeTarget != 0 {
		out = append(out, fmt.Sprintf("data disk growth to %d GiB (run nsl recover)", v.ResizeTarget))
	}
	return out
}

// vmListing is one VM in list, as the table and as JSON.
type vmListing struct {
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	State       string   `json:"state"`
	Image       string   `json:"image"`
	CPUs        int      `json:"cpus,omitempty"`
	MemoryGiB   int      `json:"memory_gib,omitempty"`
	DataDiskGiB int      `json:"data_disk_gib"`
	Pending     []string `json:"pending"`
}

// imageName is an image's build ID when known, otherwise its digest.
func imageName(build, digest string) string {
	switch {
	case build != "":
		return build
	case digest != "":
		return "sha256:" + digest
	}
	return ""
}

// shortImage abbreviates a digest image name for tables.
func shortImage(image string) string {
	if strings.HasPrefix(image, "sha256:") && len(image) > len("sha256:")+12 {
		return image[:len("sha256:")+12]
	}
	return image
}

func (a *app) list(asJSON bool) error {
	all, err := a.allVMs()
	if err != nil {
		return err
	}
	if len(all) == 0 && !asJSON {
		fmt.Fprintln(a.out, "No nsl VM yet")
		return nil
	}
	vms := []vmListing{}
	if len(all) != 0 {
		c, err := a.loadConfig()
		if err != nil {
			return err
		}
		for _, v := range all {
			state, err := a.vmState(v)
			if err != nil {
				return err
			}
			l := vmListing{Name: v.label(), Role: v.Role, State: state, Image: imageName(v.ImageBuild, v.Image),
				DataDiskGiB: v.DataGiB, Pending: a.pending(v, c, state == "running")}
			if state == "running" {
				l.CPUs, l.MemoryGiB = v.CPUs, v.Memory
			}
			if l.Pending == nil {
				l.Pending = []string{}
			}
			vms = append(vms, l)
		}
	}
	// Like the table, report no machines without a VM, and leave a fresh
	// state directory untouched.
	machines := []machineListing{}
	if len(all) != 0 {
		if machines, err = a.machineListings(all); err != nil {
			return err
		}
	}
	if asJSON {
		return writeJSON(a.out, struct {
			VMs      []vmListing      `json:"vms"`
			Machines []machineListing `json:"machines"`
		}{vms, machines})
	}
	var pending []string
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VM\tSTATE\tIMAGE\tRESOURCES\tDATA DISK")
	for _, l := range vms {
		image := shortImage(l.Image)
		if image == "" {
			image = "-"
		}
		resources := "-"
		if l.State == "running" {
			resources = fmt.Sprintf("%d CPUs, %d GiB", l.CPUs, l.MemoryGiB)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d GiB\n", l.Name, l.State, image, resources, l.DataDiskGiB)
		for _, p := range l.Pending {
			pending = append(pending, fmt.Sprintf("Pending at the next start of the %s VM: %s", l.Name, p))
		}
	}
	if err = w.Flush(); err != nil {
		return err
	}
	for _, p := range pending {
		fmt.Fprintln(a.out, p)
	}
	fmt.Fprintln(a.out)
	return printMachines(a.out, machines)
}
