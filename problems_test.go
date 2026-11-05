package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
	"github.com/frostyard/std/problems"
)

// The problem-record tests write at most 5 records in this process; the
// library caps a process at 10 records a minute. The panic and crash tests
// write theirs in child processes.

// TestMain runs a child process of the panic and crash tests instead of the
// tests when NSL_TEST_PROBLEMS_CHILD names one, so that run is the first
// function of the main goroutine, as in main.
func TestMain(m *testing.M) {
	if mode := os.Getenv("NSL_TEST_PROBLEMS_CHILD"); mode != "" {
		os.Exit(problemsChild(mode, os.Getenv("NSL_TEST_PROBLEMS_SOCKET"), os.Getenv("NSL_TEST_PROBLEMS_DIR")))
	}
	os.Exit(m.Run())
}

// journal is a unixgram socket that stands in for the journal's.
type journal struct {
	path string
	conn *net.UnixConn
}

func listenJournal(t *testing.T) *journal {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal")
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &journal{path, c}
}

// next returns the decoded fields of the next datagram.
func (j *journal) next(t *testing.T) map[string]string {
	t.Helper()
	if err := j.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<20)
	n, err := j.conn.Read(buf)
	if err != nil {
		t.Fatal("no problem record:", err)
	}
	fields, err := problems.DecodeEntry(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

// none fails when the socket holds a datagram. A record is sent before the
// call that writes it returns, so a short wait is enough.
func (j *journal) none(t *testing.T) {
	t.Helper()
	if err := j.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1<<20)
	n, err := j.conn.Read(buf)
	var ne net.Error
	if err == nil {
		t.Fatalf("unexpected problem record %q", buf[:n])
	} else if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatal(err)
	}
}

func testRecorder(j *journal) *problems.Recorder {
	return problems.New(appID, "1.2.3", problems.ComponentCLI, problems.WithSocket(j.path))
}

// expectRecord checks the identity fields of a record and returns it.
func expectRecord(t *testing.T, j *journal, class, priority string) map[string]string {
	t.Helper()
	r := j.next(t)
	for k, v := range map[string]string{"MESSAGE_ID": problems.MessageID, "PRIORITY": priority, "FROSTYARD_APP_ID": "org.frostyard.nsl",
		"FROSTYARD_APP_VERSION": "1.2.3", "FROSTYARD_RUNTIME": "go", "FROSTYARD_COMPONENT": "cli", "FROSTYARD_CLASS": class} {
		if r[k] != v {
			t.Fatalf("%s = %q, want %q in %v", k, r[k], v, r)
		}
	}
	return r
}

func expectError(t *testing.T, j *journal, code string, err error) {
	t.Helper()
	r := expectRecord(t, j, "error", "3")
	if r["FROSTYARD_CODE"] != code || r["MESSAGE"] != err.Error() {
		t.Fatalf("record %v, want code %s and message %q", r, code, err)
	}
	j.none(t)
}

