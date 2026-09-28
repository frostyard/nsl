package main

import (
	"errors"
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
