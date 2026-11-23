package jira_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/jira"
)

const rerun = "nix run example#quickstart"

func acquire(pasted string, probe func(string) error) (token, output string, err error) {
	var out bytes.Buffer
	token, err = jira.Strategy.AcquireToken(
		backendkit.Shared{},
		func(string) (string, bool) { return "", false },
		backendkit.TokenPrompt{
			Out:          &out,
			PromptMasked: func(string) string { return pasted },
			Probe:        probe,
			RerunCmd:     rerun,
		})
	return token, out.String(), err
}

func TestAcquireToken_ValidTokenIsProbed(t *testing.T) {
	var probed string
	token, out, err := acquire("tok", func(tok string) error { probed = tok; return nil })
	if err != nil || token != "tok" || probed != "tok" {
		t.Fatalf("got %q probed %q, %v", token, probed, err)
	}
	if !strings.Contains(out, "ok: Jira token validated via live API ping") {
		t.Errorf("output = %q", out)
	}
}

// Quickstart installs no Connector sandbox, so the live check cannot run: the
// token is accepted and the check is deferred to doctor.
func TestAcquireToken_NoConnectorSandboxDefersToDoctor(t *testing.T) {
	token, out, err := acquire("tok", func(string) error {
		return fmt.Errorf("build tracker: %w", connectorsandbox.ErrNoConnectorSandbox)
	})
	if err != nil || token != "tok" {
		t.Fatalf("got %q, %v; want the token accepted", token, err)
	}
	if !strings.Contains(out, "spindrift doctor") || strings.Contains(out, "validated via live API ping") {
		t.Errorf("output = %q, want a deferral to doctor", out)
	}
}

func TestAcquireToken_Failures(t *testing.T) {
	cases := []struct {
		name  string
		paste string
		probe error
		want  []string
		is    error
	}{
		{"blank", "", nil, []string{"no Jira token provided", rerun}, nil},
		{"auth", "t", forge.ErrAuthFailure, []string{"rejected by the API", "JIRA_BASE_URL", rerun}, forge.ErrAuthFailure},
		{"other", "t", errors.New("dial tcp"), []string{"JIRA_BASE_URL", "JIRA_PROJECT_KEY", rerun}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := acquire(c.paste, func(string) error { return c.probe })
			if err == nil {
				t.Fatal("want error")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q missing %q", err, w)
				}
			}
			if c.is != nil && !errors.Is(err, c.is) {
				t.Errorf("error %v does not wrap %v", err, c.is)
			}
		})
	}
}
