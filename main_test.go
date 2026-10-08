package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

type call struct {
	Bin  string
	Args []string
}

// fakeRunner emulates systemd user units, vmspawn's version and firmware list, ssh-keygen and the agent.
type fakeRunner struct {
	mu                   sync.Mutex // helpers call the runner from several goroutines
	calls                []call
	blocking             func(ctx context.Context, bin string, args []string) (bool, error) // long-running tools, run unlocked
	forwardFail          map[string]bool                                                    // ssh -O forward specs that fail
	forwards             []string                                                           // ssh -O forward and cancel calls
	states, descriptions map[string]string
	vm                   *vmRecord // the VM whose identity the fake agent reports
	identity             func(*protocol.Identity)
	agent                func(req *protocol.Request, stdin io.Reader, stdout io.Writer) error
	requests             []*protocol.Request
	vmspawnVersion       string // vmspawn --version's first line
	firmware             string // vmspawn --firmware=list: descriptor paths, one per line
	kvmGroup             string // getent's group record
	accountGroups        string // id -G USER; independent of the session's groups
}

func (f *fakeRunner) run(ctx context.Context, in io.Reader, out, stderr io.Writer, env []string, bin string, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, call{bin, append([]string{}, args...)})
	blocking := f.blocking
	f.mu.Unlock()
	if blocking != nil {
		if handled, err := blocking(ctx, bin, args); handled {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch bin {
	case "getent":
		_, err := io.WriteString(out, f.kvmGroup)
		return err
	case "id":
		_, err := io.WriteString(out, f.accountGroups)
		return err
	case "systemd-vmspawn":
		// Like vmspawn, the list succeeds with no firmware.
		answer := f.firmware
		if args[0] == "--version" {
			answer = f.vmspawnVersion + "\n+PAM +AUDIT\n"
		}
		_, err := io.WriteString(out, answer)
		return err
	case "ssh-keygen":
		path := args[len(args)-1]
		if err := os.WriteFile(path, []byte("private-key"), 0600); err != nil {
			return err
		}
		return os.WriteFile(path+".pub", []byte("ssh-ed25519 AAAA nsl-vm\n"), 0644)
	case "systemctl":
		unit := args[2]
		switch args[1] {
		case "show":
			state, ok := f.states[unit]
			if !ok {
				_, err := io.WriteString(out, "LoadState=not-found\nActiveState=inactive\nDescription="+unit+"\n")
				return err
			}
			_, err := fmt.Fprintf(out, "LoadState=loaded\nActiveState=%s\nDescription=%s\n", state, f.descriptions[unit])
			return err
		case "stop":
			f.states[unit] = "inactive"
		case "reset-failed":
			f.states[unit] = "inactive"
		}
	case "systemd-run":
		var unit, description string
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--unit="); ok {
				unit = v
			}
			if v, ok := strings.CutPrefix(a, "--description="); ok {
				description = v
			}
		}
		f.states[unit], f.descriptions[unit] = "active", description
	case "ssh":
		for i, a := range args {
			if a == "-O" {
				if args[i+1] == "forward" || args[i+1] == "cancel" {
					f.forwards = append(f.forwards, args[i+1]+" "+args[i+3])
					if args[i+1] == "forward" && f.forwardFail[args[i+3]] {
						fmt.Fprintln(stderr, "mux_client_forward: forwarding request failed: Port forwarding failed")
						return errors.New("exit status 255")
					}
				}
				return nil
			}
			if a == "-fN" {
				return nil
			}
		}
		req, err := protocol.Decode(args[len(args)-1])
		if err != nil {
			return err
		}
		f.requests = append(f.requests, req)
		// The VM answering is the one whose SSH configuration the call uses.
		vm := f.vm
		for i, a := range args[:len(args)-1] {
			if a == "-F" {
				v := &vmRecord{dir: filepath.Dir(args[i+1])}
				if b, err := os.ReadFile(filepath.Join(v.dir, "vm.json")); err == nil && json.Unmarshal(b, v) == nil && (vm == nil || v.ID != vm.ID) {
					vm = v
				}
			}
		}
		// The hook answers with an error, or lets the default answer through.
		if f.agent != nil {
			if err := f.agent(req, in, out); err == errAnswered {
				return nil
			} else if err != nil {
				fmt.Fprintln(stderr, err.Error())
				return errors.New("exit status 255")
			}
		}
		switch req.Op {
		case "identity":
			id := protocol.Identity{Protocol: 1, VM: protocol.Binding{Version: 1, ID: vm.ID, Role: vm.Role, UID: vm.Owner, GID: vm.GID, Machine: vm.Machine},
				Image: json.RawMessage(`{"schema":1,"role":"vm","build_id":"nsl-vm-trixie-x86-64-r1","distribution":"debian","release":"trixie","architecture":"x86-64","revision":1,"agent_protocol":1,"machine_protocol":1,"transport":"nsl-vsock-ssh","systemd":"257","kernel":"6.12","integration_sha256":"x","recipes_revision":"r","mkosi_revision":"m"}`)}
			if f.identity != nil {
				f.identity(&id)
			}
			return json.NewEncoder(out).Encode(id)
		case "vm":
			if reflect.DeepEqual(req.Argv, []string{"systemctl", "poweroff"}) {
				f.states[vmUnit(vm)] = "inactive"
			}
		case "create", "import":
			_, err := io.WriteString(out, `{"build_id":"nsl-machine-debian-trixie-x86-64-r1"}`+"\n")
			return err
		case "start":
			_, err := io.WriteString(out, `{"state":"running","seconds":0.5}`+"\n")
			return err
		case "machines":
			_, err := io.WriteString(out, "[]\n")
			return err
		}
	}
	return nil
}

