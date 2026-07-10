package dispatch

import "testing"

// TestBuildBoxEnvForwardsSchemaVars verifies that buildBoxEnv picks up env
// var names listed in Config.BoxEnvVars and that per-issue vars are always
// present.
func TestBuildBoxEnvForwardsSchemaVars(t *testing.T) {
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "tok")

	cfg := Config{BoxEnvVars: "REPO_SLUG GH_TOKEN"}
	env := buildBoxEnv(cfg, "7", "Test issue", 0, "")

	if env["REPO_SLUG"] != "owner/repo" {
		t.Errorf("REPO_SLUG: got %q, want %q", env["REPO_SLUG"], "owner/repo")
	}
	if env["GH_TOKEN"] != "tok" {
		t.Errorf("GH_TOKEN: got %q, want %q", env["GH_TOKEN"], "tok")
	}
	if env["ISSUE_NUMBER"] != "7" {
		t.Errorf("ISSUE_NUMBER: got %q, want %q", env["ISSUE_NUMBER"], "7")
	}
	if env["ISSUE_TITLE"] != "Test issue" {
		t.Errorf("ISSUE_TITLE: got %q, want %q", env["ISSUE_TITLE"], "Test issue")
	}
	if _, ok := env["FIX_PASS"]; ok {
		t.Error("FIX_PASS should not be set for fixPass=0")
	}
	if _, ok := env["CI_FAILURE_SUMMARY"]; ok {
		t.Error("CI_FAILURE_SUMMARY should not be set when empty")
	}
}

// TestBuildBoxEnvSetsFixPassAndSummary verifies FIX_PASS and
// CI_FAILURE_SUMMARY are present when fixPass>0 and summary is non-empty.
func TestBuildBoxEnvSetsFixPassAndSummary(t *testing.T) {
	env := buildBoxEnv(Config{}, "3", "T", 2, "lint failed")
	if env["FIX_PASS"] != "2" {
		t.Errorf("FIX_PASS: got %q, want %q", env["FIX_PASS"], "2")
	}
	if env["CI_FAILURE_SUMMARY"] != "lint failed" {
		t.Errorf("CI_FAILURE_SUMMARY: got %q, want %q", env["CI_FAILURE_SUMMARY"], "lint failed")
	}
}
