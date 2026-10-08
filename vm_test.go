package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestGroupSwitcherPrefersSgAndFallsBackToNewgrp(t *testing.T) {
	has := func(tools ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			if slices.Contains(tools, name) {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		}
	}
	for _, c := range []struct {
		tools []string
		want  string
	}{
		{[]string{"sg", "newgrp"}, "sg"},
		{[]string{"sg"}, "sg"},
		{[]string{"newgrp"}, "newgrp"}, // util-linux's newgrp without an sg
		{nil, "sg"},                    // doctor reports the missing tool
	} {
		if got := groupSwitcher(has(c.tools...)); got != c.want {
			t.Errorf("with %v: got %q, want %q", c.tools, got, c.want)
		}
	}
}

func TestVMLaunchOpensTheDevicesThroughTheHostsGroupSwitch(t *testing.T) {
	for _, c := range []struct {
		switcher string
		want     func(script string) []string
	}{
		{"sg", func(s string) []string { return []string{"sg", "kvm", "-c", s} }},
		{"newgrp", func(s string) []string { return []string{"newgrp", "-c", s, "kvm"} }},
	} {
		t.Run(c.switcher, func(t *testing.T) {
			a, f := testApp(t)
			a.groupSwitch = c.switcher
			image, digest := localImage(t, "vm image")
			if err := a.execute([]string{"update", "--image", image, "--digest", digest}); err != nil {
				t.Fatal(err)
			}
			v, err := a.loadVM()
			if err != nil {
				t.Fatal(err)
			}
			f.vm = v
			if v, err = a.runningVM(true); err != nil {
				t.Fatal(err)
			}
			for _, call := range f.calls {
				if call.Bin != "systemd-run" || !slices.Contains(call.Args, "--unit="+vmUnit(v)) {
					continue
				}
				i := slices.Index(call.Args, "--")
				want := c.want("exec '/test/nsl' _devices " + v.ID)
				if i < 0 || !slices.Equal(call.Args[i+1:], want) {
					t.Fatalf("launch runs %q, want %q", strings.Join(call.Args[i+1:], " "), strings.Join(want, " "))
				}
				return
			}
			t.Fatal("the VM was not launched")
		})
	}
}

func TestInGroupRestoresThePrimaryGroupWithEitherTool(t *testing.T) {
	a := &app{groupSwitch: "newgrp"}
	if got := a.inGroup("u", "exec true"); !slices.Equal(got, []string{"newgrp", "-c", "exec true", "u"}) {
		t.Fatal(got)
	}
	a.groupSwitch = "sg"
	if got := a.inGroup("u", "exec true"); !slices.Equal(got, []string{"sg", "u", "-c", "exec true"}) {
		t.Fatal(got)
	}
}

