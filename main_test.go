package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type fake struct {
	calls [][]string
	state machine
	fail  string
}

func (f *fake) run(capture bool, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{}, args...))
	if len(args) > 0 && args[0] == f.fail {
		return "", errors.New("failed")
	}
	if len(args) > 0 && args[0] == "inspect" {
		b, _ := json.Marshal([]machine{f.state})
		return string(b), nil
	}
	if len(args) > 0 && (args[0] == "images" || args[0] == "ps") {
		return "[]", nil
	}
	return "", nil
}
func testApp(f *fake) *app {
	return &app{r: f, uid: "1000", gid: "1000", username: "bjk", out: &bytes.Buffer{}}
}
func ownedFixture() machine {
	return machine{Name: "nsl-dev", State: "stopped", Mode: "boot", Origin: "create", Labels: map[string]string{label: "1000"}}
}
func TestNames(t *testing.T) {
	for _, bad := range []string{"", "-dev", "dev-", "Dev", "../dev", "dev:bad", strings.Repeat("a", 41)} {
		if _, err := machineName(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if n, err := machineName("dev-1"); err != nil || n != "nsl-dev-1" {
		t.Fatalf("%s %v", n, err)
	}
}
func TestOwnership(t *testing.T) {
	f := &fake{state: ownedFixture()}
	a := testApp(f)
	if _, err := a.owned("nsl-dev"); err != nil {
		t.Fatal(err)
	}
	f.state.Labels[label] = "2000"
	if _, err := a.owned("nsl-dev"); err == nil {
		t.Fatal("foreign machine accepted")
	}
	f.state.Labels[label] = "1000"
	f.state.Origin = "pull"
	if _, err := a.owned("nsl-dev"); err == nil {
		t.Fatal("image accepted")
	}
}
func TestStartMounts(t *testing.T) {
	f := &fake{}
	a := testApp(f)
	n := ownedFixture()
	if err := a.start(&n, "/tmp/p:/work"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls[0], []string{"start", "nsl-dev", "-v", "/tmp/p:/work"}) {
		t.Fatal(f.calls)
	}
	n.State = "running"
	n.Volumes = []string{"/tmp/p:/work"}
	if err := a.start(&n, "/tmp/p:/work"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatal("unexpected start")
	}
	if err := a.start(&n, "/tmp/other:/work"); err == nil {
		t.Fatal("switched running mount")
	}
}
func TestProjectRejectsRoot(t *testing.T) {
	if _, err := projectVolume("/"); err == nil {
		t.Fatal("mounted root")
	}
}
func TestRunArgsAndFailure(t *testing.T) {
	f := &fake{state: ownedFixture()}
	a := testApp(f)
	if err := a.execute([]string{"run", "dev", "--", "id"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "nsl-dev", "-u", "1000", "env", "--", "HOME=/home/bjk", "USER=bjk", "LOGNAME=bjk", "id"}
	if !reflect.DeepEqual(f.calls[len(f.calls)-1], want) {
		t.Fatalf("got %v want %v", f.calls[len(f.calls)-1], want)
	}
	if err := a.execute([]string{"run", "dev", "--root", "--", "id"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls[len(f.calls)-1], []string{"exec", "nsl-dev", "id"}) {
		t.Fatal(f.calls)
	}
	f.fail = "start"
	if err := a.execute([]string{"run", "dev", "--", "id"}); err == nil {
		t.Fatal("ignored start failure")
	}
}
func TestStopClearsMount(t *testing.T) {
	f := &fake{state: ownedFixture()}
	f.state.State = "running"
	f.state.Volumes = []string{"/tmp/p:/work"}
	a := testApp(f)
	if err := a.execute([]string{"stop", "dev"}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"inspect", "nsl-dev"}, {"stop", "nsl-dev"}, {"start", "nsl-dev", "-v", "none"}, {"stop", "nsl-dev"}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("got %v", f.calls)
	}
}
func TestWaylandRequiresSession(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if _, _, err := waylandVolume("bjk"); err == nil {
		t.Fatal("accepted no session")
	}
}
func TestInvalidInvocationDoesNotCallNspawn(t *testing.T) {
	for _, args := range [][]string{{"run", "dev"}, {"in", "dev", "/this/path/does/not/exist"}, {"gui", "dev"}, {"stop", "dev", "extra"}} {
		f := &fake{state: ownedFixture()}
		a := testApp(f)
		if err := a.execute(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
		if len(f.calls) != 0 {
			t.Fatalf("invalid %v called nspawn: %v", args, f.calls)
		}
	}
}
func TestMain(m *testing.M) { os.Exit(m.Run()) }
