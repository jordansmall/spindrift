package main

import (
	"errors"
	"testing"
)

func workEnv() map[string]string {
	return map[string]string{
		"GH_TOKEN": "tok", "REPO_SLUG": "owner/repo",
		"DISPATCH_KEY": "42", "DISPATCH_KEYING": "issue", "DISPATCH_ANNOUNCE_VERB": "implementing",
		"ISSUE_NUMBER": "42", "GIT_USER_NAME": "agent", "GIT_USER_EMAIL": "agent@example.com",
	}
}

func choreEnv() map[string]string {
	e := workEnv()
	delete(e, "ISSUE_NUMBER")
	e["DISPATCH_KEYING"] = "chore"
	e["CHORE_NAME"] = "bugs"
	return e
}

func guardErr(env map[string]string) error {
	return checkEnvGuards(func(k string) string { return env[k] })
}

func TestEnvGuards_CompleteEnvPasses(t *testing.T) {
	for name, env := range map[string]map[string]string{"work": workEnv(), "chore": choreEnv()} {
		if err := guardErr(env); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEnvGuards_MissingVarIsNamed(t *testing.T) {
	cases := []struct {
		env     func() map[string]string
		drop    string
		wantMsg string
	}{
		{workEnv, "GH_TOKEN", "GH_TOKEN is required"},
		{workEnv, "DISPATCH_KEY", "DISPATCH_KEY is required"},
		{workEnv, "DISPATCH_KEYING", "DISPATCH_KEYING is required"},
		{workEnv, "DISPATCH_ANNOUNCE_VERB", "DISPATCH_ANNOUNCE_VERB is required"},
		{workEnv, "ISSUE_NUMBER", "ISSUE_NUMBER is required"},
		{choreEnv, "CHORE_NAME", "CHORE_NAME is required"},
		{workEnv, "REPO_SLUG", "REPO_SLUG (owner/repo) is required"},
		{workEnv, "GIT_USER_NAME", "GIT_USER_NAME is required"},
		{workEnv, "GIT_USER_EMAIL", "GIT_USER_EMAIL is required"},
	}
	for _, c := range cases {
		for _, how := range []string{"unset", "empty"} {
			env := c.env()
			if how == "empty" {
				env[c.drop] = ""
			} else {
				delete(env, c.drop)
			}
			err := guardErr(env)
			var me *missingEnvError
			if !errors.As(err, &me) || me.Name != c.drop || err.Error() != c.wantMsg {
				t.Errorf("%s %s: err = %v, want %q", c.drop, how, err, c.wantMsg)
			}
		}
	}
}

func TestEnvGuards_ReportsTheFirstMissingInBashOrder(t *testing.T) {
	err := guardErr(map[string]string{})
	if err == nil || err.Error() != "GH_TOKEN is required" {
		t.Fatalf("err = %v", err)
	}
	env := workEnv()
	delete(env, "REPO_SLUG")
	delete(env, "GIT_USER_NAME")
	if err := guardErr(env); err == nil || err.Error() != "REPO_SLUG (owner/repo) is required" {
		t.Fatalf("err = %v", err)
	}
}

func TestEnvGuards_KeyingPicksTheRequiredIdentifier(t *testing.T) {
	env := choreEnv()
	env["ISSUE_NUMBER"] = "7"
	delete(env, "CHORE_NAME")
	if err := guardErr(env); err == nil || err.Error() != "CHORE_NAME is required" {
		t.Errorf("chore keying with ISSUE_NUMBER only: err = %v", err)
	}
	env = workEnv()
	env["CHORE_NAME"] = "bugs"
	delete(env, "ISSUE_NUMBER")
	if err := guardErr(env); err == nil || err.Error() != "ISSUE_NUMBER is required" {
		t.Errorf("issue keying with CHORE_NAME only: err = %v", err)
	}
}

func TestEnvGuards_ForgeVarsExemptions(t *testing.T) {
	cases := []struct {
		name    string
		set     map[string]string
		exempt  bool
		wantMsg string
	}{
		{"fully local", map[string]string{"BOX_FULLY_LOCAL": "1"}, true, ""},
		{"local tracker without BOX_FULLY_LOCAL", map[string]string{"ISSUE_TRACKER": "local", "BOX_TRACKER_AXIS_READ": "LOCAL"}, false, "GH_TOKEN is required"},
		{"local forge without BOX_FULLY_LOCAL", map[string]string{"CODE_FORGE": "local"}, false, "GH_TOKEN is required"},
		{"self-contained with unreachable tracker", map[string]string{"SELF_CONTAINED": "1", "BOX_IN_BOX_UNREACHABLE_TRACKER": "1"}, true, ""},
		{"self-contained on a reachable tracker", map[string]string{"SELF_CONTAINED": "1"}, false, "GH_TOKEN is required"},
		{"unreachable tracker without self-contained", map[string]string{"BOX_IN_BOX_UNREACHABLE_TRACKER": "1"}, false, "GH_TOKEN is required"},
		{"self-contained other than 1", map[string]string{"SELF_CONTAINED": "true", "BOX_IN_BOX_UNREACHABLE_TRACKER": "1"}, false, "GH_TOKEN is required"},
	}
	for _, c := range cases {
		env := workEnv()
		delete(env, "GH_TOKEN")
		delete(env, "REPO_SLUG")
		for k, v := range c.set {
			env[k] = v
		}
		err := guardErr(env)
		if c.exempt && err != nil {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if !c.exempt && (err == nil || err.Error() != c.wantMsg) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantMsg)
		}
	}
}

func TestEnvGuards_SelfContainedWithToken_StillRequiresRepoSlug(t *testing.T) {
	env := workEnv()
	delete(env, "REPO_SLUG")
	env["SELF_CONTAINED"] = "1"
	if err := guardErr(env); err == nil || err.Error() != "REPO_SLUG (owner/repo) is required" {
		t.Fatalf("err = %v", err)
	}
}

func TestEnvGuards_ExemptDispatchStillRequiresTheRest(t *testing.T) {
	for _, drop := range []string{"ISSUE_NUMBER", "GIT_USER_NAME", "GIT_USER_EMAIL"} {
		t.Run(drop, func(t *testing.T) {
			env := workEnv()
			delete(env, "GH_TOKEN")
			delete(env, "REPO_SLUG")
			delete(env, drop)
			env["BOX_FULLY_LOCAL"] = "1"
			if err := guardErr(env); err == nil || err.Error() != drop+" is required" {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
