package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/godbus/dbus/v5"
)

// system is the VM's service manager and machined, and through them each
// running machine's own service manager.
type system interface {
	UnitState(unit string) (string, error)
	StartUnit(unit string) error
	StopUnit(unit string) error
	KillMachine(name string, signal int) error
	TerminateMachine(name string) error
	OpenPTY(name string) (*os.File, string, error)
	Machine(name string) (manager, error)
}

// manager is a running machine's service manager.
type manager interface {
	SystemState() (string, error)
	Sessions() (int, error)
	Start(unit string, spec unitSpec) error
	Wait(ctx context.Context, unit string) (code, status int32, err error)
	Discard(unit string)
	Close() error
}

// unitSpec describes a transient command unit in a machine.
type unitSpec struct {
	Argv       []string
	SearchPath []string
	User       string
	PAM        bool
	Directory  string
	Env        []string
	Stdio      [3]int // file descriptors, when TTY is empty
	TTY        string // PTY path inside the machine
}

const (
	systemdName   = "org.freedesktop.systemd1"
	systemdPath   = "/org/freedesktop/systemd1"
	managerIface  = "org.freedesktop.systemd1.Manager"
	machinedName  = "org.freedesktop.machine1"
	machinedPath  = "/org/freedesktop/machine1"
	machinedIface = "org.freedesktop.machine1.Manager"
)

type dbusSystem struct{ conn *dbus.Conn }

func newSystem() (system, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connecting to the VM's system bus: %w", err)
	}
	return &dbusSystem{conn}, nil
}

func (s *dbusSystem) manager() dbus.BusObject { return s.conn.Object(systemdName, systemdPath) }

func unitState(conn *dbus.Conn, unit string) (string, error) {
	var path dbus.ObjectPath
	err := conn.Object(systemdName, systemdPath).Call(managerIface+".GetUnit", 0, unit).Store(&path)
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) && dbusErr.Name == "org.freedesktop.systemd1.NoSuchUnit" {
		return "inactive", nil
	}
	if err != nil {
		return "", err
	}
	v, err := conn.Object(systemdName, path).GetProperty("org.freedesktop.systemd1.Unit.ActiveState")
	if err != nil {
		return "", err
	}
	state, _ := v.Value().(string)
	return state, nil
}

func (s *dbusSystem) UnitState(unit string) (string, error) { return unitState(s.conn, unit) }

func (s *dbusSystem) StartUnit(unit string) error {
	return s.manager().Call(managerIface+".StartUnit", 0, unit, "replace").Err
}

func (s *dbusSystem) StopUnit(unit string) error {
	return s.manager().Call(managerIface+".StopUnit", 0, unit, "replace").Err
}

func (s *dbusSystem) KillMachine(name string, signal int) error {
	return s.conn.Object(machinedName, machinedPath).Call(machinedIface+".KillMachine", 0, name, "leader", int32(signal)).Err
}

func (s *dbusSystem) TerminateMachine(name string) error {
	return s.conn.Object(machinedName, machinedPath).Call(machinedIface+".TerminateMachine", 0, name).Err
}

func (s *dbusSystem) OpenPTY(name string) (*os.File, string, error) {
	var fd dbus.UnixFD
	var path string
	if err := s.conn.Object(machinedName, machinedPath).Call(machinedIface+".OpenMachinePTY", 0, name).Store(&fd, &path); err != nil {
		return nil, "", fmt.Errorf("opening a PTY in %s: %w", name, err)
	}
	return os.NewFile(uintptr(fd), "pty"), path, nil
}

// Machine connects to the machine's service manager through its private
// socket. Machines run without a user namespace, so VM root is machine root.
func (s *dbusSystem) Machine(name string) (manager, error) {
	var path dbus.ObjectPath
	if err := s.conn.Object(machinedName, machinedPath).Call(machinedIface+".GetMachine", 0, name).Store(&path); err != nil {
		return nil, err
	}
	v, err := s.conn.Object(machinedName, path).GetProperty("org.freedesktop.machine1.Machine.Leader")
	if err != nil {
		return nil, err
	}
	leader, ok := v.Value().(uint32)
	if !ok || leader == 0 {
		return nil, errors.New("machine has no leader")
	}
	conn, err := dbus.Dial("unix:path=/proc/" + strconv.FormatUint(uint64(leader), 10) + "/root/run/systemd/private")
	if err != nil {
		return nil, err
	}
	if err = conn.Auth([]dbus.Auth{dbus.AuthExternal(strconv.Itoa(os.Getuid()))}); err != nil {
		conn.Close()
		return nil, err
	}
	return &dbusManager{conn: conn}, nil
}

type dbusManager struct{ conn *dbus.Conn }

func (m *dbusManager) object() dbus.BusObject { return m.conn.Object(systemdName, systemdPath) }

func (m *dbusManager) Close() error { return m.conn.Close() }

