package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/exitcode"
	"spindrift.dev/launcher/internal/forge"
)

// ghTokenEnv is the github row's one credential: core's own GH_TOKEN.
const ghTokenEnv = "GH_TOKEN"

// useGithubConnectorRow applies a github row
// with GH_TOKEN set, and swaps the github token hook for f.
func useGithubConnectorRow(t *testing.T, f backendkit.TokenIntrospector) {
	t.Helper()
	row := backendrows.Row{Name: "github", Module: "/x/github.wasm", CredentialEnv: ghTokenEnv, Routes: []connectorhost.Route{fixtureGetRepos}}
	useConnectorRows(t, row)
	t.Setenv(ghTokenEnv, "connector-token")
	swapIntrospector(t, "github", f)
}

// walkConnectorWriteGate runs only the connector-write-github gate over
// c, through the walk the gated tier (walkEnforce) and doctor (walkReport) share.
func walkConnectorWriteGate(c config, mode walkMode) (string, error) {
	var buf bytes.Buffer
	var gates []launchGate
	for _, g := range gateRegistry {
		if g.Name == connectorWriteGateName {
			gates = append(gates, g)
		}
	}
	err := walkGateRegistry(gates, c, &buf, doctor.NewReporter(&buf, true), mode)
	return buf.String(), err
}

func TestConnectorWriteGate_RefusesAReadOnlyCredential(t *testing.T) {
	var gotToken string
	useGithubConnectorRow(t, func(token, _ string) (forge.TokenIntrospection, error) {
		gotToken = token
		return forge.TokenIntrospection{Introspectable: true, WriteCapable: false}, nil
	})
	out, err := walkConnectorWriteGate(minimalValidConfig(), walkEnforce)
	if !errors.Is(err, errLaunchGateConfigInvalid) {
		t.Fatalf("err = %v, want errLaunchGateConfigInvalid", err)
	}
	for _, want := range []string{ghTokenEnv, "owner/repo", "Issues", "Pull requests", "Contents"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
	if gotToken != "connector-token" {
		t.Errorf("introspected token = %q, want the connector credential", gotToken)
	}
	if !strings.Contains(out, connectorWriteGateName) {
		t.Errorf("report = %q, want a finding for %s", out, connectorWriteGateName)
	}
	code := doctorExitCodeFor(nil, err)
	if code != int(exitcode.DoctorConfigInvalid) {
		t.Errorf("doctorExitCodeFor = %d, want DoctorConfigInvalid", code)
	}
	if v := daemon.ClassifyPreflight(code); v.Healthy {
		t.Errorf("ClassifyPreflight(%d) is healthy, want the daemon to refuse", code)
	}
}

// prSideOnlyConfig is a config whose tracker is not github but whose code
// forge is, so the Connector carries only the PR writes.
func prSideOnlyConfig() config {
	c := minimalValidConfig()
	c.IssueTracker, c.CodeForge = "forgejo", "github"
	return c
}

func TestConnectorWriteGate_OneSideRefusesAReadOnlyCredential(t *testing.T) {
	cases := map[string]struct {
		tracker, forge string
		want, bad      []string
	}{
		"PR side only": {
			tracker: "forgejo", forge: "github",
			want: []string{"Issues", "Pull requests", "Contents", "ready"},
			bad:  []string{"issues (comments"},
		},
		"tracker only": {
			tracker: "github", forge: "forgejo",
			want: []string{"Issues", "issues (comments"},
			bad:  []string{"pull requests (", "Pull requests", "Contents"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
				return forge.TokenIntrospection{Introspectable: true, WriteCapable: false}, nil
			})
			c := minimalValidConfig()
			c.IssueTracker, c.CodeForge = tc.tracker, tc.forge
			_, err := walkConnectorWriteGate(c, walkEnforce)
			if !errors.Is(err, errLaunchGateConfigInvalid) {
				t.Fatalf("err = %v, want errLaunchGateConfigInvalid", err)
			}
			for _, want := range append([]string{ghTokenEnv}, tc.want...) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to name %q", err, want)
				}
			}
			for _, bad := range tc.bad {
				if strings.Contains(err.Error(), bad) {
					t.Errorf("err = %q, want no %q for the side github does not serve", err, bad)
				}
			}
		})
	}
}

