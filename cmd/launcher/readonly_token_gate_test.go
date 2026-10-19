package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
)

// TestCheckReadOnlyTokenGate pins checkReadOnlyTokenGate's case matrix for
// issue #1950, mirroring the read-only capability-gate test's scenarios.
func TestCheckReadOnlyTokenGate(t *testing.T) {
	cases := []struct {
		name                 string
		access               string
		codeForge            string
		issueTracker         string
		launcherToken        string
		boxToken             string // BOX_GH_TOKEN; empty means unset
		result               backendkit.TokenIntrospection
		introspectErr        error
		expectIntrospectCall bool
		wantErrSubstr        string
		wantErrIs            error // nil means skip the errors.Is check
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
			wantErrIs:            errReadOnlyGateMisconfigured,
		},
		{
			name:                 "Box token equal to Launcher token fails",
			access:               "read-only",
			launcherToken:        "shared-token",
			boxToken:             "shared-token",
			expectIntrospectCall: false,
			wantErrSubstr:        "BOX_GH_TOKEN",
			wantErrIs:            errReadOnlyGateMisconfigured,
		},
		{
			name:                 "distinct non-write-capable token succeeds",
			access:               "read-only",
			launcherToken:        "launcher-token",
			boxToken:             "box-token",
			result:               backendkit.TokenIntrospection{Introspectable: true, WriteCapable: false},
			expectIntrospectCall: true,
			wantVerified:         true,
		},
		{
			name:                 "write-capable introspectable token fails",
			access:               "read-only",
			launcherToken:        "launcher-token",
			boxToken:             "box-token",
			result:               backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true},
			expectIntrospectCall: true,
			wantErrSubstr:        "write",
			wantErrIs:            errReadOnlyGateMisconfigured,
		},
		{
			name:                 "non-introspectable token warns and succeeds",
			access:               "read-only",
			launcherToken:        "launcher-token",
			boxToken:             "box-token",
			result:               backendkit.TokenIntrospection{Introspectable: false},
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
			wantErrIs:            doctor.ErrConnectivity,
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
			introspect := func(token, repoSlug string) (backendkit.TokenIntrospection, error) {
				called = true
				gotToken, gotRepoSlug = token, repoSlug
				return tc.result, tc.introspectErr
			}

			var buf bytes.Buffer
			verified, err := checkReadOnlyTokenGate(c, rowWithIntrospector(t, "github", introspect), &buf)

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
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Errorf("checkReadOnlyTokenGate() error = %v, want errors.Is(err, %v)", err, tc.wantErrIs)
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

// Introspectable is also false for an unrecognized token prefix
// (github.Strategy.IntrospectToken's safe default), not only for a
// fine-grained PAT, so a warning naming "fine-grained PAT" would mislabel
// that case.
func TestCheckReadOnlyTokenGate_NonIntrospectableWarningDoesNotClaimFineGrainedPAT(t *testing.T) {
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "launcher-token"
	t.Setenv("BOX_GH_TOKEN", "box-token")
	introspect := func(token, repoSlug string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{Introspectable: false}, nil
	}

	var buf bytes.Buffer
	if _, err := checkReadOnlyTokenGate(c, rowWithIntrospector(t, "github", introspect), &buf); err != nil {
		t.Fatalf("checkReadOnlyTokenGate() error = %v, want nil", err)
	}
	if strings.Contains(buf.String(), "looks like a fine-grained PAT") {
		t.Errorf("warning asserts a specific token shape it can't actually determine, got %q", buf.String())
	}
}

// Before the fix, checkReadOnlyTokenGate compared c.codeForge to the literal
// string "github" and returned (false, nil), so the gate reported itself
// Applicable yet enforced nothing and accepted a missing BOX_GH_TOKEN. The
// fake "custom-github" row shares backend.GitHub's TokenEnvVar and feeds only
// the tokenGateApplicable assertion (the registry's Applicable); the check
// itself no longer reads backendRows and must still enforce.
func TestCheckReadOnlyTokenGate_AppliesWhenBackendSharesTokenEnvVarUnderDifferentName(t *testing.T) {
	original := backendRows
	backendRows = append(append([]backendRow{}, original...), backendRow{
		Descriptor: backend.Descriptor{
			Name:             "custom-github",
			ValidAsTracker:   true,
			ValidAsCodeForge: true,
			TokenEnvVar:      backend.GitHub.TokenEnvVar,
		},
	})
	defer func() { backendRows = original }()

	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.codeForge = "custom-github"
	c.issueTracker = "local"
	c.repoSlug = "owner/repo"
	t.Setenv("BOX_GH_TOKEN", "")

	if !tokenGateApplicable(c, backend.GitHub) {
		t.Fatal("tokenGateApplicable(c, backend.GitHub) = false, want true: custom-github shares GitHub's TokenEnvVar")
	}

	introspect := func(token, repoSlug string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{}, nil
	}
	var buf bytes.Buffer
	_, err := checkReadOnlyTokenGate(c, rowWithIntrospector(t, "github", introspect), &buf)
	if err == nil {
		t.Fatal("checkReadOnlyTokenGate() error = nil, want a missing-BOX_GH_TOKEN error: the gate must actually enforce when Applicable says it applies, not silently no-op")
	}
	if !errors.Is(err, errReadOnlyGateMisconfigured) {
		t.Errorf("checkReadOnlyTokenGate() error = %v, want errors.Is(err, errReadOnlyGateMisconfigured)", err)
	}
	if !strings.Contains(err.Error(), "BOX_GH_TOKEN") {
		t.Errorf("checkReadOnlyTokenGate() error = %v, want it to mention BOX_GH_TOKEN", err)
	}
}

// withIntrospector returns a copy of rows with name's IntrospectToken hook
// replaced by f; nil models a backend with no hook.
func withIntrospector(rows []backendRow, name string, f backendkit.TokenIntrospector) []backendRow {
	out := append([]backendRow{}, rows...)
	for i := range out {
		if out[i].Name == name {
			out[i].introspectToken = f
		}
	}
	return out
}

// rowWithIntrospector returns the registry row for name with its
// IntrospectToken hook replaced by f.
func rowWithIntrospector(t *testing.T, name string, f backendkit.TokenIntrospector) backendRow {
	t.Helper()
	for _, r := range withIntrospector(backendRows, name, f) {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no backend row %q", name)
	return backendRow{}
}

// swapIntrospector installs f as name's hook in backendRows for the test and
// rebuilds gateRegistry's read-only token gates from the patched rows, since
// each gate captures its row when built. Cleanup rebuilds the token gates from
// the restored rows rather than restoring a captured registry, so it is safe
// whether or not the caller swaps gateRegistry itself, in either order.
func swapIntrospector(t *testing.T, name string, f backendkit.TokenIntrospector) {
	t.Helper()
	original := backendRows
	rebuildTokenGates := func(rows []backendRow) {
		kept := make([]launchGate, 0, len(gateRegistry))
		for _, g := range gateRegistry {
			if !strings.HasPrefix(g.Name, readOnlyTokenGatePrefix) {
				kept = append(kept, g)
			}
		}
		gateRegistry = append(kept, readOnlyTokenGates(rows)...)
	}
	backendRows = withIntrospector(original, name, f)
	rebuildTokenGates(backendRows)
	t.Cleanup(func() {
		backendRows = original
		rebuildTokenGates(original)
	})
}

// Forgejo's Strategy has no IntrospectToken, so a distinct Box token is
// unverifiable: the gate warns and returns verified=false without failing.
func TestCheckReadOnlyTokenGate_Forgejo(t *testing.T) {
	cases := []struct {
		name          string
		access        string
		launcherToken string
		boxToken      string // BOX_FORGEJO_TOKEN; empty means unset
		wantErrSubstr string
		wantWarning   bool
	}{
		{name: "read-write is a no-op", access: "read-write"},
		{name: "unset Box token fails", access: "read-only", wantErrSubstr: "BOX_FORGEJO_TOKEN"},
		{name: "Box token equal to Launcher token fails", access: "read-only", launcherToken: "shared-token", boxToken: "shared-token", wantErrSubstr: "BOX_FORGEJO_TOKEN"},
		{name: "distinct token warns and succeeds unverified", access: "read-only", launcherToken: "launcher-token", boxToken: "box-token", wantWarning: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			c.boxForgeAndIssueAccess = tc.access
			c.setRowKnob("FORGEJO_TOKEN", tc.launcherToken)
			t.Setenv("BOX_FORGEJO_TOKEN", tc.boxToken)

			row, _ := backendByName("forgejo")
			var buf bytes.Buffer
			verified, err := checkReadOnlyTokenGate(c, row, &buf)

			if tc.wantErrSubstr == "" {
				if err != nil {
					t.Errorf("error = %v, want nil", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("error = %v, want it to contain %q", err, tc.wantErrSubstr)
				}
				if !errors.Is(err, errReadOnlyGateMisconfigured) {
					t.Errorf("error = %v, want errors.Is(err, errReadOnlyGateMisconfigured)", err)
				}
			}
			if verified {
				t.Error("verified = true, want false: forgejo has no introspection hook")
			}
			if got := strings.Contains(buf.String(), "WARNING"); got != tc.wantWarning {
				t.Errorf("warning printed = %v, want %v (output: %q)", got, tc.wantWarning, buf.String())
			}
		})
	}
}

// A new backend with a Box token and no IntrospectToken hook gets a warning
// naming the missing hook, never a failure (ADR 0068 decision 6).
func TestCheckReadOnlyTokenGate_StubBackendWithoutHookWarns(t *testing.T) {
	stub := backendRow{Descriptor: backend.Descriptor{
		Name:           "stub",
		DisplayName:    "Stub",
		TokenEnvVar:    "STUB_TOKEN",
		BoxTokenEnvVar: "BOX_STUB_TOKEN",
	}}
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	t.Setenv("BOX_STUB_TOKEN", "box-token")

	var buf bytes.Buffer
	verified, err := checkReadOnlyTokenGate(c, stub, &buf)
	if err != nil {
		t.Fatalf("checkReadOnlyTokenGate() error = %v, want nil: a missing hook warns, never fails", err)
	}
	if verified {
		t.Error("verified = true, want false")
	}
	for _, want := range []string{"WARNING", "Stub", "IntrospectToken", "BOX_STUB_TOKEN"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output = %q, want it to contain %q", buf.String(), want)
		}
	}
}

// A stub backend's token is no launcher knob, so hostToken falls back to the
// environment; without that the identical-token check would compare against ""
// and never fire.
func TestCheckReadOnlyTokenGate_StubBackendIdenticalTokenFails(t *testing.T) {
	stub := backendRow{Descriptor: backend.Descriptor{
		Name:           "stub",
		DisplayName:    "Stub",
		TokenEnvVar:    "STUB_TOKEN",
		BoxTokenEnvVar: "BOX_STUB_TOKEN",
	}}
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	t.Setenv("STUB_TOKEN", "same-token")
	t.Setenv("BOX_STUB_TOKEN", "same-token")

	_, err := checkReadOnlyTokenGate(c, stub, &bytes.Buffer{})
	if !errors.Is(err, errReadOnlyGateMisconfigured) {
		t.Fatalf("checkReadOnlyTokenGate() error = %v, want errors.Is(err, errReadOnlyGateMisconfigured)", err)
	}
}

// The parenthetical in the non-introspectable warning is the hook's own
// Reason; a hook that supplies none gets no parenthetical at all.
func TestCheckReadOnlyTokenGate_NonIntrospectableWarningUsesHookReason(t *testing.T) {
	cases := []struct {
		name, reason, want string
	}{
		{"empty reason omits parenthetical", "", "WARNING: BOX_GH_TOKEN's write capability could not be determined. read-only trusts"},
		{"reason is parenthesised", "token is opaque", "WARNING: BOX_GH_TOKEN's write capability could not be determined (token is opaque). read-only trusts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			c.boxForgeAndIssueAccess = "read-only"
			c.ghToken = "launcher-token"
			t.Setenv("BOX_GH_TOKEN", "box-token")
			introspect := func(string, string) (backendkit.TokenIntrospection, error) {
				return backendkit.TokenIntrospection{Introspectable: false, Reason: tc.reason}, nil
			}

			var buf bytes.Buffer
			if _, err := checkReadOnlyTokenGate(c, rowWithIntrospector(t, "github", introspect), &buf); err != nil {
				t.Fatalf("checkReadOnlyTokenGate() error = %v, want nil", err)
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("warning = %q, want it to contain %q", buf.String(), tc.want)
			}
		})
	}
}

