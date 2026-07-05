package runner

import (
	"strings"
	"testing"
)

// TestBwrapArgs_NoSecretOnArgv verifies that secret env var values are not
// passed as bwrap command-line arguments (which would expose them via ps/proc).
func TestBwrapArgs_NoSecretOnArgv(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := Box{
		Env: map[string]string{
			"GH_TOKEN":                "gh-secret-value",
			"CLAUDE_CODE_OAUTH_TOKEN": "claude-secret-value",
			"ANTHROPIC_API_KEY":       "anthropic-secret-value",
			"REPO_SLUG":               "owner/repo",
			"ISSUE_NUMBER":            "42",
		},
	}

	args := a.buildArgs("/tmp/fake-etc", box)

	secrets := []string{"gh-secret-value", "claude-secret-value", "anthropic-secret-value"}
	for _, arg := range args {
		for _, secret := range secrets {
			if strings.Contains(arg, secret) {
				t.Errorf("secret value %q found in bwrap argv: %v", secret, args)
			}
		}
	}
}

// TestBwrapArgs_NoClearEnv verifies that --clearenv is not in the args so that
// the sandbox inherits secrets from the launcher's process environment.
func TestBwrapArgs_NoClearEnv(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	args := a.buildArgs("/tmp/fake-etc", Box{Env: map[string]string{"GH_TOKEN": "s"}})
	for _, arg := range args {
		if arg == "--clearenv" {
			t.Errorf("--clearenv found in bwrap argv; secrets would not reach sandbox")
		}
	}
}

// TestBwrapArgs_NonSecretOnArgv verifies that non-secret env vars still reach
// the sandbox via --setenv (so they appear in argv).
func TestBwrapArgs_NonSecretOnArgv(t *testing.T) {
	a := &bwrapAdapter{
		agentFiles:    "/fake/agent",
		agentEnv:      "/fake/env",
		bakedPrefetch: "echo ok",
	}
	box := Box{
		Env: map[string]string{
			"GH_TOKEN":     "gh-secret-value",
			"REPO_SLUG":    "owner/repo",
			"ISSUE_NUMBER": "42",
		},
	}

	args := a.buildArgs("/tmp/fake-etc", box)

	argStr := strings.Join(args, " ")
	for _, name := range []string{"REPO_SLUG", "ISSUE_NUMBER"} {
		if !strings.Contains(argStr, name) {
			t.Errorf("non-secret %q missing from bwrap argv", name)
		}
	}
}
