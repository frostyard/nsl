package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemovePreviewAndExplicitDeletion(t *testing.T) {
	a, _, e, archive := backupFixture(t)
	if err := a.export(e.Name, archive); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(e.Project, "keep")
	if err := os.WriteFile(marker, []byte("project data"), 0600); err != nil {
		t.Fatal(err)
	}
	// A link in owned state must not make removal traverse host data.
	if err := os.Symlink(e.Project, filepath.Join(a.dir(e.Name), "external-link")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	a.out = &output
	if err := a.execute([]string{"remove", e.Name}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "--yes") {
		t.Fatal("missing preview")
	}
	if _, err := a.owned(e.Name); err != nil {
		t.Fatal("preview changed state", err)
	}
	if err := a.execute([]string{"remove", e.Name, "--yes"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{a.dir(e.Name), a.removing(e.Name), filepath.Dir(a.socket(e))} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("not removed: %s %v", path, err)
		}
	}
	for _, path := range []string{marker, archive, a.imagePath(e)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("removed retained file %s: %v", path, err)
		}
	}
	if err := a.restore(e.Name, []string{archive}); err != nil {
		t.Fatal(err)
	}
	replacement, err := a.owned(e.Name)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == e.ID {
		t.Fatal("reused runtime ID")
	}
	for _, op := range []struct {
		name string
		call func(*environment) error
	}{{"start", a.start}, {"stop", a.stop}, {"recover", a.recover}} {
		if err := op.call(e); err == nil {
			t.Fatalf("stale %s affected replacement", op.name)
		}
	}
}
func TestRemoveRejectsRunningForeignAndNestedProject(t *testing.T) {
	for _, kind := range []string{"running", "forwarder", "foreign", "nested"} {
		t.Run(kind, func(t *testing.T) {
			a, f, e, _ := backupFixture(t)
			switch kind {
			case "running":
				running(f, e)
			case "forwarder":
				f.states[portUnit(e)] = "active"
				f.descriptions[portUnit(e)] = description(e)
			case "foreign":
				f.states[unit(e)] = "inactive"
				f.descriptions[unit(e)] = "another app"
			case "nested":
				e.Project = a.dir(e.Name)
				if err := a.save(e); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.remove(e.Name, []string{"--yes"}); err == nil {
				t.Fatal("unsafe removal allowed")
			}
			if _, err := os.Stat(filepath.Join(a.dir(e.Name), "disk.qcow2")); err != nil {
				t.Fatal("touched disk", err)
			}
		})
	}
}
func TestRemoveResumesAndBlocksNameReuse(t *testing.T) {
	a, _, e, archive := backupFixture(t)
	if err := a.export(e.Name, archive); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(a.dir(e.Name), a.removing(e.Name)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(a.removing(e.Name), "disk.qcow2")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	a.out = &output
	if err := a.list(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Removing") {
		t.Fatal("pending removal hidden")
	}
	if err := a.create(e.Name, imageArgs(t)); err == nil {
		t.Fatal("create reused name during removal")
	}
	if err := a.restore(e.Name, []string{archive}); err == nil {
		t.Fatal("restore reused name during removal")
	}
	if err := a.remove(e.Name, []string{"--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.removing(e.Name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.remove(e.Name, []string{"--yes"}); err != nil {
		t.Fatal("could not finish empty tombstone", err)
	}
	if err := os.Mkdir(a.removing(e.Name), 0700); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(a.removing(e.Name), "unknown")
	if err := os.WriteFile(unknown, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.remove(e.Name, []string{"--yes"}); err == nil {
		t.Fatal("removed unidentified tombstone")
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal(err)
	}
}
func TestResizeGrowsPreservesKeysAndSupportsBackups(t *testing.T) {
	a, _, e, archive := backupFixture(t)
	key, _ := os.ReadFile(filepath.Join(a.dir(e.Name), "keys/identity"))
	if err := a.execute([]string{"resize", e.Name, "--disk", "24"}); err != nil {
		t.Fatal(err)
	}
	grown, err := a.owned(e.Name)
	if err != nil {
		t.Fatal(err)
	}
	if grown.Disk != 24 || grown.ResizeTarget != 0 || grown.ID != e.ID {
		t.Fatalf("wrong metadata %+v", grown)
	}
	actual, err := diskGiB(filepath.Join(a.dir(e.Name), "disk.qcow2"))
	if err != nil || actual != 24 {
		t.Fatalf("disk not grown %d %v", actual, err)
	}
	after, _ := os.ReadFile(filepath.Join(a.dir(e.Name), "keys/identity"))
	if !bytes.Equal(key, after) {
		t.Fatal("changed key")
	}
	if err := a.resize(e.Name, []string{"--disk", "24"}); err != nil {
		t.Fatal("idempotent resize", err)
	}
	if err := a.export(e.Name, archive); err != nil {
		t.Fatal("cannot back up grown disk", err)
	}
	b, _ := testApp(t)
	if err := b.restore("grown", []string{archive}); err != nil {
		t.Fatal("cannot restore grown disk", err)
	}
}
func TestResizeRefusesShrinkRunningAndInvalidSize(t *testing.T) {
	for _, kind := range []string{"shrink", "running", "limit", "mismatch", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			a, f, e, _ := backupFixture(t)
			args := []string{"--disk", "24"}
			switch kind {
			case "shrink":
				args = []string{"--disk", "8"}
			case "running":
				running(f, e)
			case "limit":
				args = []string{"--disk", "4097"}
			case "foreign":
				f.states[unit(e)] = "inactive"
				f.descriptions[unit(e)] = "foreign"
			case "mismatch":
				disk := filepath.Join(a.dir(e.Name), "disk.qcow2")
				data, _ := os.ReadFile(disk)
				binary.BigEndian.PutUint64(data[24:], 20*uint64(gib))
				if err := os.WriteFile(disk, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.resize(e.Name, args); err == nil {
				t.Fatal("unsafe resize allowed")
			}
			current, err := a.owned(e.Name)
			if err != nil {
				t.Fatal(err)
			}
			if current.Disk != 16 || current.ResizeTarget != 0 {
				t.Fatal("changed metadata before validation")
			}
		})
	}
}
func TestResizeIntentResumesBeforeAndAfterDiskChange(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[changed], func(t *testing.T) {
			a, f, e, archive := backupFixture(t)
			f.failResize = true
			if err := a.resize(e.Name, []string{"--disk", "24"}); err == nil {
				t.Fatal("ignored resize failure")
			}
			pending, err := a.owned(e.Name)
			if err != nil {
				t.Fatal(err)
			}
			if pending.Disk != 16 || pending.ResizeTarget != 24 {
				t.Fatal("did not retain intent")
			}
			if err := a.start(pending); err == nil {
				t.Fatal("started pending disk")
			}
			if err := a.export(e.Name, archive); err == nil {
				t.Fatal("exported pending disk")
			}
			if err := a.resize(e.Name, []string{"--disk", "32"}); err == nil {
				t.Fatal("changed pending target")
			}
			if changed {
				disk := filepath.Join(a.dir(e.Name), "disk.qcow2")
				data, _ := os.ReadFile(disk)
				binary.BigEndian.PutUint64(data[24:], 24*uint64(gib))
				if err := os.WriteFile(disk, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.failResize = false
			if changed {
				if err := a.recover(pending); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := a.resize(e.Name, []string{"--disk", "24"}); err != nil {
					t.Fatal(err)
				}
			}
			ready, err := a.owned(e.Name)
			if err != nil {
				t.Fatal(err)
			}
			if ready.Disk != 24 || ready.ResizeTarget != 0 {
				t.Fatal("growth did not commit")
			}
		})
	}
}
