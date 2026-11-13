package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
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
			c.BoxForgeAndIssueAccess = tc.access
			c.GhToken = tc.launcherToken
			c.RepoSlug = "owner/repo"
			if tc.codeForge != "" {
				c.CodeForge = tc.codeForge
			}
			if tc.issueTracker != "" {
				c.IssueTracker = tc.issueTracker
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
			if tc.expectIntrospectCall && (gotToken != tc.boxToken || gotRepoSlug != c.RepoSlug) {
				t.Errorf("introspect(%q, %q), want (%q, %q)", gotToken, gotRepoSlug, tc.boxToken, c.RepoSlug)
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
	c.BoxForgeAndIssueAccess = "read-only"
	c.GhToken = "launcher-token"
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

// Before the fix, checkReadOnlyTokenGate compared c.CodeForge to the literal
// string "github" and returned (false, nil), so the gate reported itself
// Applicable yet enforced nothing and accepted a missing BOX_GH_TOKEN. The
// fake "custom-github" row shares backend.GitHub's TokenEnvVar and feeds only
// the tokenGateApplicable assertion (the registry's Applicable); the check
// itself no longer reads the registry and must still enforce.
func TestCheckReadOnlyTokenGate_AppliesWhenBackendSharesTokenEnvVarUnderDifferentName(t *testing.T) {
	withExtraStrategies(t, backendkit.Strategy{
		Descriptor: backend.Descriptor{
			Name:             "custom-github",
			ValidAsTracker:   true,
			ValidAsCodeForge: true,
			TokenEnvVar:      backend.GitHub.TokenEnvVar,
		},
	})

	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-only"
	c.CodeForge = "custom-github"
	c.IssueTracker = "local"
	c.RepoSlug = "owner/repo"
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

// withIntrospector returns a copy of ss with name's IntrospectToken hook
// replaced by f; nil models a backend with no hook.
func withIntrospector(ss []backendkit.Strategy, name string, f backendkit.TokenIntrospector) []backendkit.Strategy {
	out := append([]backendkit.Strategy{}, ss...)
	for i := range out {
		if out[i].Name == name {
			out[i].IntrospectToken = f
		}
	}
	return out
}

// rowWithIntrospector returns the registered Strategy for name with its
// IntrospectToken hook replaced by f.
func rowWithIntrospector(t *testing.T, name string, f backendkit.TokenIntrospector) backendkit.Strategy {
	t.Helper()
	for _, s := range withIntrospector(backendkit.Strategies(), name, f) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no backend row %q", name)
	return backendkit.Strategy{}
}

// swapIntrospector installs f as name's hook in the registry for the test and
// rebuilds gateRegistry's read-only token gates from the patched Strategies,
// since each gate captures its Strategy when built. Cleanup rebuilds the token
// gates from the restored registry rather than restoring a captured gateRegistry,
// so it is safe whether or not the caller swaps gateRegistry itself, in either
// order.
func swapIntrospector(t *testing.T, name string, f backendkit.TokenIntrospector) {
	t.Helper()
	restore := backendkit.ReplaceRegistryForTest(withIntrospector(backendkit.Strategies(), name, f))
	rebuildTokenGates()
	t.Cleanup(func() {
		restore()
		rebuildTokenGates()
	})
}

// rebuildTokenGates rebuilds gateRegistry's read-only token gates from the
// registry as it stands.
func rebuildTokenGates() {
	kept := make([]launchGate, 0, len(gateRegistry))
	for _, g := range gateRegistry {
		if !strings.HasPrefix(g.Name, readOnlyTokenGatePrefix) {
			kept = append(kept, g)
		}
	}
	gateRegistry = append(kept, readOnlyTokenGates(backendkit.Strategies())...)
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
			c.BoxForgeAndIssueAccess = tc.access
			c.setRowKnob("FORGEJO_TOKEN", tc.launcherToken)
			t.Setenv("BOX_FORGEJO_TOKEN", tc.boxToken)

			row, _ := backendkit.ByName("forgejo")
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
	stub := backendkit.Strategy{Descriptor: backend.Descriptor{
		Name:           "stub",
		DisplayName:    "Stub",
		TokenEnvVar:    "STUB_TOKEN",
		BoxTokenEnvVar: "BOX_STUB_TOKEN",
	}}
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-only"
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

// A stub backend's token is no launcher knob, so hostTokenValue falls back to
// the environment; without that the identical-token check would compare
// against "" and never fire.
func TestCheckReadOnlyTokenGate_StubBackendIdenticalTokenFails(t *testing.T) {
	stub := backendkit.Strategy{Descriptor: backend.Descriptor{
		Name:           "stub",
		DisplayName:    "Stub",
		TokenEnvVar:    "STUB_TOKEN",
		BoxTokenEnvVar: "BOX_STUB_TOKEN",
	}}
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-only"
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
			c.BoxForgeAndIssueAccess = "read-only"
			c.GhToken = "launcher-token"
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
// internal/forge/github/strategy.go fails here and nowhere else. No github
// Connector row is applied, so the hook answers natively.
func TestCheckReadOnlyTokenGate_ProductionGitHubHookWarnsOnFineGrainedPAT(t *testing.T) {
	t.Cleanup(backendrows.Snapshot())
	if err := backendrows.Use(nil, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	row, ok := backendkit.ByName("github")
	if !ok {
		t.Fatal("backendkit.ByName(\"github\") ok = false, want true")
	}
	if row.IntrospectToken == nil {
		t.Fatal("github row introspectToken = nil, want the Strategy's IntrospectToken hook")
	}
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-only"
	c.GhToken = "launcher-token"
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

// A backend whose Connector cannot introspect a token leaves read-only
// unverified, with the same trust notice as a non-introspectable token. The
// error wraps backendkit.ErrUnsupported alone: it is no connectivity failure.
func TestCheckReadOnlyTokenGate_UnsupportedIntrospectionIsUnverifiedNotAConnectivityFailure(t *testing.T) {
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-only"
	c.GhToken = "launcher-token"
	t.Setenv("BOX_GH_TOKEN", "box-token")
	introspect := func(string, string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{}, fmt.Errorf("no github connector is configured: %w", backendkit.ErrUnsupported)
	}

	var buf bytes.Buffer
	verified, err := checkReadOnlyTokenGate(c, rowWithIntrospector(t, "github", introspect), &buf)

	if verified {
		t.Error("verified = true, want false")
	}
	if !errors.Is(err, backendkit.ErrUnsupported) {
		t.Fatalf("err = %v, want it to wrap backendkit.ErrUnsupported", err)
	}
	if errors.Is(err, doctor.ErrConnectivity) || errors.Is(err, errReadOnlyGateMisconfigured) {
		t.Errorf("err = %v, must be neither a connectivity nor a misconfiguration failure", err)
	}
	if !strings.Contains(buf.String(), "WARNING: BOX_GH_TOKEN's write capability could not be determined") || !strings.Contains(buf.String(), readOnlyTrustNotice) {
		t.Errorf("warning = %q, want the not-determinable warning ending in the trust notice", buf.String())
	}
}

// doctor's walk reports an unsupported gate as such, neither failing nor
// stopping later gates.
func TestWalkGateRegistry_UnsupportedGateReportsAndDoesNotBlock(t *testing.T) {
	var ranAfter bool
	registry := []launchGate{
		{Name: "read-only-token-github", Network: true, Check: func(config, io.Writer) error {
			return fmt.Errorf("introspecting BOX_GH_TOKEN: no github connector is configured: %w", backendkit.ErrUnsupported)
		}},
		{Name: "after", Network: true, Check: func(config, io.Writer) error { ranAfter = true; return nil }},
	}

	var buf bytes.Buffer
	if err := walkGateRegistry(registry, config{}, &buf, doctor.NewReporter(&buf, true), walkReport); err != nil {
		t.Fatalf("walkGateRegistry() = %v, want nil: an unsupported gate must not fail doctor", err)
	}
	if want := "unsupported: read-only-token-github (introspecting BOX_GH_TOKEN: no github connector is configured)\n"; !strings.Contains(buf.String(), want) {
		t.Errorf("output = %q, want it to contain %q", buf.String(), want)
	}
	if strings.Contains(buf.String(), "MISSING") {
		t.Errorf("output = %q, must not report a failure", buf.String())
	}
	if !ranAfter {
		t.Error("a later gate never ran")
	}
}

// The launch path fails closed: a gate its Connector cannot serve refuses the
// launch and names the remedy, instead of letting an unverified read-only
// token reach the Box.
func TestWalkGateRegistry_EnforceRefusesUnsupportedGate(t *testing.T) {
	var ranAfter bool
	registry := []launchGate{
		{Name: "read-only-token-github", Network: true, UnsupportedRemedy: "pin a Connector that serves forge.tokenGrant", Check: func(config, io.Writer) error {
			return fmt.Errorf("introspecting BOX_GH_TOKEN: no github connector is configured: %w", backendkit.ErrUnsupported)
		}},
		{Name: "after", Network: true, Check: func(config, io.Writer) error { ranAfter = true; return nil }},
	}

	var buf bytes.Buffer
	err := walkGateRegistry(registry, config{}, &buf, doctor.NewReporter(&buf, true), walkEnforce)
	if err == nil {
		t.Fatal("walkGateRegistry() = nil, want a refusal: an unverifiable read-only token must not launch")
	}
	for _, want := range []string{"read-only-token-github", "refused", "no github connector is configured", "pin a Connector that serves forge.tokenGrant"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
	if errors.Is(err, backendkit.ErrUnsupported) {
		t.Errorf("err = %v, must not wrap ErrUnsupported: Blocking would treat it as non-blocking", err)
	}
	if ranAfter {
		t.Error("a later gate ran after the refusal")
	}
}

// The real github row's gate, built by readOnlyTokenGates, refuses with the
// remedy a Connector without forge.tokenGrant needs.
func TestGateRegistry_EnforceRefusesGithubWithoutTokenGrant(t *testing.T) {
	row := rowWithIntrospector(t, "github", func(string, string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{}, fmt.Errorf("the github connector cannot introspect the token: %w", backendkit.ErrUnsupported)
	})
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-only"
	c.CodeForge, c.IssueTracker = "github", "github"
	c.GhToken = "launcher-token"
	t.Setenv("BOX_GH_TOKEN", "ghp_boxtoken")

	var buf bytes.Buffer
	err := walkGateRegistry(readOnlyTokenGates([]backendkit.Strategy{row}), c, &buf, doctor.NewDiscardReporter(), walkEnforce)
	if err == nil || !strings.Contains(err.Error(), "pin a github Connector that serves forge.tokenGrant") {
		t.Fatalf("walkGateRegistry() = %v, want a refusal naming the remedy", err)
	}
}
