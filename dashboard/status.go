package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// statusFileName is the daemon's status file inside the checkout's git dir.
// The Dashboard mirrors the daemon's published JSON with its own types rather
// than importing the launcher module (ADR 0060).
const statusFileName = "spindrift-daemon.status"

// statusSchema is the status-file schema this Dashboard understands. It
// mirrors the daemon's statusSchema and must bump with it, as
// nix/checks/dashboard-constant-parity.nix enforces.
const statusSchema = 1

// schemaError is readStatus's report of a status file written at a schema the
// Dashboard does not understand.
type schemaError struct{ got int }

func (e *schemaError) Error() string {
	return fmt.Sprintf("Daemon schema %d, Dashboard understands %d: restart the Dashboard", e.got, statusSchema)
}

// Status is the Daemon's published status file (see Status file under Daemon
// in docs/reference.md).
type Status struct {
	Pid      int            `json:"pid"`
	Host     string         `json:"host"`
	Started  string         `json:"started"`
	Time     string         `json:"time"`
	Kinds    []string       `json:"kinds"`
	State    string         `json:"state"`
	Reason   string         `json:"reason,omitempty"`
	Slots    []SlotStatus   `json:"slots"`
	Checks   []KindCheck    `json:"checks"`
	Trackers []TrackerCheck `json:"trackers,omitempty"`
	// RepoURL is the Target repo's web URL; absent when the tracker has none
	// (local, jira), in which case nothing links out to an issue.
	RepoURL string `json:"repo_url,omitempty"`
}

// TrackerCheck is one issue tracker's rate-limit state.
type TrackerCheck struct {
	Tracker          string `json:"tracker"`
	RateLimitedUntil string `json:"rate_limited_until,omitempty"`
}

// SlotStatus is one slot of the Daemon's pool; Since is when it entered Phase.
type SlotStatus struct {
	Slot      int      `json:"slot"`
	Phase     string   `json:"phase"`
	Busy      bool     `json:"busy"`
	Since     string   `json:"since"`
	Kind      string   `json:"kind,omitempty"`
	Revision  string   `json:"revision,omitempty"`
	Issues    []string `json:"issues,omitempty"`
	Chore     string   `json:"chore,omitempty"`
	Pass      string   `json:"pass,omitempty"`
	Model     string   `json:"model,omitempty"`
	ModelRole string   `json:"model_role,omitempty"`
	// ChildStart is the exact time string of a running slot's child_start event;
	// ChildStartN counts earlier child_starts on the slot sharing that string.
	ChildStart  string `json:"child_start,omitempty"`
	ChildStartN int    `json:"child_start_n,omitempty"`
}

// KindCheck is the Daemon's per-kind next-check, jam and Demand state.
type KindCheck struct {
	Kind             string `json:"kind"`
	NextCheck        string `json:"nextCheck,omitempty"`
	Jammed           bool   `json:"jammed,omitempty"`
	Ready            *int   `json:"ready,omitempty"`
	ProbedAt         string `json:"probed_at,omitempty"`
	NextProbe        string `json:"next_probe,omitempty"`
	JamUntil         string `json:"jam_until,omitempty"`
	ReadyAtJam       *int   `json:"ready_at_jam,omitempty"`
	NextDue          string `json:"next_due,omitempty"`
	NextDueOnTipMove bool   `json:"next_due_on_tip_move,omitempty"`
}

// stateHalted is the State the Daemon publishes when the pool has halted.
const stateHalted = "halted"

func (s *Status) halted() bool { return s.State == stateHalted }

// nextDueOnTipMove is the literal the daemon publishes in NextDue when only a
// moved tip lifts a child-reported kind's wait.
const nextDueOnTipMove = "on_tip_move"

// readStatus reads path. A missing file is (nil, nil, nil); an unparseable
// one returns the raw bytes beside the error so the page can show them. A
// schema the Dashboard does not understand returns a *schemaError and the raw
// bytes, undecoded.
func readStatus(path string) (*Status, []byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	// Peek at the schema alone: a breaking change may retype a field, so the
	// full decode below could fail or misread under a schema this Dashboard
	// does not know.
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, data, fmt.Errorf("parse %s: %w", path, err)
	}
	got := head.Schema
	if got == 0 { // a daemon predating the field wrote the schema-1 shape
		got = 1
	}
	if got != statusSchema {
		return nil, data, &schemaError{got: got}
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, data, fmt.Errorf("parse %s: %w", path, err)
	}
	return &s, data, nil
}

// processAlive reports whether the daemon at pid on host is still running.
// The checkout lock would be the truth, but probing it with a flock could
// race the daemon's own acquire, so liveness is by pid. A pid on another host
// cannot be probed and is taken as live.
func processAlive(pid int, host string) bool {
	if pid <= 0 {
		return false
	}
	self, err := os.Hostname()
	if err != nil || self != host {
		return true
	}
	// EPERM means the process exists under another user.
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
