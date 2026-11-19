package jira_test

import (
	"encoding/json"
	"maps"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

// An empty mapping is valid, not an error: every state falls back to its label.
func TestParseStatusMapping_Empty(t *testing.T) {
	m, err := jira.ParseStatusMapping("")
	if err != nil {
		t.Fatalf("ParseStatusMapping(\"\"): %v", err)
	}
	if len(m) != 0 {
		t.Errorf("want empty mapping, got %v", m)
	}
}

func TestParseStatusMapping_AllStates(t *testing.T) {
	m, err := jira.ParseStatusMapping(`{"dispatchable":"To Do","inProgress":"In Progress","complete":"Done","failed":"Blocked"}`)
	if err != nil {
		t.Fatalf("ParseStatusMapping: %v", err)
	}
	want := map[forge.DispatchState]jira.StatusTarget{
		forge.Dispatchable: {Status: "To Do"},
		forge.InProgress:   {Status: "In Progress"},
		forge.Complete:     {Status: "Done"},
		forge.Failed:       {Status: "Blocked"},
	}
	if !maps.Equal(m, want) {
		t.Errorf("mapping = %v, want %v", m, want)
	}
}

// A typo'd key must fail fast at startup, not be silently dropped.
func TestParseStatusMapping_UnknownKey(t *testing.T) {
	if _, err := jira.ParseStatusMapping(`{"disptchable":"To Do"}`); err == nil {
		t.Fatal("want error for unknown key, got nil")
	}
}

func TestParseStatusMapping_AmbiguousKey(t *testing.T) {
	m, err := jira.ParseStatusMapping(`{"ambiguous":"Needs Spec"}`)
	if err != nil {
		t.Fatalf("ParseStatusMapping: %v", err)
	}
	if m[forge.Ambiguous].Status != "Needs Spec" {
		t.Errorf("m[Ambiguous] = %q, want %q", m[forge.Ambiguous].Status, "Needs Spec")
	}
}

func TestParseStatusMapping_UnknownKeyListsAmbiguous(t *testing.T) {
	_, err := jira.ParseStatusMapping(`{"disptchable":"To Do"}`)
	if err == nil || !strings.Contains(err.Error(), "(want one of dispatchable, inProgress, complete, failed, ambiguous)") {
		t.Fatalf("err = %v, want the key list ending in ambiguous", err)
	}
}

// failed and ambiguous both mean a human must act, so they may share one
// column, case-insensitively.
func TestParseStatusMapping_FailedAndAmbiguousMayShare(t *testing.T) {
	for _, s := range []string{
		`{"failed":"Blocked","ambiguous":"Blocked"}`,
		`{"failed":"Blocked","ambiguous":"blocked"}`,
	} {
		m, err := jira.ParseStatusMapping(s)
		if err != nil {
			t.Fatalf("ParseStatusMapping(%s): %v", s, err)
		}
		if m[forge.Failed].Status == "" || m[forge.Ambiguous].Status == "" {
			t.Errorf("ParseStatusMapping(%s) = %v, want both mapped", s, m)
		}
	}
}

func TestParseStatusMapping_OtherSharesRejected(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			"case-differing dispatchable and inProgress",
			`{"dispatchable":"To Do","inProgress":"to do"}`,
			`JIRA_STATUS_MAPPING: keys "dispatchable" and "inProgress" both map to status "To Do" (only failed and ambiguous may share a status)`,
		},
		{
			"non-ASCII case-only share",
			`{"dispatchable":"Ärger","inProgress":"ärger"}`,
			`JIRA_STATUS_MAPPING: keys "dispatchable" and "inProgress" both map to status "Ärger" (only failed and ambiguous may share a status)`,
		},
		{
			"complete and failed",
			`{"failed":"Done","complete":"Done"}`,
			`JIRA_STATUS_MAPPING: keys "complete" and "failed" both map to status "Done" (only failed and ambiguous may share a status)`,
		},
		{
			"three-way ambiguous failed complete",
			`{"ambiguous":"Blocked","failed":"Blocked","complete":"Blocked"}`,
			`JIRA_STATUS_MAPPING: keys "ambiguous" and "complete" both map to status "Blocked" (only failed and ambiguous may share a status)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := jira.ParseStatusMapping(tc.in)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// An empty (or null) status is unmapped, so two of them never collide.
func TestParseStatusMapping_EmptyStatusesDoNotCollide(t *testing.T) {
	for _, s := range []string{
		`{"dispatchable":"","inProgress":""}`,
		`{"dispatchable":null,"complete":null}`,
	} {
		if _, err := jira.ParseStatusMapping(s); err != nil {
			t.Errorf("ParseStatusMapping(%s): %v", s, err)
		}
	}
}

func TestParseStatusMapping_InvalidJSON(t *testing.T) {
	if _, err := jira.ParseStatusMapping(`{not json`); err == nil {
		t.Fatal("want error for invalid JSON, got nil")
	}
}

// The corpus is shared with the Nix mirror of ParseStatusMapping, so both
// parsers are held to the same accept/reject decisions and error text.
func TestParseStatusMapping_Corpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/status-mapping-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string
		Input string
		Error string
		Want  map[string]jira.StatusTarget
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	keys := map[string]forge.DispatchState{
		"dispatchable": forge.Dispatchable, "inProgress": forge.InProgress,
		"complete": forge.Complete, "failed": forge.Failed, "ambiguous": forge.Ambiguous,
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := jira.ParseStatusMapping(tc.Input)
			if tc.Error != "" {
				if err == nil || err.Error() != tc.Error {
					t.Fatalf("error = %v, want %q", err, tc.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := map[forge.DispatchState]jira.StatusTarget{}
			for k, v := range tc.Want {
				want[keys[k]] = v
			}
			if !maps.Equal(got, want) {
				t.Errorf("mapping = %v, want %v", got, want)
			}
		})
	}
}

// The lifecycle corpus is shared with lib/jira-status-mapping.nix's
// lifecycleError, so eval and launcher startup word the same errors.
func TestCheckLifecycle_Corpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/lifecycle-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name      string
		Lifecycle string
		Mapping   string
		Error     string
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			mapping, err := jira.ParseStatusMapping(tc.Mapping)
			if err != nil {
				t.Fatal(err)
			}
			err = jira.CheckLifecycle(tc.Lifecycle, mapping)
			if tc.Error == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.Error {
				t.Fatalf("error = %v, want %q", err, tc.Error)
			}
		})
	}
}

func TestParseStatusMapping_InvalidJSONMessage(t *testing.T) {
	_, err := jira.ParseStatusMapping("{not json")
	if err == nil || !strings.HasPrefix(err.Error(), "parse JIRA_STATUS_MAPPING: ") {
		t.Fatalf("error = %v, want a parse JIRA_STATUS_MAPPING: prefix", err)
	}
}
