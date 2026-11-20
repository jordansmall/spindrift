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
)

const connectorWriteTokenEnv = "SPINDRIFT_GITHUB_CONNECTOR_TOKEN"

// useGithubConnectorRow applies a github row (primary, shadow, or neither)
// with the connector token set, and swaps the github token hook for f.
func useGithubConnectorRow(t *testing.T, primary bool, f backendkit.TokenIntrospector) {
	t.Helper()
	row := backendrows.Row{Name: "github", Module: "/x/github.wasm", CredentialEnv: connectorWriteTokenEnv, Routes: []connectorhost.Route{fixtureGetRepos}}
	if primary {
		row.Primary = true
	} else {
		row.Shadow, row.ShadowEvery = true, 1
	}
	useConnectorRows(t, row)
	t.Setenv(connectorWriteTokenEnv, "connector-token")
	swapIntrospector(t, "github", f)
}

// walkRoutedWriteGate runs only the primary-connector-write-github gate over
// c, through the walk the gated tier (walkEnforce) and doctor (walkReport) share.
func walkRoutedWriteGate(c config, mode walkMode) (string, error) {
	var buf bytes.Buffer
	var gates []launchGate
	for _, g := range gateRegistry {
		if g.Name == primaryConnectorWriteGateName {
			gates = append(gates, g)
		}
	}
	err := walkGateRegistry(gates, c, &buf, doctor.NewReporter(&buf, true), mode)
	return buf.String(), err
}

func TestRoutedConnectorWriteGate_RefusesAReadOnlyCredential(t *testing.T) {
	var gotToken string
	useGithubConnectorRow(t, true, func(token, _ string) (backendkit.TokenIntrospection, error) {
		gotToken = token
		return backendkit.TokenIntrospection{Introspectable: true, WriteCapable: false}, nil
	})
	out, err := walkRoutedWriteGate(minimalValidConfig(), walkEnforce)
	if !errors.Is(err, errLaunchGateConfigInvalid) {
		t.Fatalf("err = %v, want errLaunchGateConfigInvalid", err)
	}
	for _, want := range []string{connectorWriteTokenEnv, "owner/repo", "Issues", "Pull requests", "Contents"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
	if gotToken != "connector-token" {
		t.Errorf("introspected token = %q, want the connector credential", gotToken)
	}
	if !strings.Contains(out, primaryConnectorWriteGateName) {
		t.Errorf("report = %q, want a finding for %s", out, primaryConnectorWriteGateName)
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

func TestRoutedConnectorWriteGate_OneSideRefusesAReadOnlyCredential(t *testing.T) {
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
			useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) {
				return backendkit.TokenIntrospection{Introspectable: true, WriteCapable: false}, nil
			})
			c := minimalValidConfig()
			c.IssueTracker, c.CodeForge = tc.tracker, tc.forge
			_, err := walkRoutedWriteGate(c, walkEnforce)
			if !errors.Is(err, errLaunchGateConfigInvalid) {
				t.Fatalf("err = %v, want errLaunchGateConfigInvalid", err)
			}
			for _, want := range append([]string{connectorWriteTokenEnv}, tc.want...) {
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

func TestRoutedConnectorWriteGate_PRSideOnlyUndeterminableWarnsWithFullSet(t *testing.T) {
	useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{Reason: "fine-grained PAT"}, nil
	})
	out, err := walkRoutedWriteGate(prSideOnlyConfig(), walkEnforce)
	if err != nil {
		t.Fatalf("err = %v, want nil: undeterminable scopes warn", err)
	}
	for _, want := range []string{"WARNING:", "fine-grained PAT", "Issues", "Pull requests", "Contents", "Checks", "Commit statuses"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q", out, want)
		}
	}
}

func TestRoutedConnectorWriteGate_PRSideOnlyInstallationTokenWarns(t *testing.T) {
	useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) {
		t.Error("introspector called for an installation token, want it skipped")
		return backendkit.TokenIntrospection{}, nil
	})
	t.Setenv(connectorWriteTokenEnv, "ghs_installationtoken")
	out, err := walkRoutedWriteGate(prSideOnlyConfig(), walkEnforce)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	for _, want := range []string{"WARNING:", "Pull requests", "Contents"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q", out, want)
		}
	}
}

func TestRoutedConnectorWriteGate_UndeterminableWarns(t *testing.T) {
	cases := map[string]struct {
		result backendkit.TokenIntrospection
		err    error
		want   string
	}{
		"not introspectable": {result: backendkit.TokenIntrospection{Reason: "fine-grained PAT"}, want: "fine-grained PAT"},
		"unsupported":        {err: backendkit.ErrUnsupported, want: "could not be determined"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) { return tc.result, tc.err })
			out, err := walkRoutedWriteGate(minimalValidConfig(), walkEnforce)
			if err != nil {
				t.Fatalf("err = %v, want nil: undeterminable scopes warn", err)
			}
			for _, want := range []string{"WARNING:", connectorWriteTokenEnv, tc.want} {
				if !strings.Contains(out, want) {
					t.Errorf("output = %q, want %q", out, want)
				}
			}
		})
	}
}

func TestRoutedConnectorWriteGate_NotIntrospectableWithoutReasonFallsBack(t *testing.T) {
	useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{}, nil
	})
	out, err := walkRoutedWriteGate(minimalValidConfig(), walkEnforce)
	if err != nil {
		t.Fatalf("err = %v, want nil: an empty-Reason introspection warns", err)
	}
	if !strings.Contains(out, "(token not introspectable)") {
		t.Errorf("output = %q, want the %q fallback reason", out, "token not introspectable")
	}
}

