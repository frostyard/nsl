package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestDoctorChecksTheFirmwareLaunchSelects(t *testing.T) {
	const describe = `{"description":"UEFI firmware for x86_64, without Secure Boot","mapping":{"device":"flash","executable":{"filename":"/usr/share/OVMF/OVMF_CODE_4M.fd","format":"raw"}}}`
	const missing = "MISSING UEFI firmware (vmspawn found no x86_64 firmware without Secure Boot; install ovmf)\n"
	for _, c := range []struct {
		name      string
		vmspawn   bool
		firmware  string
		want, not string
	}{
		{"present", true, describe, "OK /usr/share/OVMF/OVMF_CODE_4M.fd\n", missing},
		{"missing", true, "", missing, "OK /usr/share/OVMF"},
		{"no vmspawn", false, describe, "MISSING systemd-vmspawn\n", "UEFI"}, // nothing to ask
	} {
		t.Run(c.name, func(t *testing.T) {
			a, f := testApp(t)
			var out bytes.Buffer
			a.out, f.firmware = &out, c.firmware
			dir := t.TempDir()
			if c.vmspawn {
				if err := os.WriteFile(filepath.Join(dir, "systemd-vmspawn"), nil, 0755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir)
			// The other host checks pass; only vmspawn answers from the fake.
			f.blocking = func(ctx context.Context, bin string, args []string) (bool, error) {
				return bin != "systemd-vmspawn", nil
			}
			// The test PATH lacks the other tools, so doctor fails in every case;
			// the firmware line is what differs.
			if err := a.doctor(); err == nil {
				t.Fatal("doctor passed without its tools")
			}
			if !strings.Contains(out.String(), c.want) || strings.Contains(out.String(), c.not) {
				t.Fatalf("doctor printed:\n%s", out.String())
			}
			describes := 0
			for _, call := range f.calls {
				if call.Bin == "systemd-vmspawn" {
					describes++
					if !slices.Equal(call.Args, []string{"--firmware=describe", "--secure-boot=no"}) {
						t.Fatalf("vmspawn ran with %q", call.Args)
					}
				}
			}
			if (describes == 1) != c.vmspawn {
				t.Fatalf("vmspawn described firmware %d times", describes)
			}
		})
	}
}
