package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/frostyard/nsl/internal/protocol"
)

// listeners reports the TCP sockets listening for machines in the VM's network
// namespace, which the machines share. Each socket is attributed through the
// control group of a process holding it, systemd-nspawn@NAME.service.
func (a *agent) listeners() error {
	sockets := map[string][]protocol.Listener{} // by socket inode
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(a.path(table))
		if err != nil {
			return err
		}
		for inode, l := range listening(string(b)) {
			sockets[inode] = append(sockets[inode], l)
		}
	}
	records, err := a.records()
	if err != nil {
		return err
	}
	machines := map[string]bool{}
	for _, r := range records {
		machines[r.Name] = true
	}
	out := []protocol.Listener{}
	for inode, machine := range a.socketOwners(sockets, machines) {
		for _, l := range sockets[inode] {
			l.Machine = machine
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Port < out[j].Port || out[i].Port == out[j].Port && out[i].Address < out[j].Address
	})
	return json.NewEncoder(a.stdout).Encode(out)
}

// listening parses /proc/net/tcp or tcp6 for loopback or wildcard listeners on
// forwardable ports, by socket inode.
func listening(table string) map[string]protocol.Listener {
	out := map[string]protocol.Listener{}
	for _, line := range strings.Split(table, "\n") {
		// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
		f := strings.Fields(line)
		if len(f) < 10 || f[3] != "0A" {
			continue
		}
		address, port, ok := strings.Cut(f[1], ":")
		n, err := strconv.ParseUint(port, 16, 16)
		if !ok || err != nil || n < 1024 || n == 5353 || n == 5355 || f[9] == "0" {
			continue
		}
		// The forwarder reaches VM loopback, so only loopback and wildcard
		// listeners are reachable.
		if ip := kernelAddress(address); ip != nil && (ip.IsUnspecified() || ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback)) {
			out[f[9]] = protocol.Listener{Port: int(n), Address: ip.String()}
		}
	}
	return out
}

// kernelAddress decodes an address as /proc/net/tcp prints it: 32-bit words in
// host (little-endian) order.
func kernelAddress(s string) net.IP {
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil
	}
	ip := make(net.IP, len(b))
	for i := 0; i < len(b); i += 4 {
		binary.BigEndian.PutUint32(ip[i:], binary.LittleEndian.Uint32(b[i:]))
	}
	if len(ip) == 4 {
		return net.IPv4(ip[0], ip[1], ip[2], ip[3])
	}
	return ip
}

// socketOwners maps socket inodes to the machines whose processes hold them.
func (a *agent) socketOwners(sockets map[string][]protocol.Listener, machines map[string]bool) map[string]string {
	owners := map[string]string{}
	processes, err := os.ReadDir(a.path("/proc"))
	if err != nil {
		return owners
	}
	for _, p := range processes {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		dir := a.path("/proc/" + p.Name())
		fds, err := os.ReadDir(dir + "/fd")
		if err != nil {
			continue
		}
		machine := ""
		for _, fd := range fds {
			link, err := os.Readlink(dir + "/fd/" + fd.Name())
			inode, ok := strings.CutPrefix(link, "socket:[")
			inode = strings.TrimSuffix(inode, "]")
			if err != nil || !ok || sockets[inode] == nil || owners[inode] != "" {
				continue
			}
			if machine == "" {
				if machine = cgroupMachine(dir + "/cgroup"); !machines[machine] {
					break
				}
			}
			owners[inode] = machine
		}
	}
	return owners
}

// cgroupMachine names the machine whose nspawn unit holds a process.
func cgroupMachine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, part := range strings.Split(strings.TrimSpace(string(b)), "/") {
		if name, ok := strings.CutPrefix(part, "systemd-nspawn@"); ok {
			if name, ok = strings.CutSuffix(name, ".service"); ok && protocol.ValidName(name) {
				return name
			}
		}
	}
	return ""
}
