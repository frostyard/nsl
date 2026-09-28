package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

// A machine's desktop session (docs/specs/cli.md#desktop-and-host-actions): the
// host's Waypipe client and the machine's broker, joined to the machine through
// one SSH session running the agent's display operation.

func desktopUnit(v *vmRecord, name string) string {
	return fmt.Sprintf("nsl-%d-vm-%s-desktop-%s.service", v.Owner, v.ID, name)
}
func desktopDescription(v *vmRecord, name string) string { return "nsl desktop " + v.ID + " " + name }

// brokerSocket is the host end of a machine's broker, in the VM's private
// runtime directory, which keeps socket paths short.
func (a *app) brokerSocket(v *vmRecord, name string) string {
	return filepath.Join(filepath.Dir(a.socket(v)), name+"-open.sock")
}

// startDesktop starts a shared machine's desktop session when this command runs
// in a Wayland session with Waypipe available.
func (a *app) startDesktop(v *vmRecord, m *machineRecord) error {
	display := os.Getenv("WAYLAND_DISPLAY")
	if m.Tier != "shared" || display == "" {
		return nil
	}
	if _, err := exec.LookPath(a.waypipe); err != nil {
		return nil
	}
	// A broker that answers shows a running session without asking systemd.
	if c, err := net.DialTimeout("unix", a.brokerSocket(v, m.Name), time.Second); err == nil {
		c.Close()
		return nil
	}
	env := []string{"WAYLAND_DISPLAY=" + display}
	for _, key := range []string{"XDG_RUNTIME_DIR", "NSL_WAYPIPE", "NSL_OPENER"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	// A notify unit: starting it waits until the machine can use the display.
	return a.startHelper(v, desktopUnit(v, m.Name), desktopDescription(v, m.Name), env, "--property=Type=notify", "--property=TimeoutStartSec=30", "--", "_desktop", m.Name)
}

// notifyReady tells the service manager that the unit has started.
func notifyReady() error {
	address := os.Getenv("NOTIFY_SOCKET")
	if address == "" {
		return nil
	}
	if address[0] == '@' {
		address = "\x00" + address[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: address, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte("READY=1"))
	return err
}

// desktop runs a machine's desktop session until the VM, the Waypipe client or
// the SSH session ends. It returns an error so the unit restarts.
func (a *app) desktop(args []string) error {
	if len(args) != 1 {
		return errors.New("internal desktop session requires a machine")
	}
	m, err := a.machine(args[0])
	if err != nil {
		return err
	}
	v, err := a.loadVM()
	if err != nil || v == nil {
		return errors.Join(errors.New("there is no nsl VM"), err)
	}
	if err = a.runtimeFiles(v); err != nil {
		return err
	}
	shares, err := a.hostShares()
	if err != nil {
		return err
	}
	transport, broker := filepath.Join(filepath.Dir(a.socket(v)), m.Name+"-waypipe.sock"), a.brokerSocket(v, m.Name)
	for _, p := range []string{transport, broker} {
		if err = os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	listener, err := net.Listen("unix", broker)
	if err != nil {
		return err
	}
	defer listener.Close()
	go a.serveBroker(listener, m.Name, shares)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Either side ending ends the session; the unit restarts it after a failure.
	ended := make(chan error, 2)
	finished := func(what string, err error) error {
		if err == nil {
			return errors.New(what + " ended")
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	go func() {
		ended <- finished("the Waypipe client", a.r.run(ctx, nil, io.Discard, a.err, os.Environ(), a.waypipe, "--no-gpu", "--socket", transport, "client"))
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if st, err := os.Lstat(transport); err == nil && st.Mode()&os.ModeSocket != 0 {
			break
		}
		select {
		case err := <-ended:
			return err
		default:
		}
		if time.Now().After(deadline) {
			return errors.New("the Waypipe client did not start")
		}
	}
	request, err := protocol.Encode(protocol.Request{Protocol: protocol.Version, Op: "display", Machine: m.Name, ID: m.ID})
	if err != nil {
		return err
	}
	// The agent serves the session until its stdin ends, which is when ssh ends.
	session, hold := io.Pipe()
	defer hold.Close()
	reports, output := io.Pipe()
	go func() {
		lines := bufio.NewScanner(reports)
		for lines.Scan() {
			if lines.Text() == "ready" {
				if err := notifyReady(); err != nil {
					fmt.Fprintln(a.err, "nsl: notifying readiness:", err)
				}
			}
		}
	}()
	go func() {
		err := a.r.run(ctx, session, output, a.err, os.Environ(), "ssh", "-F", filepath.Join(v.dir, "ssh.config"),
			"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ExitOnForwardFailure=yes",
			"-R", "/run/nsl/waypipe-"+m.Name+".sock:"+transport, "-R", "/run/nsl/desktop/"+m.Name+"/open.sock:"+broker, "-T", "vm", request)
		output.Close()
		ended <- finished("the desktop session", err)
	}()
	return <-ended
}

func (a *app) serveBroker(l net.Listener, machine string, shares []protocol.Share) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go a.broker(c, machine, shares)
	}
}

// varlinkCall is one Varlink method call; the broker allows no flags.
type varlinkCall struct {
	Method     string          `json:"method"`
	Parameters json.RawMessage `json:"parameters"`
	Oneway     bool            `json:"oneway,omitempty"`
	More       bool            `json:"more,omitempty"`
	Upgrade    bool            `json:"upgrade,omitempty"`
}

// broker answers one Varlink call from a machine's nsl-open.
func (a *app) broker(c net.Conn, machine string, shares []protocol.Share) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	reply := func(v any) {
		b, _ := json.Marshal(v)
		c.Write(append(b, 0))
	}
	refuse := func(reason string) {
		reply(map[string]any{"error": "io.frostyard.nsl.Broker.Refused", "parameters": map[string]string{"reason": reason}})
	}
	message, err := bufio.NewReader(io.LimitReader(c, 64<<10)).ReadBytes(0)
	if err != nil {
		return
	}
	var call varlinkCall
	var parameters struct {
		Target string `json:"target"`
	}
	if err = protocol.DecodeStrict(bytes.TrimSuffix(message, []byte{0}), &call); err != nil || call.Oneway || call.More || call.Upgrade {
		refuse("invalid Varlink call")
		return
	}
	if call.Method != "io.frostyard.nsl.Broker.Open" {
		reply(map[string]any{"error": "org.varlink.service.MethodNotFound", "parameters": map[string]string{"method": call.Method}})
		return
	}
	if err = protocol.DecodeStrict(call.Parameters, &parameters); err != nil {
		refuse("Open takes one parameter, target")
		return
	}
	target, err := brokerTarget(parameters.Target, shares)
	if err != nil {
		fmt.Fprintf(a.err, "nsl-open from %s refused: %q: %v\n", machine, parameters.Target, err)
		refuse(err.Error())
		return
	}
	fmt.Fprintf(a.err, "nsl-open from %s: %s\n", machine, target)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = a.r.run(ctx, nil, io.Discard, a.err, os.Environ(), a.opener, target); err != nil {
		reply(map[string]any{"error": "io.frostyard.nsl.Broker.Failed", "parameters": map[string]string{"reason": err.Error()}})
		return
	}
	reply(map[string]any{"parameters": map[string]any{}})
}

// brokerTarget accepts an http or https URL, or a machine path under /mnt/host
// whose host path, symlinks resolved, lies in a shared tree. It returns what the
// host opener receives.
func brokerTarget(target string, shares []protocol.Share) (string, error) {
	if len(target) > 4096 || strings.IndexFunc(target, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", errors.New("the target is too long or has control characters")
	}
	if strings.HasPrefix(target, "/") {
		rest, ok := strings.CutPrefix(path.Clean(target), "/mnt/host/")
		if !ok {
			return "", errors.New("only paths under /mnt/host can be opened on the host")
		}
		resolved, err := filepath.EvalSymlinks("/" + rest)
		if err != nil {
			return "", fmt.Errorf("the host cannot open %s: %v", "/"+rest, err)
		}
		if _, shared := translate(resolved, shares); !shared {
			return "", errors.New("the path leaves the trees shared with machines")
		}
		return resolved, nil
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return "", errors.New("only http and https URLs and /mnt/host paths can be opened")
	}
	return target, nil
}
