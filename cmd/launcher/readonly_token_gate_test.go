package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestCheckReadOnlyTokenGate table-drives checkReadOnlyTokenGate's cases
// (issue #1950), mirroring the existing read-only capability-gate test's
// scenario set: read-write no-op, unset Box token, Box token equal to the
// Launcher's, a distinct non-write-capable token, a write-capable
// introspectable token, and a non-introspectable (fine-grained PAT) token.
func TestCheckReadOnlyTokenGate(t *testing.T) {
	cases := []struct {
		name                 string
		access               string
		codeForge            string
		issueTracker         string
		launcherToken        string
		boxToken             string // BOX_GH_TOKEN; empty means unset
		result               tokenIntrospectionResult
		introspectErr        error
		expectIntrospectCall bool
		wantErrSubstr        string
		wantVerified         bool
		wantWarning          bool
	}{
		{
			name:                 "read-write is a no-op",
			access:               "read-write",
			expectIntrospectCall: false,
		},
		{
			name:                 "unset Box token fails",
			access:               "read-only",
			boxToken:             "",
			expectIntrospectCall: false,
			wantErrSubstr:        "BOX_GH_TOKEN",
		},
		{
			name:                 "Box token equal to Launcher token fails",
			access:               "read-only",
			launcherToken:        "shared-token",
			boxToken:             "shared-token",
			expectIntrospectCall: false,
			wantErrSubstr:        "BOX_GH_TOKEN",
		},
		{
			name:                 "distinct non-write-capable token succeeds",
			access:               "read-only",
			launcherToken:        "launcher-token",
			boxToken:             "box-token",
			result:               tokenIntrospectionResult{Introspectable: true, WriteCapable: false},
			expectIntrospectCall: true,
			wantVerified:         true,
		},
		{
			name:                 "write-capable introspectable token fails",
			access:               "read-only",
			launcherToken:        "launcher-token",
			boxToken:             "box-token",
			result:               tokenIntrospectionResult{Introspectable: true, WriteCapable: true},
			expectIntrospectCall: true,
			wantErrSubstr:        "write",
		},
		{
			name:                 "non-introspectable token warns and succeeds",
			access:               "read-only",
			launcherToken:        "launcher-token",
			boxToken:             "box-token",
			result:               tokenIntrospectionResult{Introspectable: false},
			expectIntrospectCall: true,
			wantWarning:          true,
		},
		{
			name:                 "introspect error aborts startup",
			access:               "read-only",
			launcherToken:        "launcher-token",
			boxToken:             "box-token",
			introspectErr:        errors.New("gh api -i user: exit status 1"),
			expectIntrospectCall: true,
			wantErrSubstr:        "introspecting BOX_GH_TOKEN failed",
		},
		{
			name:                 "read-only pure-forgejo deployment is a no-op",
			access:               "read-only",
			codeForge:            "forgejo",
			issueTracker:         "forgejo",
			expectIntrospectCall: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			c.boxForgeAndIssueAccess = tc.access
			c.ghToken = tc.launcherToken
			c.repoSlug = "owner/repo"
			if tc.codeForge != "" {
				c.codeForge = tc.codeForge
			}
			if tc.issueTracker != "" {
				c.issueTracker = tc.issueTracker
			}
			t.Setenv("BOX_GH_TOKEN", tc.boxToken)

			called := false
			var gotToken, gotRepoSlug string
			introspect := func(token, repoSlug string) (tokenIntrospectionResult, error) {
				called = true
				gotToken, gotRepoSlug = token, repoSlug
				return tc.result, tc.introspectErr
			}

			var buf bytes.Buffer
			verified, err := checkReadOnlyTokenGate(c, introspect, &buf)

			if called != tc.expectIntrospectCall {
				t.Errorf("introspect called = %v, want %v", called, tc.expectIntrospectCall)
			}
			if tc.wantErrSubstr == "" {
				if err != nil {
					t.Errorf("checkReadOnlyTokenGate() error = %v, want nil", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("checkReadOnlyTokenGate() error = %v, want it to contain %q", err, tc.wantErrSubstr)
				}
			}
			if verified != tc.wantVerified {
				t.Errorf("checkReadOnlyTokenGate() verified = %v, want %v", verified, tc.wantVerified)
			}
			gotWarning := strings.Contains(strings.ToUpper(buf.String()), "WARNING")
			if gotWarning != tc.wantWarning {
				t.Errorf("warning printed = %v, want %v (output: %q)", gotWarning, tc.wantWarning, buf.String())
			}
			if tc.expectIntrospectCall && (gotToken != tc.boxToken || gotRepoSlug != c.repoSlug) {
				t.Errorf("introspect(%q, %q), want (%q, %q)", gotToken, gotRepoSlug, tc.boxToken, c.repoSlug)
			}
		})
	}
}

// TestCheckReadOnlyTokenGate_NonIntrospectableWarningDoesNotClaimFineGrainedPAT
// verifies the non-introspectable warning doesn't assert a specific token
// shape: Introspectable is also false for an unrecognized/unknown-prefix
// token (ghTokenIntrospector's safe default), not only a fine-grained PAT, so
// a warning that names "fine-grained PAT" specifically would mislabel that
// case.
func TestCheckReadOnlyTokenGate_NonIntrospectableWarningDoesNotClaimFineGrainedPAT(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "launcher-token"
	t.Setenv("BOX_GH_TOKEN", "box-token")
	introspect := func(token, repoSlug string) (tokenIntrospectionResult, error) {
		return tokenIntrospectionResult{Introspectable: false}, nil
	}

	var buf bytes.Buffer
	if _, err := checkReadOnlyTokenGate(c, introspect, &buf); err != nil {
		t.Fatalf("checkReadOnlyTokenGate() error = %v, want nil", err)
	}
	if strings.Contains(buf.String(), "looks like a fine-grained PAT") {
		t.Errorf("warning asserts a specific token shape it can't actually determine, got %q", buf.String())
	}
}