func writePrivate(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

// selectedVM selects a local image, which writes the VM record, and returns it.
func selectedVM(t *testing.T) (*app, *fakeRunner, *vmRecord) {
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
	return a, f, v
}

func TestProblemRecordInvalidVMRecord(t *testing.T) {
	a, _, v := selectedVM(t)
	writePrivate(t, filepath.Join(v.dir, "vm.json"), "{\n")
	_, want := a.loadVM()
	if want == nil || !strings.HasPrefix(want.Error(), "VM record: ") {
		t.Fatal(want)
	}
	j := listenJournal(t)
	a.problems = testRecorder(j)
	if _, err := a.loadVM(); err == nil || err.Error() != want.Error() {
		t.Fatalf("error %v, want %v", err, want)
	}
	expectError(t, j, "state.vm_record_invalid", want)
}

func TestProblemRecordInvalidMachineRecord(t *testing.T) {
	a, _ := testApp(t)
	if err := a.initMachines(); err != nil {
		t.Fatal(err)
	}
	j := listenJournal(t)
	for content, want := range map[string]string{
		"not json":                     "machine record debian: ",
		`{"schema":2,"name":"debian"}`: "unsupported machine record or identity mismatch: debian",
	} {
		writePrivate(t, a.machinePath("debian"), content)
		a.problems = nil
		_, plain := a.machine("debian")
		if plain == nil || !strings.HasPrefix(plain.Error(), want) {
			t.Fatalf("%q: %v", content, plain)
		}
		a.problems = testRecorder(j)
		if _, err := a.machine("debian"); err == nil || err.Error() != plain.Error() {
			t.Fatalf("%q: error %v, want %v", content, err, plain)
		}
		expectError(t, j, "state.machine_record_invalid", plain)
	}
}

func TestProblemRecordVMExitedOnStart(t *testing.T) {
	a, f, v := selectedVM(t)
	// The VM unit starts and exits at once: systemd-run leaves no unit.
	f.blocking = func(_ context.Context, bin string, args []string) (bool, error) {
		return bin == "systemd-run" && strings.Contains(strings.Join(args, " "), "--unit="+vmUnit(v)+" "), nil
	}
	j := listenJournal(t)
	a.problems = testRecorder(j)
	_, err := a.runningVM(true)
	if want := "the VM exited; see journalctl --user -u " + vmUnit(v) + ", then run nsl recover"; err == nil || err.Error() != want {
		t.Fatalf("error %v, want %s", err, want)
	}
	expectError(t, j, "vm.exited_on_start", err)
}

func TestProblemRecordVMReadinessTimeout(t *testing.T) {
	a, f, v := selectedVM(t)
	saved := vmReadyTimeout
	vmReadyTimeout = 100 * time.Millisecond
	t.Cleanup(func() { vmReadyTimeout = saved })
	f.agent = func(req *protocol.Request, _ io.Reader, _ io.Writer) error {
		if req.Op == "identity" {
			return errors.New("the agent is not ready")
		}
		return nil
	}
	j := listenJournal(t)
	a.problems = testRecorder(j)
	_, err := a.runningVM(true)
	want := "VM readiness timed out; disks retained; see journalctl --user -u " + vmUnit(v) + " or run nsl recover: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "the agent is not ready") {
		t.Fatalf("error %v, want %s...", err, want)
	}
	expectError(t, j, "vm.readiness_timeout", err)
}

func TestProblemRecordDevVersionRecordsNothing(t *testing.T) {
	a, _, v := selectedVM(t)
	writePrivate(t, filepath.Join(v.dir, "vm.json"), "{\n")
	j := listenJournal(t)
	a.problems = problems.New(appID, "vmspawn-dev", problems.ComponentCLI, problems.WithSocket(j.path))
	if a.problems.Err() == nil {
		t.Fatal("a dev version gives an active recorder")
	}
	if _, err := a.loadVM(); err == nil {
		t.Fatal("a corrupt VM record loaded")
	}
	j.none(t)
}

func TestProblemComponent(t *testing.T) {
	for args, want := range map[string]problems.Component{"_forward": problems.ComponentDaemon, "_ssh x": problems.ComponentDaemon,
		"list": problems.ComponentCLI, "": problems.ComponentCLI, "-m dev": problems.ComponentCLI} {
		if got := component(strings.Fields(args)); got != want {
			t.Errorf("component(%q) = %s, want %s", args, got, want)
		}
	}
}

// panicWriter panics on its first write: nsl version panics with it.
type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("test panic") }

// childApp is the app of a child process: no runner, state in dir.
func childApp(dir string, out io.Writer) *app {
	return &app{home: filepath.Join(dir, "nsl"), runtimeDir: filepath.Join(dir, "run"), uid: os.Getuid(), gid: os.Getgid(),
		user: "u", group: "u", in: strings.NewReader(""), out: out, err: os.Stderr, hostRoot: dir}
}

// problemsChild runs nsl in a child process and returns its exit status. An
// empty socket gives no recorder.
func problemsChild(mode, socket, dir string) int {
	var rec *problems.Recorder
	if socket != "" {
		rec = problems.New(appID, "1.2.3", problems.ComponentCLI, problems.WithSocket(socket))
	}
	switch mode {
	case "panic":
		return run(rec, []string{"version"}, func() (*app, error) { return childApp(dir, panicWriter{}), nil })
	case "goroutine":
		// A panic in a goroutine with no RecoverPanic ends the process; only
		// the crash output keeps it.
		return run(rec, []string{"version"}, func() (*app, error) {
			never := make(chan struct{})
			go func() { panic("goroutine panic") }()
			<-never
			return nil, nil
		})
	case "list":
		return run(rec, []string{"list"}, func() (*app, error) { return childApp(dir, os.Stdout), nil })
	}
	return run(rec, []string{"version"}, func() (*app, error) { return childApp(dir, io.Discard), nil })
}

