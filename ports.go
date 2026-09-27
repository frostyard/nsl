package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type portStatus struct {
	Port  int    `json:"port"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}
type portReport struct {
	Updated time.Time    `json:"updated"`
	Ports   []portStatus `json:"ports"`
	Error   string       `json:"error,omitempty"`
}

func listenerPorts(output string) map[int]bool {
	ports := map[int]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		address := fields[3]
		i := strings.LastIndexByte(address, ':')
		if i < 0 {
			continue
		}
		p, err := strconv.Atoi(address[i+1:])
		if err == nil && p >= 1024 && p <= 65535 && p != 5353 && p != 5355 {
			ports[p] = true
		}
	}
	return ports
}
func (a *app) portSSH(e *environment, args ...string) ([]byte, error) {
	options := []string{"-F", filepath.Join(a.dir(e.Name), "ssh.config"), "-S", filepath.Join(filepath.Dir(a.socket(e)), "ports.sock")}
	return a.capture(5*time.Second, "ssh", append(options, args...)...)
}
func (a *app) forward(e *environment) error {
	if err := a.runtimeFiles(e); err != nil {
		return err
	}
	forwarded := map[int]bool{}
	defer func() { _, _ = a.portSSH(e, "-O", "exit", "guest") }()
	request, _ := encodeRequest([]string{"ss", "-H", "-4", "-ltn"}, "")
	for {
		state, err := a.unitState(e, unit(e))
		if err != nil {
			return err
		}
		if state != "active" && state != "activating" {
			return nil
		}
		report := portReport{Updated: time.Now(), Ports: []portStatus{}}
		if _, err = a.portSSH(e, "-O", "check", "guest"); err != nil {
			forwarded = map[int]bool{}
			_, err = a.portSSH(e, "-o", "ControlMaster=yes", "-o", "ControlPersist=no", "-fN", "guest")
		}
		var output []byte
		if err == nil {
			output, err = a.portSSH(e, "guest", "/usr/local/libexec/nsl-exec", request)
		}
		if err != nil {
			report.Error = err.Error()
		} else {
			desired := listenerPorts(string(output))
			for p := range forwarded {
				if !desired[p] {
					if _, cancelErr := a.portSSH(e, "-O", "cancel", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", p, p), "guest"); cancelErr != nil {
						return cancelErr
					}
					delete(forwarded, p)
				}
			}
			ordered := []int{}
			for p := range desired {
				ordered = append(ordered, p)
			}
			sort.Ints(ordered)
			for _, p := range ordered {
				row := portStatus{Port: p, State: "forwarded"}
				if !forwarded[p] {
					if _, forwardErr := a.portSSH(e, "-O", "forward", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", p, p), "guest"); forwardErr != nil {
						row.State = "conflict"
						row.Error = forwardErr.Error()
					} else {
						forwarded[p] = true
					}
				}
				report.Ports = append(report.Ports, row)
			}
		}
		b, _ := json.MarshalIndent(report, "", "  ")
		if err = atomicWrite(filepath.Join(a.dir(e.Name), "ports.json"), b, 0600); err != nil {
			return err
		}
		time.Sleep(time.Second)
	}
}
func (a *app) ports(e *environment) error {
	state, err := a.unitState(e, portUnit(e))
	if err != nil {
		return err
	}
	if state != "active" {
		fmt.Fprintln(a.out, "Port forwarding is stopped")
		return nil
	}
	path := filepath.Join(a.dir(e.Name), "ports.json")
	if err = privateFile(path, a.uid, 0077); os.IsNotExist(err) {
		fmt.Fprintln(a.out, "Port discovery is starting")
		return nil
	} else if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var report portReport
	if err = json.Unmarshal(b, &report); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Updated %s\n", report.Updated.Format(time.RFC3339))
	if report.Error != "" {
		fmt.Fprintln(a.out, report.Error)
	}
	for _, p := range report.Ports {
		fmt.Fprintf(a.out, "127.0.0.1:%d\t%s\t%s\n", p.Port, p.State, p.Error)
	}
	return nil
}
