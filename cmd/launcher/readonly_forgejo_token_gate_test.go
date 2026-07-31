package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestCheckReadOnlyForgejoTokenGate table-drives checkReadOnlyForgejoTokenGate's
// cases, mirroring TestCheckReadOnlyTokenGate's scenario set adapted for
// Forgejo, which exposes no token-introspection endpoint: read-write no-op,
// read-only but neither backend is forgejo (no-op), unset Box token, Box
// token equal to the Launcher's, and a distinct token (unverifiable, so
// always warns and returns verified=false).
func TestCheckReadOnlyForgejoTokenGate(t *testing.T) {
	cases := []struct {
		name          string
		access        string
		codeForge     string
		issueTracker  string
		launcherToken string
		boxToken      string // BOX_FORGEJO_TOKEN; empty means unset
		wantErrSubstr string
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
		},
		{
			name:          "Box token equal to Launcher token fails",
			access:        "read-only",
			codeForge:     "forgejo",
			launcherToken: "shared-token",
			boxToken:      "shared-token",
			wantErrSubstr: "BOX_FORGEJO_TOKEN",
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
