package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/frostyard/nsl/internal/protocol"
)

const lsblkBlank = `{"blockdevices":[
 {"name":"/dev/vda","type":"disk","ro":false,"fstype":null,"label":null,"children":[{"name":"/dev/vda1","type":"part","ro":false,"fstype":"vfat","label":null},{"name":"/dev/vda2","type":"part","ro":false,"fstype":"btrfs","label":"root"}]},
 {"name":"/dev/vdb","type":"disk","ro":false,"fstype":null,"label":null},
 {"name":"/dev/sr0","type":"rom","ro":true,"fstype":null,"label":null}]}`

func storageAgent(t *testing.T, lsblk string, blkid error) *testAgent {
	ta := newTestAgent(t, "shared")
	ta.r.handler = func(argv []string, stdin io.Reader, stdout io.Writer) error {
		switch argv[0] {
		case "findmnt":
			io.WriteString(stdout, "/dev/vda2\n")
		case "lsblk":
			if argv[1] == "--json" {
				io.WriteString(stdout, lsblk)
			} else {
				io.WriteString(stdout, "/dev/vda\n")
			}
		case "blkid":
			return blkid
		}
		return nil
	}
	return ta
}

func TestStorageFormatsOnlyABlankDisk(t *testing.T) {
	ta := storageAgent(t, lsblkBlank, exitError(2))
	if err := ta.storage(); err != nil {
		t.Fatal(err)
	}
	mount := ta.root + runtimeDir + "/format"
	if ta.r.ran("mkfs.btrfs", "--label", "nsl-data", "/dev/vdb") != 1 || ta.r.ran("btrfs", "subvolume", "create", mount+"/machines") != 1 ||
		ta.r.ran("btrfs", "subvolume", "create", mount+"/state") != 1 || ta.r.ran("umount", mount) != 1 {
		t.Fatal(ta.r.calls)
	}
	ta = storageAgent(t, lsblkBlank, nil)
	if err := ta.storage(); err == nil || !strings.Contains(err.Error(), "refusing to format /dev/vdb") || ta.r.ran("mkfs.btrfs") != 0 {
		t.Fatal(err, ta.r.calls)
	}
	ta = storageAgent(t, lsblkBlank, exitError(4))
	if err := ta.storage(); err == nil || ta.r.ran("mkfs.btrfs") != 0 {
		t.Fatal("formatted after a failed probe")
	}
	for name, tc := range map[string]struct{ lsblk, err string }{
		"prepared":     {strings.Replace(lsblkBlank, `"/dev/vdb","type":"disk","ro":false,"fstype":null,"label":null`, `"/dev/vdb","type":"disk","ro":false,"fstype":"btrfs","label":"nsl-data"`, 1), ""},
		"ext4 labeled": {strings.Replace(lsblkBlank, `"/dev/vdb","type":"disk","ro":false,"fstype":null,"label":null`, `"/dev/vdb","type":"disk","ro":false,"fstype":"ext4","label":"nsl-data"`, 1), "non-btrfs"},
		"two disks":    {strings.Replace(lsblkBlank, `{"name":"/dev/sr0"`, `{"name":"/dev/vdc","type":"disk","ro":false,"fstype":null,"label":null},{"name":"/dev/sr0"`, 1), "found 2"},
		"none":         {strings.Replace(lsblkBlank, `"/dev/vdb","type":"disk","ro":false`, `"/dev/vdb","type":"disk","ro":true`, 1), "found 0"},
	} {
		ta = storageAgent(t, tc.lsblk, exitError(2))
		err := ta.storage()
		if (tc.err == "" && err != nil) || (tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err))) || ta.r.ran("mkfs.btrfs") != 0 {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func credential(t *testing.T, ta *testAgent, c protocol.Credential) {
	t.Helper()
	dir := t.TempDir()
	b, _ := json.Marshal(c)
	os.WriteFile(filepath.Join(dir, "nsl.vm"), b, 0600)
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
}

func vmCredential() protocol.Credential {
	return protocol.Credential{Binding: protocol.Binding{Version: 1, ID: strings.Repeat("cd", 16), Role: "shared", UID: 1000, GID: 1000},
		PublicKey: "ssh-ed25519 AAAA nsl", Autostart: true, IdleTimeout: 15,
		Shares: []protocol.Share{{Source: "/var/home/u"}}, Aliases: []protocol.Alias{{Path: "/mnt/host/home", Target: "var/home"}}}
}

func setupAgent(t *testing.T) *testAgent {
	ta := newTestAgent(t, "shared")
	os.Remove(ta.root + stateDir + "/identity.json")
	ta.r.handler = func(argv []string, stdin io.Reader, stdout io.Writer) error {
		if argv[0] == "ssh-keygen" {
			key := argv[len(argv)-1]
			os.WriteFile(key, []byte("private"), 0600)
			return os.WriteFile(key+".pub", []byte("ssh-ed25519 HOST nsl-vm\n"), 0644)
		}
		return nil
	}
	return ta
}

func TestSetupBindsOnFirstBootAndChecksLater(t *testing.T) {
	ta := setupAgent(t)
	c := vmCredential()
	credential(t, ta, c)
	os.MkdirAll(ta.root+"/mnt/host/stale", 0755)
	os.Symlink("old", ta.root+"/mnt/host/old")
	if err := ta.setup(); err != nil {
		t.Fatal(err)
	}
	var recorded protocol.Binding
	b, _ := os.ReadFile(ta.root + stateDir + "/identity.json")
	if err := protocol.DecodeStrict(b, &recorded); err != nil || !reflect.DeepEqual(recorded, c.Binding) {
		t.Fatal(err, string(b))
	}
	keys, _ := os.ReadFile(ta.root + runtimeDir + "/ssh/authorized_keys")
	if string(keys) != `restrict,pty,port-forwarding,permitopen="127.0.0.1:*",permitopen="[::1]:*",command="/usr/lib/nsl/nsl-agent" ssh-ed25519 AAAA nsl`+"\n" {
		t.Fatalf("%q", keys)
	}
	if link, _ := os.Readlink(ta.root + "/mnt/host/home"); link != "var/home" {
		t.Fatal(link)
	}
	if _, err := os.Lstat(ta.root + "/mnt/host/old"); !os.IsNotExist(err) {
		t.Fatal("stale alias kept")
	}
	if _, err := os.Stat(ta.root + "/mnt/host/stale"); err != nil {
		t.Fatal("removed a directory")
	}
	if ta.r.ran("ssh-keygen") != 1 {
		t.Fatal(ta.r.calls)
	}
	// A later boot keeps the key and accepts the same binding.
	if err := ta.setup(); err != nil || ta.r.ran("ssh-keygen") != 1 {
		t.Fatal(err, ta.r.calls)
	}
	for name, change := range map[string]func(*protocol.Credential){
		"uid": func(c *protocol.Credential) { c.UID = 1001 },
		"id":  func(c *protocol.Credential) { c.ID = strings.Repeat("ef", 16) },
		"role": func(c *protocol.Credential) {
			c.Role, c.Machine, c.Shares, c.Aliases = "isolated", &protocol.MachineRef{Name: "m", ID: machineID}, nil, nil
		},
	} {
		other := vmCredential()
		change(&other)
		credential(t, ta, other)
		if err := ta.setup(); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	b, _ = os.ReadFile(ta.root + stateDir + "/identity.json")
	if err := protocol.DecodeStrict(b, &recorded); err != nil || !reflect.DeepEqual(recorded, c.Binding) {
		t.Fatal("binding changed:", string(b))
	}
}

func TestSetupRefusesBadCredentials(t *testing.T) {
	ta := setupAgent(t)
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if err := ta.setup(); err == nil {
		t.Fatal("ran without a credential")
	}
	c := vmCredential()
	c.Aliases = []protocol.Alias{{Path: "/mnt/host/home", Target: "/etc"}}
	credential(t, ta, c)
	if err := ta.setup(); err == nil {
		t.Fatal("accepted an absolute alias")
	}
	c = vmCredential()
	os.MkdirAll(ta.root+"/mnt/host/home", 0755)
	credential(t, ta, c)
	if err := ta.setup(); err == nil || !strings.Contains(err.Error(), "would replace a directory") {
		t.Fatal(err)
	}
}

func TestBootWritesSettingsAndAutostarts(t *testing.T) {
	for _, autostart := range []bool{true, false} {
		ta := setupAgent(t)
		c := vmCredential()
		c.Autostart = autostart
		credential(t, ta, c)
		if err := ta.setup(); err != nil {
			t.Fatal(err)
		}
		ta.addMachine(t, "debian", machineID)
		ta.addMachine(t, "fedora", strings.Repeat("1", 32))
		if err := ta.boot(); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"debian", "fedora"} {
			if b, err := os.ReadFile(ta.root + settingsDir + "/" + name + ".nspawn"); err != nil || string(b) != nspawnSettings("shared") {
				t.Fatal(err)
			}
		}
		if started := ta.r.ran("systemctl", "start", "--no-block"); (started == 2) != autostart {
			t.Fatal(autostart, ta.r.calls)
		}
	}
}