// runChild runs problemsChild in a new process with its crash folder in dir.
func runChild(t *testing.T, mode, socket, dir string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "STATE_DIRECTORY=") && !strings.HasPrefix(e, "XDG_STATE_HOME=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "NSL_TEST_PROBLEMS_CHILD="+mode, "NSL_TEST_PROBLEMS_SOCKET="+socket, "NSL_TEST_PROBLEMS_DIR="+dir,
		"XDG_STATE_HOME="+filepath.Join(dir, "state"))
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return cmd.ProcessState.ExitCode(), stderr.String()
}

func crashFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "state", "frostyard-problems", appID, "*.crash"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func expectPanic(t *testing.T, j *journal, message string) map[string]string {
	t.Helper()
	r := expectRecord(t, j, "panic", "2")
	if r["MESSAGE"] != message {
		t.Fatalf("MESSAGE = %q, want %q", r["MESSAGE"], message)
	}
	frames := strings.Split(r["FROSTYARD_FRAMES"], "\n")
	own := false
	for _, f := range frames {
		if strings.HasPrefix(f, "/") || strings.Contains(f, ".go:") || strings.ContainsAny(f, " \t+") || strings.Contains(f, "0x") {
			t.Fatalf("frame %q holds a file path, a line, an offset or an argument", f)
		}
		// nsl's own code is package main in nsl, and the package under test
		// in a test binary.
		own = own || strings.HasPrefix(f, "main.") || strings.HasPrefix(f, "github.com/frostyard/nsl.")
	}
	if !own {
		t.Fatalf("no frame of nsl's own code in %q", frames)
	}
	return r
}

func TestProblemRecordPanic(t *testing.T) {
	j, dir := listenJournal(t), t.TempDir()
	code, stderr := runChild(t, "panic", j.path, dir)
	if code != 2 || !strings.Contains(stderr, "panic: test panic") || !strings.Contains(stderr, "[recovered, repanicked]") {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if r := expectPanic(t, j, "test panic"); !strings.Contains(r["FROSTYARD_FRAMES"], ".(*app).execute\n") {
		t.Fatalf("the frames %q miss the command", r["FROSTYARD_FRAMES"])
	}
	j.none(t)
	// The next start reads the crash output of the panic, which already
	// gave its record.
	if code, stderr = runChild(t, "ok", j.path, dir); code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	j.none(t)
	if files := crashFiles(t, dir); len(files) != 1 {
		t.Fatalf("crash files %q, want the second run's own only", files)
	}
}

func TestProblemRecordCrashAtNextStart(t *testing.T) {
	j, dir := listenJournal(t), t.TempDir()
	code, stderr := runChild(t, "goroutine", j.path, dir)
	if code != 2 || !strings.Contains(stderr, "panic: goroutine panic") {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	j.none(t)
	if code, stderr = runChild(t, "ok", j.path, dir); code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	expectPanic(t, j, "panic: goroutine panic") // a crash record keeps the runtime's line
	j.none(t)
}

func TestProblemRecordWithoutJournal(t *testing.T) {
	dir := t.TempDir()
	a := childApp(dir, io.Discard)
	if err := a.init(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a.vmDir(), 0700); err != nil {
		t.Fatal(err)
	}
	writePrivate(t, filepath.Join(a.vmDir(), "vm.json"), "{\n")
	code, stderr := runChild(t, "list", "", dir)
	if code != 1 || !strings.HasPrefix(stderr, "nsl: VM record: ") || strings.Count(stderr, "\n") != 1 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	missing := filepath.Join(t.TempDir(), "no-journal")
	if gotCode, got := runChild(t, "list", missing, dir); gotCode != code || got != stderr {
		t.Fatalf("without a journal: exit %d, stderr %q; want exit %d, stderr %q", gotCode, got, code, stderr)
	}
}