// Pins the wiring between the production GitHub Strategy's IntrospectToken
// hook and the gate: it uses the real row, so deleting the hook from
// internal/forge/github/strategy.go fails here and nowhere else.
func TestCheckReadOnlyTokenGate_ProductionGitHubHookWarnsOnFineGrainedPAT(t *testing.T) {
	row, ok := backendByName("github")
	if !ok {
		t.Fatal("backendByName(\"github\") ok = false, want true")
	}
	if row.introspectToken == nil {
		t.Fatal("github row introspectToken = nil, want the Strategy's IntrospectToken hook")
	}
	c := minimalValidConfig()
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "launcher-token"
	t.Setenv("BOX_GH_TOKEN", "github_pat_boxtoken")

	var buf bytes.Buffer
	verified, err := checkReadOnlyTokenGate(c, row, &buf)
	if err != nil {
		t.Fatalf("checkReadOnlyTokenGate() error = %v, want nil", err)
	}
	if verified {
		t.Error("verified = true, want false for a fine-grained PAT")
	}
	out := buf.String()
	if !strings.Contains(out, "fine-grained PAT") {
		t.Errorf("output = %q, want it to carry the production hook's fine-grained-PAT reason", out)
	}
	if strings.Contains(out, "no IntrospectToken hook") {
		t.Errorf("output = %q, must not take the no-hook branch", out)
	}
}