func TestConnectorWriteGate_PRSideOnlyUndeterminableWarnsWithFullSet(t *testing.T) {
	useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
		return forge.TokenIntrospection{Reason: "fine-grained PAT"}, nil
	})
	out, err := walkConnectorWriteGate(prSideOnlyConfig(), walkEnforce)
	if err != nil {
		t.Fatalf("err = %v, want nil: undeterminable scopes warn", err)
	}
	for _, want := range []string{"WARNING:", "fine-grained PAT", "Issues", "Pull requests", "Contents", "Checks", "Commit statuses"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q", out, want)
		}
	}
}

func TestConnectorWriteGate_PRSideOnlyInstallationTokenWarns(t *testing.T) {
	useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
		t.Error("introspector called for an installation token, want it skipped")
		return forge.TokenIntrospection{}, nil
	})
	t.Setenv(ghTokenEnv, "ghs_installationtoken")
	out, err := walkConnectorWriteGate(prSideOnlyConfig(), walkEnforce)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	for _, want := range []string{"WARNING:", "Pull requests", "Contents"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q", out, want)
		}
	}
}

func TestConnectorWriteGate_UndeterminableWarns(t *testing.T) {
	cases := map[string]struct {
		result forge.TokenIntrospection
		err    error
		want   string
	}{
		"not introspectable": {result: forge.TokenIntrospection{Reason: "fine-grained PAT"}, want: "fine-grained PAT"},
		"unsupported":        {err: backendkit.ErrUnsupported, want: "could not be determined"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) { return tc.result, tc.err })
			out, err := walkConnectorWriteGate(minimalValidConfig(), walkEnforce)
			if err != nil {
				t.Fatalf("err = %v, want nil: undeterminable scopes warn", err)
			}
			for _, want := range []string{"WARNING:", ghTokenEnv, tc.want} {
				if !strings.Contains(out, want) {
					t.Errorf("output = %q, want %q", out, want)
				}
			}
		})
	}
}

func TestConnectorWriteGate_NotIntrospectableWithoutReasonFallsBack(t *testing.T) {
	useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
		return forge.TokenIntrospection{}, nil
	})
	out, err := walkConnectorWriteGate(minimalValidConfig(), walkEnforce)
	if err != nil {
		t.Fatalf("err = %v, want nil: an empty-Reason introspection warns", err)
	}
	if !strings.Contains(out, "(token not introspectable)") {
		t.Errorf("output = %q, want the %q fallback reason", out, "token not introspectable")
	}
}

func TestConnectorWriteGate_IntrospectionFailureIsConnectivity(t *testing.T) {
	useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
		return forge.TokenIntrospection{}, errors.New("boom")
	})
	_, err := walkConnectorWriteGate(minimalValidConfig(), walkEnforce)
	if !errors.Is(err, doctor.ErrConnectivity) || !strings.Contains(err.Error(), ghTokenEnv) {
		t.Fatalf("err = %v, want a connectivity error naming %s", err, ghTokenEnv)
	}
}

func TestConnectorWriteGate_WriteCapablePasses(t *testing.T) {
	useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
		return forge.TokenIntrospection{Introspectable: true, WriteCapable: true}, nil
	})
	out, err := walkConnectorWriteGate(minimalValidConfig(), walkEnforce)
	if err != nil || strings.Contains(out, "WARNING") {
		t.Fatalf("err = %v, output = %q, want a clean pass", err, out)
	}
}