func TestRoutedConnectorWriteGate_IntrospectionFailureIsConnectivity(t *testing.T) {
	useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{}, errors.New("boom")
	})
	_, err := walkRoutedWriteGate(minimalValidConfig(), walkEnforce)
	if !errors.Is(err, doctor.ErrConnectivity) || !strings.Contains(err.Error(), connectorWriteTokenEnv) {
		t.Fatalf("err = %v, want a connectivity error naming %s", err, connectorWriteTokenEnv)
	}
}

func TestRoutedConnectorWriteGate_WriteCapablePasses(t *testing.T) {
	useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true}, nil
	})
	out, err := walkRoutedWriteGate(minimalValidConfig(), walkEnforce)
	if err != nil || strings.Contains(out, "WARNING") {
		t.Fatalf("err = %v, output = %q, want a clean pass", err, out)
	}
}

func TestRoutedConnectorWriteGate_NotApplicable(t *testing.T) {
	cases := map[string]struct {
		primary bool
		tracker string
		forge   string
	}{
		"shadow row":                   {primary: false, tracker: "github", forge: "github"},
		"non-github tracker and forge": {primary: true, tracker: "forgejo", forge: "forgejo"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			useGithubConnectorRow(t, tc.primary, func(string, string) (backendkit.TokenIntrospection, error) {
				called = true
				return backendkit.TokenIntrospection{Introspectable: true}, nil
			})
			c := minimalValidConfig()
			c.IssueTracker, c.CodeForge = tc.tracker, tc.forge
			out, err := walkRoutedWriteGate(c, walkEnforce)
			if err != nil || called || out != "" {
				t.Errorf("err = %v, called = %v, output = %q, want the gate skipped", err, called, out)
			}
		})
	}
}

func TestRoutedConnectorWriteGate_InstallationTokenWarnsWithoutIntrospecting(t *testing.T) {
	called := false
	useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) {
		called = true
		return backendkit.TokenIntrospection{Introspectable: true, WriteCapable: false}, nil
	})
	t.Setenv(connectorWriteTokenEnv, "ghs_installationtoken")
	out, err := walkRoutedWriteGate(minimalValidConfig(), walkEnforce)
	if err != nil {
		t.Fatalf("err = %v, want nil: an installation token's Issues permission is undeterminable", err)
	}
	if called {
		t.Error("introspector called for an installation token, want it skipped")
	}
	for _, want := range []string{"WARNING:", connectorWriteTokenEnv, "Contents", "Issues", "Pull requests"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q", out, want)
		}
	}
}

func TestRoutedConnectorWriteGate_UnsetCredentialWarns(t *testing.T) {
	called := false
	useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) {
		called = true
		return backendkit.TokenIntrospection{}, nil
	})
	t.Setenv(connectorWriteTokenEnv, "")
	out, err := walkRoutedWriteGate(minimalValidConfig(), walkEnforce)
	if err != nil || called {
		t.Fatalf("err = %v, called = %v, want a warning only", err, called)
	}
	for _, want := range []string{"WARNING:", connectorWriteTokenEnv, "not set", "Issues", "Pull requests", "Contents", "Checks", "Commit statuses"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q", out, want)
		}
	}
}

func TestRoutedConnectorWriteGate_DoctorReportsWriteCapability(t *testing.T) {
	fullSet := []string{"Issues", "Pull requests", "Contents"}
	cases := map[string]struct {
		result  backendkit.TokenIntrospection
		wantErr bool
		want    []string
	}{
		"read-only":      {result: backendkit.TokenIntrospection{Introspectable: true}, wantErr: true, want: append([]string{"MISSING: " + primaryConnectorWriteGateName}, fullSet...)},
		"undeterminable": {result: backendkit.TokenIntrospection{Reason: "fine-grained PAT"}, want: append([]string{"WARNING:", "fine-grained PAT"}, fullSet...)},
		"write-capable":  {result: backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true}, want: []string{"ok: " + primaryConnectorWriteGateName}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useGithubConnectorRow(t, true, func(string, string) (backendkit.TokenIntrospection, error) {
				return tc.result, nil
			})
			out, err := walkRoutedWriteGate(minimalValidConfig(), walkReport)
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

// A plain non-shadow github row routes the same writes as a primary one, so the
// gate refuses its read-only credential too.
func TestRoutedConnectorWriteGate_AppliesToAPlainRow(t *testing.T) {
	useConnectorRows(t, backendrows.Row{Name: "github", Module: "/x/github.wasm", CredentialEnv: connectorWriteTokenEnv, Routes: []connectorhost.Route{fixtureGetRepos}})
	t.Setenv(connectorWriteTokenEnv, "connector-token")
	swapIntrospector(t, "github", func(string, string) (backendkit.TokenIntrospection, error) {
		return backendkit.TokenIntrospection{Introspectable: true, WriteCapable: false}, nil
	})

	_, err := walkRoutedWriteGate(minimalValidConfig(), walkEnforce)

	if !errors.Is(err, errLaunchGateConfigInvalid) {
		t.Fatalf("err = %v, want errLaunchGateConfigInvalid", err)
	}
}

// Under a GH_HOST other than github.com the github row is not applied (its
// Routes pin api.github.com), so the write gate does not apply and the
// connector credential is not needed this run.
func TestRoutedConnectorWriteGate_GitHubEnterpriseHostAppliesNoRow(t *testing.T) {
	row := backendrows.Row{Name: "github", Module: "/x/github.wasm", CredentialEnv: connectorWriteTokenEnv, Routes: []connectorhost.Route{fixtureGetRepos}}
	ghe := func(k string) string {
		if k == "GH_HOST" {
			return "ghe.example.com"
		}
		return ""
	}
	useConnectorRowsWith(t, ghe, row)
	c := minimalValidConfig()
	c.IssueTracker, c.CodeForge = "github", "github"
	if routedGithubApplies(c) {
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
