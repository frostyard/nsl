package main

import (
	"strings"

	"github.com/frostyard/std/problems"
)

// appID names nsl in its problem records (ADR-0023, frostyard/core ADR-0069).
const appID = "org.frostyard.nsl"

// The codes of nsl's internal errors. Each one marks a state nsl's own code
// should never reach; a user mistake gets no record.
const (
	codeVMRecordInvalid      = "state.vm_record_invalid"
	codeMachineRecordInvalid = "state.machine_record_invalid"
	codeVMExitedOnStart      = "vm.exited_on_start"
	codeVMReadinessTimeout   = "vm.readiness_timeout"
)

// component names the part of nsl that runs: the internal helper commands
// (_forward, _desktop, _devices and _launch run as systemd user units, and
// _ssh is ssh's ProxyCommand) are daemon; every other command is cli.
func component(args []string) problems.Component {
	if len(args) > 0 && strings.HasPrefix(args[0], "_") {
		return problems.ComponentDaemon
	}
	return problems.ComponentCLI
}
