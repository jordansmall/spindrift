package main

import "testing"

// TestBackendRowsShape asserts the backendRows registry reproduces today's
// hardcoded per-axis switch behavior for each of the 5 backends (issue
// #2267 slice 1: purely additive, nothing calls into this registry yet).
func TestBackendRowsShape(t *testing.T) {
	if len(backendRows) != 5 {
		t.Fatalf("len(backendRows) = %d, want 5", len(backendRows))
	}

	cases := []struct {
		name                    string
		validAsTracker          bool
		validAsCodeForge        bool
		tokenEnvVar             string
		boxTokenEnvVar          string
		doctorTokenHint         string
		doctorSlugHint          string
		hostMediatedRemote      bool
		outboxRelayCapable      bool
		inBoxUnreachableTracker bool

		hasValidateTracker      bool
		hasValidateCodeForge    bool
		hasNewIssueTracker      bool
		hasNewCodeForge         bool
		hasNewReadOnlyCodeForge bool
		hasReadOnlyTokenGate    bool
		hasReadOnlyGateOkMsg    bool
	}{
		{
			name:               "github",
			validAsTracker:     true,
			validAsCodeForge:   true,
			tokenEnvVar:        "GH_TOKEN",
			boxTokenEnvVar:     "BOX_GH_TOKEN",
			outboxRelayCapable: true,

			hasNewIssueTracker:      true,
			hasNewCodeForge:         true,
			hasNewReadOnlyCodeForge: true,
			hasReadOnlyTokenGate:    true,
			hasReadOnlyGateOkMsg:    true,
		},
		{
			name:             "forgejo",
			validAsTracker:   true,
			validAsCodeForge: true,
			tokenEnvVar:      "FORGEJO_TOKEN",
			boxTokenEnvVar:   "BOX_FORGEJO_TOKEN",
			doctorTokenHint:  "FORGEJO_TOKEN",
			doctorSlugHint:   "FORGEJO_BASE_URL",

			hasValidateTracker:      true,
			hasValidateCodeForge:    true,
			hasNewIssueTracker:      true,
			hasNewCodeForge:         true,
			hasNewReadOnlyCodeForge: true,
			hasReadOnlyTokenGate:    true,
			hasReadOnlyGateOkMsg:    true,
		},
		{
			name:             "jira",
			validAsTracker:   true,
			validAsCodeForge: false,
			tokenEnvVar:      "JIRA_TOKEN",
			boxTokenEnvVar:   "",
			doctorTokenHint:  "JIRA_TOKEN",
			doctorSlugHint:   "JIRA_BASE_URL / JIRA_PROJECT_KEY",

			hasValidateTracker: true,
			hasNewIssueTracker: true,
		},
		{
			name:                    "local",
			validAsTracker:          true,
			validAsCodeForge:        true,
			hostMediatedRemote:      true,
			inBoxUnreachableTracker: true,

			hasValidateCodeForge: true,
			hasNewIssueTracker:   true,
			hasNewCodeForge:      true,
		},
		{
			name:             "git",
			validAsTracker:   false,
			validAsCodeForge: true,

			hasValidateCodeForge: true,
			hasNewCodeForge:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row, ok := backendByName(tc.name)
			if !ok {
				t.Fatalf("backendByName(%q) ok=false, want true", tc.name)
			}
			if row.Name != tc.name {
				t.Errorf("name = %q, want %q", row.Name, tc.name)
			}
			if row.ValidAsTracker != tc.validAsTracker {
				t.Errorf("validAsTracker = %v, want %v", row.ValidAsTracker, tc.validAsTracker)
			}
			if row.ValidAsCodeForge != tc.validAsCodeForge {
				t.Errorf("validAsCodeForge = %v, want %v", row.ValidAsCodeForge, tc.validAsCodeForge)
			}
			if row.TokenEnvVar != tc.tokenEnvVar {
				t.Errorf("tokenEnvVar = %q, want %q", row.TokenEnvVar, tc.tokenEnvVar)
			}
			if row.boxTokenEnvVar != tc.boxTokenEnvVar {
				t.Errorf("boxTokenEnvVar = %q, want %q", row.boxTokenEnvVar, tc.boxTokenEnvVar)
			}
			if row.DoctorTokenHint != tc.doctorTokenHint {
				t.Errorf("doctorTokenHint = %q, want %q", row.DoctorTokenHint, tc.doctorTokenHint)
			}
			if row.DoctorSlugHint != tc.doctorSlugHint {
				t.Errorf("doctorSlugHint = %q, want %q", row.DoctorSlugHint, tc.doctorSlugHint)
			}
			if row.HostMediatedRemote != tc.hostMediatedRemote {
				t.Errorf("hostMediatedRemote = %v, want %v", row.HostMediatedRemote, tc.hostMediatedRemote)
			}
			if row.outboxRelayCapable != tc.outboxRelayCapable {
				t.Errorf("outboxRelayCapable = %v, want %v", row.outboxRelayCapable, tc.outboxRelayCapable)
			}
			if row.inBoxUnreachableTracker != tc.inBoxUnreachableTracker {
				t.Errorf("inBoxUnreachableTracker = %v, want %v", row.inBoxUnreachableTracker, tc.inBoxUnreachableTracker)
			}

			if (row.validateTracker != nil) != tc.hasValidateTracker {
				t.Errorf("validateTracker present = %v, want %v", row.validateTracker != nil, tc.hasValidateTracker)
			}
			if (row.validateCodeForge != nil) != tc.hasValidateCodeForge {
				t.Errorf("validateCodeForge present = %v, want %v", row.validateCodeForge != nil, tc.hasValidateCodeForge)
			}
			if (row.newIssueTracker != nil) != tc.hasNewIssueTracker {
				t.Errorf("newIssueTracker present = %v, want %v", row.newIssueTracker != nil, tc.hasNewIssueTracker)
			}
			if (row.newCodeForge != nil) != tc.hasNewCodeForge {
				t.Errorf("newCodeForge present = %v, want %v", row.newCodeForge != nil, tc.hasNewCodeForge)
			}
			if (row.newReadOnlyCodeForge != nil) != tc.hasNewReadOnlyCodeForge {
				t.Errorf("newReadOnlyCodeForge present = %v, want %v", row.newReadOnlyCodeForge != nil, tc.hasNewReadOnlyCodeForge)
			}
			if (row.readOnlyTokenGate != nil) != tc.hasReadOnlyTokenGate {
				t.Errorf("readOnlyTokenGate present = %v, want %v", row.readOnlyTokenGate != nil, tc.hasReadOnlyTokenGate)
			}
			if (row.readOnlyGateOkMessage != nil) != tc.hasReadOnlyGateOkMsg {
				t.Errorf("readOnlyGateOkMessage present = %v, want %v", row.readOnlyGateOkMessage != nil, tc.hasReadOnlyGateOkMsg)
			}
		})
	}
}

