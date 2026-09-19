package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
)

// This mirrors TestCheckReadOnlyTokenGate's scenario set for Forgejo, which
// exposes no token-introspection endpoint, so a distinct token is
// unverifiable and the gate always warns and returns verified=false.
func TestCheckReadOnlyForgejoTokenGate(t *testing.T) {
	cases := []struct {
		name          string
		access        string
		codeForge     string
		issueTracker  string
		launcherToken string
		boxToken      string // BOX_FORGEJO_TOKEN; empty means unset
		wantErrSubstr string
		wantErrIs     error // nil means skip the errors.Is check
		wantWarning   bool
	}{
		{
			name:   "read-write is a no-op",
			access: "read-write",
		},
		{
			name:         "read-only but neither backend is forgejo is a no-op",
			access:       "read-only",
			codeForge:    "github",
			issueTracker: "github",
		},
		{
			name:          "unset Box token fails",
			access:        "read-only",
			codeForge:     "forgejo",
			boxToken:      "",
			wantErrSubstr: "BOX_FORGEJO_TOKEN",
			wantErrIs:     errReadOnlyGateMisconfigured,
		},
		{
			name:          "Box token equal to Launcher token fails",
			access:        "read-only",
			codeForge:     "forgejo",
			launcherToken: "shared-token",
			boxToken:      "shared-token",
			wantErrSubstr: "BOX_FORGEJO_TOKEN",
			wantErrIs:     errReadOnlyGateMisconfigured,
		},
		{
			name:          "distinct token warns and succeeds unverified",
			access:        "read-only",
			codeForge:     "forgejo",
			launcherToken: "launcher-token",
			boxToken:      "box-token",
			wantWarning:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			c.boxForgeAndIssueAccess = tc.access
			if tc.codeForge != "" {
				c.codeForge = tc.codeForge
			}
			if tc.issueTracker != "" {
				c.issueTracker = tc.issueTracker
			}
			c.forgejoToken = tc.launcherToken
			t.Setenv("BOX_FORGEJO_TOKEN", tc.boxToken)

			var buf bytes.Buffer
			verified, err := checkReadOnlyForgejoTokenGate(c, &buf)

			if tc.wantErrSubstr == "" {
				if err != nil {
					t.Errorf("checkReadOnlyForgejoTokenGate() error = %v, want nil", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("checkReadOnlyForgejoTokenGate() error = %v, want it to contain %q", err, tc.wantErrSubstr)
				}
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Errorf("checkReadOnlyForgejoTokenGate() error = %v, want errors.Is(err, %v)", err, tc.wantErrIs)
			}
			if verified {
				t.Errorf("checkReadOnlyForgejoTokenGate() verified = true, want always false (Forgejo has no introspection endpoint)")
			}
			gotWarning := strings.Contains(strings.ToUpper(buf.String()), "WARNING")
			if gotWarning != tc.wantWarning {
				t.Errorf("warning printed = %v, want %v (output: %q)", gotWarning, tc.wantWarning, buf.String())
			}
		})
	}
}

// This is the Forgejo counterpart to
// TestCheckReadOnlyTokenGate_AppliesWhenBackendSharesTokenEnvVarUnderDifferentName.
// The gate used to compare c.codeForge to the literal "forgejo", so it no-opped
// on a backend that shares Forgejo's TokenEnvVar under another name even though
// gateRegistry reported the gate as applicable.
func TestCheckReadOnlyForgejoTokenGate_AppliesWhenBackendSharesTokenEnvVarUnderDifferentName(t *testing.T) {
	original := backendRows
	backendRows = append(append([]backendRow{}, original...), backendRow{
		Descriptor: backend.Descriptor{
			Name:             "custom-forgejo",
			ValidAsTracker:   true,
			ValidAsCodeForge: true,
			TokenEnvVar:      backend.Forgejo.TokenEnvVar,
		},
	})
	defer func() { backendRows = original }()

	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.codeForge = "custom-forgejo"
	c.issueTracker = "local"
	t.Setenv("BOX_FORGEJO_TOKEN", "")

	if !tokenGateApplicable(c, backend.Forgejo) {
		t.Fatal("tokenGateApplicable(c, backend.Forgejo) = false, want true: custom-forgejo shares Forgejo's TokenEnvVar")
	}

	var buf bytes.Buffer
	_, err := checkReadOnlyForgejoTokenGate(c, &buf)
	if err == nil {
		t.Fatal("checkReadOnlyForgejoTokenGate() error = nil, want a missing-BOX_FORGEJO_TOKEN error: the gate must actually enforce when Applicable says it applies, not silently no-op")
	}
	if !errors.Is(err, errReadOnlyGateMisconfigured) {
		t.Errorf("checkReadOnlyForgejoTokenGate() error = %v, want errors.Is(err, errReadOnlyGateMisconfigured)", err)
	}
	if !strings.Contains(err.Error(), "BOX_FORGEJO_TOKEN") {
		t.Errorf("checkReadOnlyForgejoTokenGate() error = %v, want it to mention BOX_FORGEJO_TOKEN", err)
	}
}