// ran counts calls; tests with concurrent helpers hold f.mu around it.
// errAnswered lets an agent hook answer a request completely.
var errAnswered = errors.New("answered")

func (f *fakeRunner) ran(bin string, prefix ...string) int {
	n := 0
	for _, c := range f.calls {
		if c.Bin == bin && len(c.Args) >= len(prefix) && (len(prefix) == 0 || reflect.DeepEqual(c.Args[:len(prefix)], prefix)) {
			n++
		}
	}
	return n
}

func testApp(t *testing.T) (*app, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{states: map[string]string{}, descriptions: map[string]string{}, kvmGroup: "kvm:x:993:u\n", accountGroups: "1000 993\n",
		vmspawnVersion: "systemd 262 (262-1)"}
	host := t.TempDir()
	t.Setenv("HOME", filepath.Join(host, "home", "u"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(host, "config"))
	t.Setenv("WAYLAND_DISPLAY", "") // desktop sessions only where a test asks
	os.MkdirAll(filepath.Join(host, "home", "u"), 0700)
	a := &app{home: filepath.Join(t.TempDir(), "nsl"), runtimeDir: t.TempDir(), self: "/test/nsl", waypipe: "waypipe", groupSwitch: "sg", uid: os.Getuid(), gid: os.Getgid(),
		user: "u", group: "u", r: f, in: strings.NewReader(""), out: io.Discard, err: io.Discard, host: &hostFacts{memoryKiB: 16 << 20, cpus: 8}, hostRoot: host}
	os.Chmod(a.runtimeDir, 0700)
	if a.uid == 0 {
		t.Skip("nsl requires a normal user")
	}
	// Unit tests never reach a registry: this one refuses every connection.
	// Delivery tests replace it with their fixture.
	a.imageService = &imageClient{app: a, verify: fixtureVerify, now: time.Now,
		registry: &imageRegistry{base: "http://127.0.0.1:0/v2", tokenURL: "http://127.0.0.1:0/token", client: &http.Client{}}}
	return a, f
}

// jsonDocument decodes a --json command's output, which must be exactly one
// object with no fields the test does not expect.
func jsonDocument(t *testing.T, out []byte, v any) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(out))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		t.Fatalf("%v in\n%s", err, out)
	}
	if d.More() {
		t.Fatalf("more than one document in\n%s", out)
	}
}