// TestBackendByNameUnknown asserts an unregistered name returns ok=false.
func TestBackendByNameUnknown(t *testing.T) {
	if _, ok := backendByName("nonexistent"); ok {
		t.Fatalf("backendByName(%q) ok=true, want false", "nonexistent")
	}
}

// TestJiraNewIssueTrackerMalformedStatusMapping mirrors the existing
// fallback-to-empty-map behavior main.go's newIssueTracker "jira" case has
// today: a malformed JIRA_STATUS_MAPPING must not panic, and must still
// yield a non-nil tracker.
func TestJiraNewIssueTrackerMalformedStatusMapping(t *testing.T) {
	row, ok := backendByName("jira")
	if !ok {
		t.Fatal("backendByName(\"jira\") ok=false")
	}
	if row.newIssueTracker == nil {
		t.Fatal("jira row.newIssueTracker is nil")
	}
	c := config{schemaConfig: schemaConfig{
		jiraBaseURL:       "https://example.atlassian.net",
		jiraProjectKey:    "PROJ",
		jiraToken:         "tok",
		jiraStatusMapping: "{not valid json",
	}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("newIssueTracker panicked: %v", r)
		}
	}()
	tracker := row.newIssueTracker(c)
	if tracker == nil {
		t.Fatal("newIssueTracker returned nil tracker")
	}
}
