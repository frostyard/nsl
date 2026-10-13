package main

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestFinished(t *testing.T) {
	for _, c := range []struct {
		active, sub string
		job         uint32
		want        bool
	}{
		// Publication run 38254315979: the start job was still queued, the unit
		// inactive, and the agent stopped it before the command ran.
		{"inactive", "dead", 7, false},
		{"activating", "start", 7, false},
		{"active", "running", 0, false},
		{"active", "exited", 0, true},
		{"failed", "failed", 0, true},
		{"inactive", "dead", 0, true},
		{"active", "exited", 9, false},
	} {
		if got := finished(c.active, c.sub, c.job); got != c.want {
			t.Errorf("finished(%s, %s, %d) = %v", c.active, c.sub, c.job, got)
		}
	}
}

// The Job property arrives as a (uo) struct, which godbus decodes as []any.
func TestUnitJobStore(t *testing.T) {
	for want, wire := range map[uint32][]any{
		7: {uint32(7), dbus.ObjectPath("/org/freedesktop/systemd1/job/7")},
		0: {uint32(0), dbus.ObjectPath("/")},
	} {
		var job unitJob
		if err := dbus.MakeVariant(wire).Store(&job); err != nil || job.ID != want {
			t.Fatal(want, job, err)
		}
	}
}
