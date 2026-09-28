package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/frostyard/nsl/internal/protocol"
)

func portsUnit(v *vmRecord) string        { return fmt.Sprintf("nsl-%d-vm-%s-ports.service", v.Owner, v.ID) }
func portsDescription(v *vmRecord) string { return "nsl ports " + v.ID }

// startHelper runs an nsl helper as a user unit bound to the VM's unit, so it
// ends with the VM, unless it runs already. Arguments before "--" are further
// systemd-run options.
func (a *app) startHelper(v *vmRecord, unit, description string, env []string, args ...string) error {
	state, err := a.unitState(unit, description)
	if err != nil || state == "active" || state == "activating" || state == "reloading" {
		return err
	}
	if state == "failed" {
		if _, err = a.capture(5*time.Second, "systemctl", "--user", "reset-failed", unit); err != nil {
			return err
		}
	}
	options := []string{"--user", "--quiet", "--unit=" + unit, "--description=" + description, "--collect",
		"--property=BindsTo=" + vmUnit(v), "--property=After=" + vmUnit(v), "--property=Restart=on-failure",
		"--property=RestartSec=5", "--setenv=NSL_HOME=" + a.home}
	for _, e := range env {
		options = append(options, "--setenv="+e)
	}
	if i := slices.Index(args, "--"); i >= 0 {
		options, args = append(options, args[:i]...), args[i+1:]
	}
	return a.call(nil, a.err, "systemd-run", append(append(options, "--", a.self), args...)...)
}

// stopHelper stops an owned helper unit.
func (a *app) stopHelper(unit, description string) error {
	state, err := a.unitState(unit, description)
	if err != nil || state == "inactive" {
		return err
	}
	_, err = a.capture(40*time.Second, "systemctl", "--user", "stop", unit)
	return err
}