// vmsAndMachines is list's JSON document.
type vmsAndMachines struct {
	VMs []struct {
		Name        string   `json:"name"`
		Role        string   `json:"role"`
		State       string   `json:"state"`
		Image       string   `json:"image"`
		CPUs        int      `json:"cpus"`
		MemoryGiB   int      `json:"memory_gib"`
		DataDiskGiB int      `json:"data_disk_gib"`
		Pending     []string `json:"pending"`
	} `json:"vms"`
	Machines []struct {
		Name    string `json:"name"`
		State   string `json:"state"`
		Image   string `json:"image"`
		Tier    string `json:"tier"`
		Default bool   `json:"default"`
	} `json:"machines"`
}

// configDocument is config's JSON document.
type configDocument struct {
	Path     string `json:"path"`
	Present  bool   `json:"present"`
	Settings []struct {
		Key    string `json:"key"`
		Value  any    `json:"value"`
		Min    *int   `json:"min"`
		Max    *int   `json:"max"`
		Unit   string `json:"unit"`
		Source string `json:"source"`
		Line   int    `json:"line"`
		Reason string `json:"reason"`
	} `json:"settings"`
	Pending []struct {
		VM     string `json:"vm"`
		Change string `json:"change"`
	} `json:"pending"`
}

func localImage(t *testing.T, content string) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "vm.raw")
	os.WriteFile(p, []byte(content), 0600)
	h := sha256.Sum256([]byte(content))
	return p, "sha256:" + hex.EncodeToString(h[:])
}

// startedVM selects a local image and starts the VM.
func startedVM(t *testing.T) (*app, *fakeRunner, *vmRecord) {
	t.Helper()
	a, f := testApp(t)
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
	return a, f, v
}

func TestUpdateSelectsAnImageForTheNextStart(t *testing.T) {
	a, f, v := startedVM(t)
	if v.PendingImage != "" || v.Image == "" || v.ImageBuild != "nsl-vm-trixie-x86-64-r1" || !v.Initialized || v.CPUs != 8 || v.Memory != 8 {
		t.Fatalf("%+v", v)
	}
	cached := a.vmImagePath(v.Image)
	if st, err := os.Stat(cached); err != nil || st.Mode().Perm() != 0444 {
		t.Fatal(err)
	}
	if got := rootStart(t, v, "vm image"); got != "vm image" {
		t.Fatalf("the root starts with %q, not the image", got)
	}
	// A second image waits for the next start and never touches the running VM.
	image, digest := localImage(t, "newer vm image")
	if err := a.execute([]string{"update", "--image", image, "--digest", digest}); err != nil {
		t.Fatal(err)
	}
	if v, _ = a.loadVM(); v.PendingImage != strings.TrimPrefix(digest, "sha256:") || rootStart(t, v, "vm image") != "vm image" {
		t.Fatal("update changed a running VM")
	}
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"list"}); err != nil || !strings.Contains(out.String(), "Pending at the next start of the shared VM: VM image sha256:"+v.PendingImage[:12]) {
		t.Fatal(err, out.String())
	}
	if err := a.execute([]string{"shutdown"}); err != nil {
		t.Fatal(err)
	}
	f.vm = v
	if v, _ = a.runningVM(true); v.Image != strings.TrimPrefix(digest, "sha256:") || v.PendingImage != "" {
		t.Fatalf("%+v", v)
	}
	if got := rootStart(t, v, "newer vm image"); got != "newer vm image" {
		t.Fatalf("the root starts with %q, not the new image", got)
	}
}

// rootStart checks that a VM's root is a private file of the root's capacity
// and returns as many of its first bytes as want has.
func rootStart(t *testing.T, v *vmRecord, want string) string {
	t.Helper()
	root := filepath.Join(v.dir, "root.raw")
	if st, err := os.Stat(root); err != nil || st.Size() != rootGiB*gib || st.Mode().Perm() != 0600 {
		t.Fatal("root:", st, err)
	}
	return diskStart(t, root, len(want))
}

