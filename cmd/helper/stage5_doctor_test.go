package helper

import "testing"

func TestDoctorChecksStage5UnitEnablement(t *testing.T) {
	state := func(name string) systemdUnitState {
		return systemdUnitState{Name: name, Path: "/etc/systemd/system/" + name, Present: true, Active: "active", Enabled: "enabled"}
	}
	if got := doctorBootConvergeCheck(state, "box"); got.Status != doctorStatusOK || got.ID != doctorCheckBootConverge {
		t.Fatalf("boot check=%+v", got)
	}
	if got := doctorGCTimerCheck(state, "box"); got.Status != doctorStatusOK || got.ID != doctorCheckGCTimer {
		t.Fatalf("GC check=%+v", got)
	}
	missing := func(name string) systemdUnitState {
		return systemdUnitState{Name: name, Path: "/etc/systemd/system/" + name, Enabled: "disabled"}
	}
	if got := doctorBootConvergeCheck(missing, "box"); got.Status != doctorStatusFailed {
		t.Fatalf("missing boot check=%+v", got)
	}
	if got := doctorGCTimerCheck(missing, "box"); got.Status != doctorStatusFailed {
		t.Fatalf("missing GC check=%+v", got)
	}
}