func (m *dbusManager) SystemState() (string, error) {
	v, err := m.object().GetProperty(managerIface + ".SystemState")
	if err != nil {
		return "", err
	}
	state, _ := v.Value().(string)
	return state, nil
}

func (m *dbusManager) Sessions() (int, error) {
	var units [][]any
	err := m.object().Call(managerIface+".ListUnitsByPatterns", 0, []string{"active", "activating"}, []string{"nsl-run-*.service"}).Store(&units)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range units {
		// A finished command stays active (exited) until the agent collects it.
		if len(u) > 4 && u[4] != "exited" {
			n++
		}
	}
	return n, nil
}

type property struct {
	Name  string
	Value dbus.Variant
}

type execCommand struct {
	Path  string
	Argv  []string
	Flags []string
}

func (m *dbusManager) Start(unit string, spec unitSpec) error {
	props := []property{
		{"Description", dbus.MakeVariant("nsl command")},
		{"Type", dbus.MakeVariant("exec")},
		// Keep the result readable after the command exits; the agent collects it.
		{"RemainAfterExit", dbus.MakeVariant(true)},
		// Argv reaches the program literally: no $VAR expansion.
		{"ExecStartEx", dbus.MakeVariant([]execCommand{{spec.Argv[0], spec.Argv, []string{"no-env-expand"}}})},
		{"ExecSearchPath", dbus.MakeVariant(spec.SearchPath)},
		{"User", dbus.MakeVariant(spec.User)},
		{"WorkingDirectory", dbus.MakeVariant(spec.Directory)},
		{"Environment", dbus.MakeVariant(spec.Env)},
		{"IgnoreSIGPIPE", dbus.MakeVariant(false)},
		{"SendSIGHUP", dbus.MakeVariant(true)},
		{"TimeoutStopUSec", dbus.MakeVariant(uint64(10 * time.Second / time.Microsecond))},
	}
	if spec.PAM {
		props = append(props, property{"PAMName", dbus.MakeVariant("nsl")})
	}
	if spec.TTY != "" {
		props = append(props, property{"TTYPath", dbus.MakeVariant(spec.TTY)},
			property{"StandardInput", dbus.MakeVariant("tty")}, property{"StandardOutput", dbus.MakeVariant("tty")},
			property{"StandardError", dbus.MakeVariant("tty")})
	} else {
		props = append(props, property{"StandardInputFileDescriptor", dbus.MakeVariant(dbus.UnixFD(spec.Stdio[0]))},
			property{"StandardOutputFileDescriptor", dbus.MakeVariant(dbus.UnixFD(spec.Stdio[1]))},
			property{"StandardErrorFileDescriptor", dbus.MakeVariant(dbus.UnixFD(spec.Stdio[2]))})
	}
	// Signals about the unit arrive only after subscribing on this connection.
	if err := m.object().Call(managerIface+".Subscribe", 0).Err; err != nil {
		return err
	}
	return m.object().Call(managerIface+".StartTransientUnit", 0, unit, "fail", props, []struct {
		Name       string
		Properties []property
	}{}).Err
}

func (m *dbusManager) unit(unit string) (dbus.BusObject, error) {
	var path dbus.ObjectPath
	if err := m.object().Call(managerIface+".GetUnit", 0, unit).Store(&path); err != nil {
		return nil, err
	}
	return m.conn.Object(systemdName, path), nil
}

// Wait returns the main process's ExecMainCode and ExecMainStatus once it exits.
func (m *dbusManager) Wait(ctx context.Context, unit string) (int32, int32, error) {
	signals := make(chan *dbus.Signal, 16)
	m.conn.Signal(signals)
	defer m.conn.RemoveSignal(signals)
	obj, err := m.unit(unit)
	if err != nil {
		return 0, 0, err
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		active, err := obj.GetProperty("org.freedesktop.systemd1.Unit.ActiveState")
		if err != nil {
			return 0, 0, err
		}
		sub, err := obj.GetProperty("org.freedesktop.systemd1.Unit.SubState")
		if err != nil {
			return 0, 0, err
		}
		if a, s := active.Value().(string), sub.Value().(string); a == "failed" || a == "inactive" || (a == "active" && s == "exited") {
			var code, status int32
			if v, err := obj.GetProperty("org.freedesktop.systemd1.Service.ExecMainCode"); err == nil {
				code, _ = v.Value().(int32)
			}
			v, err := obj.GetProperty("org.freedesktop.systemd1.Service.ExecMainStatus")
			if err != nil {
				return 0, 0, err
			}
			status, _ = v.Value().(int32)
			return code, status, nil
		}
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-signals:
		case <-tick.C:
		}
	}
}

func (m *dbusManager) Discard(unit string) {
	m.object().Call(managerIface+".StopUnit", 0, unit, "replace")
	m.object().Call(managerIface+".ResetFailedUnit", 0, unit)
}