// diskStart reads a disk's first n bytes; disks are too large to read whole.
func diskStart(t *testing.T, path string, n int) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, n)
	if _, err = io.ReadFull(f, b); err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateRefusesBadImages(t *testing.T) {
	a, _ := testApp(t)
	image, _ := localImage(t, "vm image")
	for _, args := range [][]string{
		{"--image", image, "--digest", "sha256:" + strings.Repeat("0", 64)},
		{"--image", image, "--digest", "md5:00"},
		{"--image", image},
		{"--offline", "--image", image, "--digest", "sha256:" + strings.Repeat("0", 64)},
		{"--offline"}, // nothing cached, and offline never downloads
	} {
		if err := a.update(args); err == nil {
			t.Fatal("accepted", args)
		}
	}
	if v, err := a.loadVM(); v != nil || err != nil {
		t.Fatal("a refused update created a VM")
	}
	entries, _ := os.ReadDir(filepath.Join(a.home, "images", "vm"))
	if len(entries) != 0 {
		t.Fatal("cached a mismatched image")
	}
}

func TestStartNeedsAnImage(t *testing.T) {
	a, _ := testApp(t)
	if _, err := a.runningVM(true); err == nil || !strings.Contains(err.Error(), "nsl update") {
		t.Fatal(err)
	}
}

func TestLaunchArgumentsAndCredential(t *testing.T) {
	a, _, v := startedVM(t)
	os.MkdirAll(filepath.Join(a.hostRoot, "mnt"), 0755)
	os.MkdirAll(filepath.Join(a.hostRoot, "var"), 0755)
	os.Rename(filepath.Join(a.hostRoot, "home"), filepath.Join(a.hostRoot, "var", "home"))
	os.Symlink("var/home", filepath.Join(a.hostRoot, "home"))
	c, _ := a.loadConfig()
	c.autostart.value, c.idleTimeout.value = false, 0
	if err := a.writeCredential(v, c, true); err != nil {
		t.Fatal(err)
	}
	var cred protocol.Credential
	b, _ := os.ReadFile(filepath.Join(v.dir, "nsl.vm"))
	if err := protocol.DecodeStrict(b, &cred); err != nil {
		t.Fatal(err)
	}
	home, _ := filepath.EvalSymlinks(filepath.Join(a.hostRoot, "var", "home", "u"))
	mnt, _ := filepath.EvalSymlinks(filepath.Join(a.hostRoot, "mnt"))
	if cred.ID != v.ID || cred.Autostart || cred.IdleTimeout != 0 || cred.PublicKey != "ssh-ed25519 AAAA nsl-vm" ||
		!reflect.DeepEqual(cred.Shares, []protocol.Share{{Source: home}, {Source: mnt}}) ||
		!reflect.DeepEqual(cred.Aliases, []protocol.Alias{{Path: "/mnt/host/home", Target: "var/home"}}) {
		t.Fatalf("%+v", cred)
	}
	args := strings.Join(a.launchArgs(v, &cred), " ")
	for _, want := range []string{
		"--image=" + v.dir + "/root.raw", "--extra-drive=" + v.dir + "/data.raw",
		"--bind-ro=" + a.home + "/images/machines:/var/cache/nsl/images", "--bind=" + home + ":/mnt/host" + home,
		"--load-credential=nsl.vm:" + v.dir + "/nsl.vm", "--cpus=8", "--ram=8G", "--register=no", "--secure-boot=no",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %s in %s", want, args)
		}
	}
	for _, arg := range a.launchArgs(v, &cred) {
		if arg == "--user" || strings.HasPrefix(arg, "--image-format") || strings.HasPrefix(arg, "--firmware") {
			t.Fatalf("launch passes %s, which systemd 259's vmspawn rejects", arg)
		}
	}
}