func TestDoctorChecksSystemdAndTheFirmwareLaunchSelects(t *testing.T) {
	dir := t.TempDir()
	base := `{"description":"UEFI firmware","interface-types":["uefi"],"mapping":{"device":"flash","executable":{"filename":"/fw/NAME.fd","format":"raw"},"nvram-template":{"filename":"/fw/VARS.fd","format":"raw"}},"targets":[{"architecture":"x86_64","machines":["pc-i440fx-*","pc-q35-*"]}],"features":["acpi-s3"],"tags":[]}`
	// descriptor writes a firmware descriptor, replacing pairs of strings in base.
	descriptor := func(name string, replace ...string) string {
		body := strings.ReplaceAll(base, "NAME", name)
		for i := 0; i+1 < len(replace); i += 2 {
			body = strings.Replace(body, replace[i], replace[i+1], 1)
		}
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	list := func(paths ...string) string { return strings.Join(paths, "\n") + "\n" }
	enrolled := descriptor("enrolled", `"acpi-s3"`, `"acpi-s3","enrolled-keys","secure-boot"`)
	secure := descriptor("secure", `"acpi-s3"`, `"acpi-s3","secure-boot"`)
	unusable := list(enrolled, secure,
		descriptor("bios", `"uefi"`, `"bios"`),
		descriptor("arm", `"x86_64"`, `"aarch64"`),
		descriptor("i440fx", `,"pc-q35-*"`, ``),
		descriptor("stateless", `,"nvram-template":{"filename":"/fw/VARS.fd","format":"raw"}`, ``),
		descriptor("truncated", `"tags":[]}`, ``),
		filepath.Join(dir, "absent.json"))
	plain := descriptor("plain")
	const current = "systemd 262 (262-1)"
	const missing = "MISSING UEFI firmware (vmspawn lists no x86-64 UEFI firmware without Secure Boot; install ovmf)\n"
	for _, c := range []struct {
		name              string
		vmspawn           bool
		version, firmware string
		want, not         []string
	}{
		{"first launchable", true, current, unusable + list(plain, descriptor("later")), []string{"OK systemd 262\n", "OK /fw/plain.fd\n"}, []string{"UEFI", "later"}},
		{"secure boot only", true, current, list(enrolled, secure), []string{missing}, []string{"OK /fw"}},
		{"no firmware", true, current, "", []string{missing}, []string{"OK /fw"}},
		{"systemd 259", true, "systemd 259 (259.9-1.fc44)", list(plain), []string{"OK systemd 259\n", "OK /fw/plain.fd\n"}, nil},
		{"systemd 258", true, "systemd 258 (258.1-1.fc43)", list(plain), []string{"MISSING systemd 259 or newer (systemd-vmspawn is from systemd 258)\n", "OK /fw/plain.fd\n"}, []string{"OK systemd"}},
		{"release candidate", true, "systemd 262~rc3 (262~rc3-1.fc45)", list(plain), []string{"OK systemd 262\n"}, nil},
		{"unknown version", true, "vmspawn 1.0", list(plain), []string{`MISSING systemd 259 or newer (unexpected systemd-vmspawn version "vmspawn 1.0")`}, []string{"OK systemd"}},
		{"no vmspawn", false, current, list(plain), []string{"MISSING systemd-vmspawn\n"}, []string{"UEFI", "OK systemd", "OK /fw"}}, // nothing to ask
	} {
		t.Run(c.name, func(t *testing.T) {
			a, f := testApp(t)
			var out bytes.Buffer
			a.out, f.vmspawnVersion, f.firmware = &out, c.version, c.firmware
			bin := t.TempDir()
			if c.vmspawn {
				if err := os.WriteFile(filepath.Join(bin, "systemd-vmspawn"), nil, 0755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			// The other host checks pass; only vmspawn answers from the fake.
			f.blocking = func(ctx context.Context, bin string, args []string) (bool, error) {
				return bin != "systemd-vmspawn" && bin != "getent" && bin != "id", nil
			}
			// The test PATH lacks the other tools, so doctor fails in every case;
			// the systemd and firmware lines are what differ.
			if err := a.doctor(); err == nil {
				t.Fatal("doctor passed without its tools")
			}
			for _, want := range c.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("want %q in:\n%s", want, out.String())
				}
			}
			for _, not := range c.not {
				if strings.Contains(out.String(), not) {
					t.Fatalf("unexpected %q in:\n%s", not, out.String())
				}
			}
			var asked [][]string
			for _, call := range f.calls {
				if call.Bin == "systemd-vmspawn" {
					asked = append(asked, call.Args)
				}
			}
			if want := [][]string{{"--version"}, {"--firmware=list"}}; c.vmspawn != slices.EqualFunc(asked, want, slices.Equal) || (!c.vmspawn && len(asked) != 0) {
				t.Fatalf("vmspawn ran with %q", asked)
			}
		})
	}
}

func TestLaunchRefusesAnOldSystemdBeforeChangingState(t *testing.T) {
	a, f := testApp(t)
	image, digest := localImage(t, "vm image")
	if err := a.execute([]string{"update", "--image", image, "--digest", digest}); err != nil {
		t.Fatal(err)
	}
	v, err := a.loadVM()
	if err != nil {
		t.Fatal(err)
	}
	f.vmspawnVersion = "systemd 258 (258.1-1.fc43)"
	err = a.launchVM(v, "inactive", true)
	if err == nil || err.Error() != "missing VM prerequisite: systemd 259 or newer (systemd-vmspawn is from systemd 258)" {
		t.Fatal(err)
	}
	if f.ran("systemd-run") != 0 {
		t.Fatal("launched the VM")
	}
	if _, err := os.Lstat(filepath.Join(v.dir, "root.raw")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("made a root:", err)
	}
}

func TestRootCopySkipsTheImagesHolesAndZeros(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "image.raw")
	f, err := os.Create(image)
	if err != nil {
		t.Fatal(err)
	}
	// Data, 32 MiB of written zeros, a hole, data, and a trailing hole.
	f.WriteAt([]byte("first"), 0)
	f.WriteAt(make([]byte, 32<<20), 4096)
	f.WriteAt([]byte("second"), 64<<20+100)
	f.Truncate(128 << 20)
	f.Close()
	root := filepath.Join(dir, "root.raw")
	if err = copyRoot(image, root); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(image)
	got, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	copied := make([]byte, len(b))
	if _, err = io.ReadFull(got, copied); err != nil || !bytes.Equal(copied, b) {
		t.Fatal("the root differs from the image", err)
	}
	st, _ := got.Stat()
	if st.Size() != rootGiB*gib || st.Mode().Perm() != 0600 {
		t.Fatal(st.Size(), st.Mode())
	}
	if allocated := st.Sys().(*syscall.Stat_t).Blocks * 512; allocated > 16<<20 {
		t.Fatalf("the root allocates %d bytes; it copied the image's holes or zeros", allocated)
	}
	if err = copyRoot(image, root); !errors.Is(err, os.ErrExist) {
		t.Fatal("replaced an existing file:", err)
	}
}