func TestConnectorWriteGate_NotApplicable(t *testing.T) {
	cases := map[string]struct {
		tracker string
		forge   string
	}{
		"non-github tracker and forge": {tracker: "forgejo", forge: "forgejo"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
				called = true
				return forge.TokenIntrospection{Introspectable: true}, nil
			})
			c := minimalValidConfig()
			c.IssueTracker, c.CodeForge = tc.tracker, tc.forge
			out, err := walkConnectorWriteGate(c, walkEnforce)
			if err != nil || called || out != "" {
				t.Errorf("err = %v, called = %v, output = %q, want the gate skipped", err, called, out)
			}
		})
	}
}

func TestConnectorWriteGate_InstallationTokenWarnsWithoutIntrospecting(t *testing.T) {
	called := false
	useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
		called = true
		return forge.TokenIntrospection{Introspectable: true, WriteCapable: false}, nil
	})
	t.Setenv(ghTokenEnv, "ghs_installationtoken")
	out, err := walkConnectorWriteGate(minimalValidConfig(), walkEnforce)
	if err != nil {
		t.Fatalf("err = %v, want nil: an installation token's Issues permission is undeterminable", err)
	}
	if called {
		t.Error("introspector called for an installation token, want it skipped")
	}
	for _, want := range []string{"WARNING:", ghTokenEnv, "Contents", "Issues", "Pull requests"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q", out, want)
		}
	}
}

func TestConnectorWriteGate_UnsetCredentialPassesSilently(t *testing.T) {
	called := false
	useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
		called = true
		return forge.TokenIntrospection{}, nil
	})
	t.Setenv(ghTokenEnv, "")
	out, err := walkConnectorWriteGate(minimalValidConfig(), walkEnforce)
	if err != nil || called || strings.Contains(out, "WARNING") {
		t.Errorf("err = %v, called = %v, output = %q, want a pass without a warning", err, called, out)
	}
}

func TestConnectorWriteGate_DoctorReportsWriteCapability(t *testing.T) {
	fullSet := []string{"Issues", "Pull requests", "Contents"}
	cases := map[string]struct {
		result  forge.TokenIntrospection
		wantErr bool
		want    []string
	}{
		"read-only":      {result: forge.TokenIntrospection{Introspectable: true}, wantErr: true, want: append([]string{"MISSING: " + connectorWriteGateName}, fullSet...)},
		"undeterminable": {result: forge.TokenIntrospection{Reason: "fine-grained PAT"}, want: append([]string{"WARNING:", "fine-grained PAT"}, fullSet...)},
		"write-capable":  {result: forge.TokenIntrospection{Introspectable: true, WriteCapable: true}, want: []string{"ok: " + connectorWriteGateName}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useGithubConnectorRow(t, func(string, string) (forge.TokenIntrospection, error) {
				return tc.result, nil
			})
			out, err := walkConnectorWriteGate(minimalValidConfig(), walkReport)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("report = %q, want %q", out, want)
				}
			}
		})
	}
}

// Under a GH_HOST other than github.com the github row is not applied (its
// Routes pin api.github.com), so the write gate does not apply and the
// connector credential is not needed this run.
func TestConnectorWriteGate_GitHubEnterpriseHostAppliesNoRow(t *testing.T) {
	row := backendrows.Row{Name: "github", Module: "/x/github.wasm", CredentialEnv: ghTokenEnv, Routes: []connectorhost.Route{fixtureGetRepos}}
	ghe := func(k string) string {
		if k == "GH_HOST" {
			return "ghe.example.com"
		}
		return ""
	}
	useConnectorRowsWith(t, ghe, row)
	c := minimalValidConfig()
	c.IssueTracker, c.CodeForge = "github", "github"
	if githubApplies(c) {
		t.Error("the write gate applies under GH_HOST=ghe.example.com")
	}
	if rows := backendrows.All(); len(rows) != 0 {
		t.Errorf("rows = %+v, want none, so no credential is fetched or sent", rows)
	}

	useConnectorRowsWith(t, func(string) string { return "" }, row)
	if _, ok := backendrows.Lookup("github"); !ok {
		t.Error("the github row is dropped under the default host")
	}
}