type portStatus struct {
	Port    int    `json:"port"`
	Machine string `json:"machine"`
	Target  string `json:"target"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
}

type portReport struct {
	Updated time.Time    `json:"updated"`
	Ports   []portStatus `json:"ports"`
	Error   string       `json:"error,omitempty"`
}

// forwarder keeps one SSH connection to the VM with a local forward for every
// machine listener. It never displaces a host listener: a failed bind is
// reported as a conflict and retried.
type forwarder struct {
	a         *app
	v         *vmRecord
	forwarded map[int]string // port to VM target
}

// forwarding reports whether the forwarder wrote its report in the last few seconds.
func (a *app) forwarding(v *vmRecord) bool {
	st, err := os.Stat(filepath.Join(v.dir, "ports.json"))
	return err == nil && time.Since(st.ModTime()) < 5*time.Second
}

func (f *forwarder) ssh(args ...string) error {
	options := []string{"-F", filepath.Join(f.v.dir, "ssh.config"), "-S", filepath.Join(filepath.Dir(f.a.socket(f.v)), "ports.sock")}
	_, err := f.a.capture(10*time.Second, "ssh", append(options, args...)...)
	return err
}

func forwardSpec(port int, target string) string {
	return fmt.Sprintf("127.0.0.1:%d:%s:%d", port, target, port)
}

// step brings the forwards in line with the machines' listeners once.
func (f *forwarder) step() portReport {
	report := portReport{Updated: time.Now().UTC(), Ports: []portStatus{}}
	var listeners []protocol.Listener
	err := f.ssh("-O", "check", "vm")
	if err != nil {
		f.forwarded = map[int]string{}
		err = f.ssh("-o", "ControlMaster=yes", "-o", "ControlPersist=no", "-fN", "vm")
	}
	if err == nil {
		err = f.a.agentJSON(f.v, protocol.Request{Op: "listeners"}, nil, 10*time.Second, &listeners)
	}
	if err != nil {
		report.Error = err.Error()
		return report
	}
	// IPv4 and wildcard listeners take 127.0.0.1; one only on ::1 takes [::1].
	wanted, machines := map[int]string{}, map[int]string{}
	for _, l := range listeners {
		target := "127.0.0.1"
		if l.Address == "::1" {
			target = "[::1]"
		}
		if current, ok := wanted[l.Port]; !ok || current == "[::1]" {
			wanted[l.Port], machines[l.Port] = target, l.Machine
		}
	}
	for port, target := range f.forwarded {
		if wanted[port] != target {
			if err := f.ssh("-O", "cancel", "-L", forwardSpec(port, target), "vm"); err != nil {
				report.Error = err.Error()
				continue
			}
			delete(f.forwarded, port)
		}
	}
	ports := make([]int, 0, len(wanted))
	for port := range wanted {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	for _, port := range ports {
		row := portStatus{Port: port, Machine: machines[port], Target: wanted[port], State: "forwarded"}
		if _, ok := f.forwarded[port]; !ok {
			if err := f.ssh("-O", "forward", "-L", forwardSpec(port, wanted[port]), "vm"); err != nil {
				row.State, row.Error = "conflict", "host port in use or refused: "+strings.TrimSpace(err.Error())
			} else {
				f.forwarded[port] = wanted[port]
			}
		}
		report.Ports = append(report.Ports, row)
	}
	return report
}

func (a *app) forward(args []string) error {
	if len(args) != 1 || !protocol.ValidID(args[0]) {
		return errors.New("internal forwarder requires a VM")
	}
	v, err := a.vmByID(args[0])
	if err != nil {
		return err
	}
	if err = a.runtimeFiles(v); err != nil {
		return err
	}
	f := &forwarder{a: a, v: v, forwarded: map[int]string{}}
	defer func() { _ = f.ssh("-O", "exit", "vm") }()
	for {
		if state, err := a.vmState(v); err != nil || state != "running" {
			return err
		}
		b, _ := json.MarshalIndent(f.step(), "", "  ")
		if err = atomicWrite(filepath.Join(v.dir, "ports.json"), append(b, '\n'), 0600); err != nil {
			return err
		}
		time.Sleep(time.Second)
	}
}

func (a *app) ports(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: ports [NAME]")
	}
	name := ""
	if len(args) == 1 {
		m, err := a.machine(args[0])
		if err != nil {
			return err
		}
		name = m.Name
	}
	all, err := a.allVMs()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tMACHINE\tSTATE")
	var notes []string
	for _, v := range all {
		if v.Machine != nil && name != "" && v.Machine.Name != name {
			continue
		}
		report, note, err := a.portReport(v)
		if err != nil {
			return err
		}
		if note != "" {
			notes = append(notes, fmt.Sprintf("%s VM: %s", v.label(), note))
			continue
		}
		for _, p := range report.Ports {
			if name != "" && p.Machine != name {
				continue
			}
			state := p.State
			if p.Error != "" {
				state += ": " + p.Error
			}
			fmt.Fprintf(w, "127.0.0.1:%d\t%s\t%s\n", p.Port, p.Machine, state)
		}
	}
	if err = w.Flush(); err != nil {
		return err
	}
	for _, n := range notes {
		fmt.Fprintln(a.out, n)
	}
	if len(all) == 0 {
		fmt.Fprintln(a.out, "Port forwarding is stopped: there is no nsl VM yet")
	}
	return nil
}

// portReport reads a VM's forwarder report, or says why there is none.
func (a *app) portReport(v *vmRecord) (*portReport, string, error) {
	vm, err := a.vmState(v)
	if err != nil {
		return nil, "", err
	}
	forwarder, err := a.unitState(portsUnit(v), portsDescription(v))
	if err != nil {
		return nil, "", err
	}
	if vm != "running" || forwarder != "active" {
		return nil, fmt.Sprintf("port forwarding is stopped (VM %s, forwarder %s)", vm, forwarder), nil
	}
	path := filepath.Join(v.dir, "ports.json")
	if err = privateFile(path, a.uid, 0077); errors.Is(err, os.ErrNotExist) {
		return nil, "port forwarding is starting", nil
	} else if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var report portReport
	if err = protocol.DecodeStrict(b, &report); err != nil {
		return nil, "", fmt.Errorf("port report: %w", err)
	}
	if report.Error != "" {
		return &report, "port discovery failed: " + report.Error, nil
	}
	return &report, "", nil
}
