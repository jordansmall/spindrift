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

// nativeJira is the native adapter over the Connector harness's backend, which
// the adapter reaches through the backend's TLS client.
func nativeJira(h *connectorHarness, cfg jira.JiraConfig) forge.IssueTracker {
	cfg.BaseURL = h.srv.URL
	cfg.ProjectKey = connectorProject
	cfg.Token = connectorToken
	cfg.HTTPClient = h.srv.Client()
	if h.cloud {
		cfg.Email = connectorEmail
	}
	return jira.NewJiraClient(cfg)
}

func (h *connectorHarness) setMyself(status int) {
	h.mutex(func() { h.myselfStatus = status })
}

func (h *connectorHarness) mutex(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f()
}

func TestConnector_ProbeMatchesNative(t *testing.T) {
	for _, fl := range connectorFlavours {
		t.Run(fl.name, func(t *testing.T) {
			h := newConnectorHarness(t, fl.cloud)
			native := nativeJira(h, jira.JiraConfig{})
			for _, status := range []int{0, 401, 403, 404, 500} {
				h.setMyself(status)
				wantSlug, wantErr := native.Probe()
				gotSlug, gotErr := h.tr.Probe()
				if gotSlug != wantSlug {
					t.Errorf("status %d: Probe slug = %q, native %q", status, gotSlug, wantSlug)
				}
				if (gotErr == nil) != (wantErr == nil) ||
					errors.Is(gotErr, forge.ErrAuthFailure) != errors.Is(wantErr, forge.ErrAuthFailure) ||
					errors.Is(gotErr, forge.ErrRepoNotFound) != errors.Is(wantErr, forge.ErrRepoNotFound) {
					t.Errorf("status %d: Probe err = %v, native %v", status, gotErr, wantErr)
				}
				if status == 0 && (gotErr != nil || gotSlug != connectorProject) {
					t.Errorf("good credential: Probe = (%q, %v), want (%q, nil)", gotSlug, gotErr, connectorProject)
				}
				if status == 401 && !errors.Is(gotErr, forge.ErrAuthFailure) {
					t.Errorf("auth failure: Probe err = %v, want forge.ErrAuthFailure", gotErr)
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
	failStatuses bool // statuses endpoint answers 403
	wantFail     bool // the doctor reports a finding on the native side
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
		name:      "status missing",
		mapping:   `{"dispatchable":"To Do","inProgress":"Wip","complete":"Dnoe","failed":"Blocked","ambiguous":"blocked"}`,
		issueType: "Task",
		workflow:  []string{"To Do", "Done"},
		wantFail:  true,
	},
	{
		name:      "issue type not offered",
		mapping:   `{"dispatchable":"To Do"}`,
		issueType: "Story",
		workflow:  []string{"To Do"},
		wantFail:  true,
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
		wantFail: true,
	},
	{
		name:         "statuses unreadable",
		mapping:      `{"dispatchable":"To Do"}`,
		issueType:    "Task",
		failStatuses: true,
		wantFail:     true,
	},
}

// A check row is a pure function of what the tracker's check reads return, so
// the Connector reports the same findings as the native adapter when the two
// rows, built from each tracker's resolved capabilities, fail alike.
func TestConnector_DoctorChecksMatchNative(t *testing.T) {
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
				native := nativeJira(h, jira.JiraConfig{StatusMapping: mapping, IssueType: fx.issueType, Labels: testLabels})

				want := doctorFindings(native)
				got := doctorFindings(h.tr)
				if !slices.Equal(got, want) {
					t.Errorf("Connector findings differ from native\n got: %q\nwant: %q", got, want)
				}
				failing := slices.ContainsFunc(want, func(s string) bool { return !strings.HasSuffix(s, ": ok") })
				if !failing && fx.wantFail {
					t.Errorf("fixture %q reports nothing natively: it does not fail", fx.name)
				}
				if failing && !fx.wantFail {
					t.Errorf("fixture %q should pass natively, got %q", fx.name, want)
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