func TestKVMGroupMembershipBeforeDoctorAndLaunch(t *testing.T) {
	for _, c := range []struct {
		name, group, groups, fail, want string
	}{
		{name: "supplementary member", group: "kvm:x:993:u\n", groups: "1000 993\n"},
		{name: "primary member", group: "kvm:x:993:\n", groups: "993 1000\n"},
		{name: "NSS member", group: "kvm:x:993:\n", groups: "1000 993\n"},
		{name: "non-member", group: "kvm:x:993:other\n", groups: "1000 1993\n", want: "kvm group membership (ask an administrator to run: sudo usermod -aG kvm 'u'; then retry nsl; no new login needed)"},
		{name: "group lookup failed", fail: "getent", want: "cannot look up kvm group: getent:"},
		{name: "account lookup failed", group: "kvm:x:993:u\n", fail: "id", want: "cannot look up kvm group membership for u: id:"},
		{name: "empty group", want: "invalid getent response"},
		{name: "wrong group", group: "other:x:993:u\n", want: "invalid getent response"},
		{name: "invalid group GID", group: "kvm:x:bad:u\n", want: "invalid GID"},
		{name: "empty groups", group: "kvm:x:993:u\n", want: "empty id response"},
		{name: "invalid account GID", group: "kvm:x:993:u\n", groups: "993 bad\n", want: "invalid GID"},
	} {
		for _, switcher := range []string{"sg", "newgrp"} {
			for _, command := range []string{"doctor", "launch"} {
				t.Run(c.name+"/"+switcher+"/"+command, func(t *testing.T) {
					a, f := testApp(t)
					a.groupSwitch = switcher
					var v *vmRecord
					if command == "launch" {
						image, digest := localImage(t, "vm image")
						if err := a.execute([]string{"update", "--image", image, "--digest", digest}); err != nil {
							t.Fatal(err)
						}
						var err error
						if v, err = a.loadVM(); err != nil {
							t.Fatal(err)
						}
					}
					f.calls = nil
					f.kvmGroup, f.accountGroups = c.group, c.groups
					f.blocking = func(ctx context.Context, bin string, args []string) (bool, error) {
						if bin == c.fail {
							return true, errors.New("lookup failed")
						}
						return bin == switcher || (bin == "systemctl" && slices.Contains(args, "show-environment")), nil
					}
					if command == "doctor" {
						var out bytes.Buffer
						a.out = &out
						t.Setenv("PATH", t.TempDir())
						if err := a.doctor(); err == nil {
							t.Fatal("doctor passed without its tools")
						}
						want := c.want
						if want == "" {
							want = "OK kvm group membership\n"
						} else if !strings.Contains(out.String(), "MISSING "+c.want) && !strings.Contains(out.String(), "MISSING cannot look up") {
							t.Fatalf("missing diagnostic:\n%s", out.String())
						}
						if !strings.Contains(out.String(), want) {
							t.Fatalf("want %q in:\n%s", want, out.String())
						}
					} else {
						err := a.launchVM(v, "failed", true)
						if c.want == "" && err != nil {
							t.Fatal(err)
						}
						if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
							t.Fatalf("got %v, want %q", err, c.want)
						}
					}
					invoked := false
					for _, call := range f.calls {
						switch call.Bin {
						case "getent":
							if !slices.Equal(call.Args, []string{"group", "kvm"}) {
								t.Fatalf("group lookup: %v", call)
							}
						case "id":
							if !slices.Equal(call.Args, []string{"-G", "--", a.user}) {
								t.Fatalf("must query the account, not session groups: %v", call)
							}
						case "sg", "newgrp", "systemd-run":
							invoked = true
						default:
							if command == "launch" && c.want != "" {
								t.Fatalf("launch changed state before confirming membership: %v", call)
							}
						}
					}
					if invoked != (c.want == "") {
						t.Fatalf("group switch/launch invoked = %v, want %v", invoked, c.want == "")
					}
				})
			}
		}
	}
}

func TestDoctorReportsDeviceAccessFailureForKVMMember(t *testing.T) {
	for _, switcher := range []string{"sg", "newgrp"} {
		t.Run(switcher, func(t *testing.T) {
			a, f := testApp(t)
			a.groupSwitch = switcher
			var out bytes.Buffer
			a.out = &out
			t.Setenv("PATH", t.TempDir())
			f.blocking = func(ctx context.Context, bin string, args []string) (bool, error) {
				if bin == switcher {
					return true, errors.New("device access denied")
				}
				return bin == "systemctl", nil
			}
			if err := a.doctor(); err == nil {
				t.Fatal("doctor passed without device access")
			}
			for _, want := range []string{"OK kvm group membership", "KVM/vsock group access: " + switcher + ": device access denied"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("want %q in:\n%s", want, out.String())
				}
			}
		})
	}
}