func TestIsolatedHostFilesStayOutOfTheCacheShare(t *testing.T) {
	a, _ := testApp(t)
	if err := a.init(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(a.machineImages())
	if len(entries) != 0 {
		t.Fatal("the cache share holds more than machine images")
	}
}

func TestReadinessChecksIdentityAndProtocols(t *testing.T) {
	for name, change := range map[string]func(*protocol.Identity){
		"id":   func(id *protocol.Identity) { id.VM.ID = strings.Repeat("0", 32) },
		"uid":  func(id *protocol.Identity) { id.VM.UID++ },
		"role": func(id *protocol.Identity) { id.VM.Role = "isolated" },
		"agent protocol": func(id *protocol.Identity) {
			id.Image = bytes.Replace(id.Image, []byte(`"agent_protocol":1`), []byte(`"agent_protocol":2`), 1)
		},
		"machine protocol": func(id *protocol.Identity) {
			id.Image = bytes.Replace(id.Image, []byte(`"machine_protocol":1`), []byte(`"machine_protocol":2`), 1)
		},
		"architecture": func(id *protocol.Identity) {
			id.Image = bytes.Replace(id.Image, []byte(`"architecture":"x86-64"`), []byte(`"architecture":"arm64"`), 1)
		},
		"unknown field": func(id *protocol.Identity) { id.Image = bytes.Replace(id.Image, []byte(`{`), []byte(`{"extra":1,`), 1) },
	} {
		a, f := testApp(t)
		image, digest := localImage(t, "vm image")
		a.update([]string{"--image", image, "--digest", digest})
		f.vm, _ = a.loadVM()
		f.identity = change
		v, _ := a.loadVM()
		if name != "id" && name != "uid" && name != "role" {
			if _, err := a.ready(v); !errors.Is(err, errIncompatibleImage) {
				t.Fatalf("%s: %v", name, err)
			}
			continue
		}
		if _, err := a.ready(v); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestForeignUnitAndStateAreRefused(t *testing.T) {
	a, f, v := startedVM(t)
	f.descriptions[vmUnit(v)] = "someone else"
	if _, err := a.vmState(v); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(v.dir, "vm.json"), 0640)
	if _, err := a.loadVM(); err == nil {
		t.Fatal("accepted a group-readable record")
	}
	os.Chmod(filepath.Join(v.dir, "vm.json"), 0600)
	b, _ := os.ReadFile(filepath.Join(v.dir, "vm.json"))
	os.WriteFile(filepath.Join(v.dir, "vm.json"), bytes.Replace(b, []byte(`"role": "shared"`), []byte(`"role": "shared", "extra": 1`), 1), 0600)
	if _, err := a.loadVM(); err == nil {
		t.Fatal("accepted an unknown field")
	}
}

func TestLockRejectsAReplacedVM(t *testing.T) {
	a, _, v := startedVM(t)
	stale := *v
	stale.ID = strings.Repeat("0", 32)
	if _, _, err := a.lockVM(&stale); err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatal(err)
	}
}

func TestResizeGrowsTheStoppedDataDisk(t *testing.T) {
	a, f, v := startedVM(t)
	disk := filepath.Join(v.dir, "data.raw")
	if got, err := diskGiB(disk); err != nil || got != defaultDataGiB {
		t.Fatal(got, err)
	}
	if err := a.resize([]string{"--disk", "200"}); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatal(err)
	}
	a.shutdown()
	if err := a.resize([]string{"--disk", "64"}); err == nil || !strings.Contains(err.Error(), "shrinking") {
		t.Fatal(err)
	}
	// Growth that cannot write the disk fails after recording its target.
	os.Chmod(disk, 0400)
	if err := a.resize([]string{"--disk", "200"}); err == nil {
		t.Fatal("hid a failed resize")
	}
	if r, _ := a.loadVM(); r.ResizeTarget != 200 {
		t.Fatal("did not record the target before growing")
	}
	if got, _ := diskGiB(disk); got != defaultDataGiB {
		t.Fatal("a failed resize changed the disk to", got)
	}
	if _, err := a.runningVM(true); err == nil || !strings.Contains(err.Error(), "recover") {
		t.Fatal("started with pending growth:", err)
	}
	if err := a.resize([]string{"--disk", "300"}); err == nil || !strings.Contains(err.Error(), "pending growth to 200") {
		t.Fatal(err)
	}
	os.Chmod(disk, 0600)
	f.vm, _ = a.loadVM()
	if err := a.recover(nil); err != nil {
		t.Fatal(err)
	}
	v, _ = a.loadVM()
	if v.DataGiB != 200 || v.ResizeTarget != 0 {
		t.Fatalf("%+v", v)
	}
	if got, err := diskGiB(disk); err != nil || got != 200 {
		t.Fatal(got, err)
	}
	// Growth interrupted after extending the disk commits without extending it again.
	a.shutdown()
	v.ResizeTarget = 256
	if err := a.saveVM(v); err != nil {
		t.Fatal(err)
	}
	os.Truncate(disk, 256*gib)
	if err := a.resize([]string{"--disk", "256"}); err != nil {
		t.Fatal(err)
	}
	if v, _ = a.loadVM(); v.DataGiB != 256 || v.ResizeTarget != 0 {
		t.Fatalf("%+v", v)
	}
}

