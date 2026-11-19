package jira_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

// exerciseDoctorReads drives the Connector's probe and doctor reads once so a
// run's route accounting sees the routes they need.
func exerciseDoctorReads(t *testing.T, tr forge.IssueTracker) {
	t.Helper()
	if _, err := tr.Probe(); err != nil {
		t.Errorf("Probe: %v", err)
	}
	if c, ok := forge.As[forge.StatusMappingChecker](tr); !ok {
		t.Error("Connector tracker does not serve CheckMappedStatuses: it must advertise tracker.statuses")
	} else if _, err := c.CheckMappedStatuses(); err != nil {
		t.Errorf("CheckMappedStatuses: %v", err)
	}
	if c, ok := forge.As[forge.IssueTypeChecker](tr); !ok {
		t.Error("Connector tracker does not serve CheckIssueType: it must advertise tracker.createMeta")
	} else if _, err := c.CheckIssueType(); err != nil {
		t.Errorf("CheckIssueType: %v", err)
	}
}

func (h *connectorHarness) setMyself(status int) {
	h.mutex(func() { h.myselfStatus = status })
}

func (h *connectorHarness) mutex(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f()
}

// Probe maps the site's answer to /myself: a good credential names the
// project, 401 and 403 are an auth failure, and any other failure reads as a
// project the credential cannot reach.
func TestConnector_Probe(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarness(t, fl.cloud)
			for _, tc := range []struct {
				status       int
				auth, absent bool
				fails        bool
			}{
				{status: 0},
				{status: 401, auth: true, fails: true},
				{status: 403, auth: true, fails: true},
				{status: 404, absent: true, fails: true},
				{status: 500, absent: true, fails: true},
			} {
				h.setMyself(tc.status)
				slug, err := h.tr.Probe()
				if (err != nil) != tc.fails ||
					errors.Is(err, forge.ErrAuthFailure) != tc.auth ||
					errors.Is(err, forge.ErrRepoNotFound) != tc.absent {
					t.Errorf("status %d: Probe err = %v, want fails=%v auth=%v absent=%v", tc.status, err, tc.fails, tc.auth, tc.absent)
				}
				if tc.status == 0 && slug != connectorProject {
					t.Errorf("good credential: Probe slug = %q, want %q", slug, connectorProject)
				}
			}
		})
	}
}

type doctorFixture struct {
	name         string
	mapping      string // JIRA_STATUS_MAPPING
	issueType    string
	workflow     []string
	fields       []metaField
	failStatuses bool   // statuses endpoint answers 403
	wantFail     bool   // the doctor reports a finding
	wantFinding  string // substring of the finding wantFail expects
}

var doctorFixtures = []doctorFixture{
	{
		name:      "passing",
		mapping:   `{"dispatchable":"to do","inProgress":"In Progress","complete":{"status":"DONE","resolution":"Fixed"}}`,
		issueType: "Task",
		workflow:  []string{"To Do", "In Progress", "Done"},
		fields:    []metaField{{FieldID: "summary", Name: "Summary", Required: true}},
	},
	{
		name:        "status missing",
		mapping:     `{"dispatchable":"To Do","inProgress":"Wip","complete":"Dnoe","failed":"Blocked","ambiguous":"blocked"}`,
		issueType:   "Task",
		workflow:    []string{"To Do", "Done"},
		wantFail:    true,
		wantFinding: "does not have: \"Blocked\", \"Dnoe\", \"Wip\"",
	},
	{
		name:        "issue type not offered",
		mapping:     `{"dispatchable":"To Do"}`,
		issueType:   "Story",
		workflow:    []string{"To Do"},
		wantFail:    true,
		wantFinding: "is not an issue type of project PROJ (offered: Task, Bug",
	},
	{
		name:      "required field unfilled",
		issueType: "Bug",
		workflow:  []string{"To Do"},
		fields: []metaField{
			{FieldID: "summary", Name: "Summary", Required: true},
			{FieldID: "priority", Name: "Priority", Required: true, HasDefaultValue: true},
			{FieldID: "customfield_10010", Name: "Team", Required: true},
		},
		wantFail:    true,
		wantFinding: "\"Team\" (customfield_10010)",
	},
	{
		name:         "statuses unreadable",
		mapping:      `{"dispatchable":"To Do"}`,
		issueType:    "Task",
		failStatuses: true,
		wantFail:     true,
		wantFinding:  "status-mapping: degraded",
	},
}

// A check row is a pure function of what the tracker's check reads return, so
// each fixture's findings are the rows' verdicts over the scripted site: the
// passing fixture reports every row ok, each other one at least one finding.
func TestConnector_DoctorChecks(t *testing.T) {
	for _, fl := range connectorFlavours {
		for _, fx := range doctorFixtures {
			t.Run(fl.name+"/"+fx.name, func(t *testing.T) {
				mapping, err := jira.ParseStatusMapping(fx.mapping)
				if err != nil {
					t.Fatalf("ParseStatusMapping: %v", err)
				}
				statuses := map[forge.DispatchState]string{}
				for st, tgt := range mapping {
					statuses[st] = tgt.Status
				}
				h := newConnectorHarnessWith(t, fl.cloud, connectortracker.Lifecycle{
					Labels: testLabels, Statuses: statuses, IssueType: fx.issueType,
				})
				h.mutex(func() {
					h.workflowStatuses = fx.workflow
					h.metaFields = fx.fields
					h.statusesFail = fx.failStatuses
				})

				got := doctorFindings(h.tr)
				failing := slices.ContainsFunc(got, func(s string) bool { return !strings.HasSuffix(s, ": ok") })
				if fx.wantFinding != "" && !slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, fx.wantFinding) }) {
					t.Errorf("findings %q: none contains %q", got, fx.wantFinding)
				}
				if failing != fx.wantFail {
					t.Errorf("findings %q: failing = %v, want %v", got, failing, fx.wantFail)
				}
			})
		}
	}
}

// doctorFindings runs the status-mapping and issue-type rows built from tr's
// capabilities, one entry per row: its name and the error it reports.
func doctorFindings(tr forge.IssueTracker) []string {
	caps := forge.ResolveCapabilities(nil, tr, backend.Descriptor{}, backend.Descriptor{})
	var out []string
	for _, c := range append(doctor.StatusMappingChecks(caps), doctor.IssueTypeChecks(caps)...) {
		_, err := c.Probe()
		msg := "ok"
		switch {
		case errors.Is(err, doctor.ErrDegraded):
			// The wrapped transport text is each side's own; the row's tier is the finding.
			msg = "degraded"
		case err != nil:
			msg = err.Error()
		}
		out = append(out, c.Name+": "+msg)
	}
	return out
}