func TestRecoverRebuildsTheRootAndKeepsTheDataDisk(t *testing.T) {
	a, _, v := startedVM(t)
	root, data := filepath.Join(v.dir, "root.raw"), filepath.Join(v.dir, "data.raw")
	for _, p := range []string{root, data} {
		f, err := os.OpenFile(p, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteAt([]byte("used"), 0)
		f.Close()
	}
	if err := a.recover(nil); err != nil {
		t.Fatal(err)
	}
	if got := rootStart(t, v, "vm image"); got != "vm image" {
		t.Fatalf("root not rebuilt: it starts with %q", got)
	}
	if got := diskStart(t, data, 4); got != "used" {
		t.Fatal("data disk changed")
	}
	a.shutdown()
	os.Truncate(data, gib+1)
	if err := a.recover(nil); err == nil || !strings.Contains(err.Error(), "preserved") {
		t.Fatal(err)
	}
	if st, err := os.Stat(data); err != nil || st.Size() != gib+1 {
		t.Fatal("recover changed a disk that failed its check")
	}
}

func TestPendingRestartIsReported(t *testing.T) {
	a, _, _ := startedVM(t)
	path, _ := configPath()
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("[vm]\nmemory = 4\n"), 0600)
	var out bytes.Buffer
	a.out = &out
	if err := a.execute([]string{"config"}); err != nil || !strings.Contains(out.String(), "Pending at the next start of the shared VM: vm.memory 4 GiB (running with 8)") {
		t.Fatal(err, out.String())
	}
	out.Reset()
	if err := a.execute([]string{"list"}); err != nil || !strings.Contains(out.String(), "vm.memory 4 GiB") || !strings.Contains(out.String(), "running") {
		t.Fatal(err, out.String())
	}
	out.Reset()
	if err := a.execute([]string{"config", "--json"}); err != nil {
		t.Fatal(err)
	}
	var config configDocument
	jsonDocument(t, out.Bytes(), &config)
	if len(config.Pending) != 1 || config.Pending[0].VM != "shared" || config.Pending[0].Change != "vm.memory 4 GiB (running with 8)" {
		t.Fatalf("%+v", config.Pending)
	}
	out.Reset()
	if err := a.execute([]string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}
	var list vmsAndMachines
	jsonDocument(t, out.Bytes(), &list)
	if len(list.VMs) != 1 || list.VMs[0].State != "running" || list.VMs[0].CPUs != 8 || list.VMs[0].MemoryGiB != 8 ||
		len(list.VMs[0].Pending) != 1 || list.VMs[0].Pending[0] != "vm.memory 4 GiB (running with 8)" {
		t.Fatalf("%+v", list.VMs)
	}
}

func TestUsage(t *testing.T) {
	a, _ := testApp(t)
	for _, args := range [][]string{{"shell"}, {"list", "x"}, {"list", "--yaml"}, {"config", "--json", "x"}, {"shutdown", "now"}} {
		if err := a.execute(args); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
