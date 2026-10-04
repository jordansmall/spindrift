package main

import (
	"strings"
	"testing"
)

const wantStoreNotice = "==> WARNING: /nix/store is writable (self-test mode) — this Box is not hermetic; do not use for untrusted issues"

func TestStoreNotice_PrintedOnlyForTrue(t *testing.T) {
	for _, c := range []struct {
		val  string
		want bool
	}{
		{"true", true}, {"", false}, {"false", false}, {"1", false}, {"TRUE", false},
	} {
		f := newFixture(t)
		if c.val != "" {
			f.knobs["NIX_STORE_WRITABLE"] = c.val
		}
		f.run()
		if got := strings.Contains(f.stdout(), wantStoreNotice+"\n"); got != c.want {
			t.Errorf("NIX_STORE_WRITABLE=%q: notice printed = %v, want %v", c.val, got, c.want)
		}
	}
}

func TestStoreNotice_FollowsTheEnvGuards(t *testing.T) {
	f := newFixture(t)
	f.knobs["NIX_STORE_WRITABLE"] = "true"
	delete(f.knobs, "GIT_USER_NAME")
	if _, err := run(f.in, f.env, f.d); err == nil {
		t.Fatal("run() succeeded without GIT_USER_NAME")
	}
	if strings.Contains(f.stdout(), wantStoreNotice) {
		t.Errorf("notice printed before the env guards passed\n%s", f.stdout())
	}
}

func TestStoreNotice_IsTheFirstLineAfterTheGuards(t *testing.T) {
	f := newFixture(t)
	f.knobs["NIX_STORE_WRITABLE"] = "true"
	f.run()
	if got := f.lines()[0]; got != wantStoreNotice {
		t.Errorf("first stdout line = %q, want the store notice", got)
	}
}

func TestDirDefaults(t *testing.T) {
	unset := func(string) string { return "" }
	got := withDirDefaults(inputs{WorkDir: "/w"}, unset)
	if got.HarnessSkillsDir != "/agent/skills" || got.OperatorSkillsDir != "/operator-skills" ||
		got.HarnessHomeAgentDir != "/home-agent-staged" {
		t.Errorf("defaults = %q %q %q", got.HarnessSkillsDir, got.OperatorSkillsDir, got.HarnessHomeAgentDir)
	}
	if got.WorkDir != "/w" {
		t.Errorf("WorkDir = %q, want the other inputs left alone", got.WorkDir)
	}
	env := map[string]string{
		"HARNESS_SKILLS_DIR": "/h", "OPERATOR_SKILLS_DIR": "/o", "HARNESS_HOME_AGENT_DIR": "/a",
	}
	got = withDirDefaults(inputs{}, func(k string) string { return env[k] })
	if got.HarnessSkillsDir != "/h" || got.OperatorSkillsDir != "/o" || got.HarnessHomeAgentDir != "/a" {
		t.Errorf("env values = %q %q %q", got.HarnessSkillsDir, got.OperatorSkillsDir, got.HarnessHomeAgentDir)
	}
}

func TestDirDefaultsWorkOutboxAndRepoMount(t *testing.T) {
	unset := func(string) string { return "" }
	got := withDirDefaults(inputs{}, unset)
	if got.WorkDir != "/work" || got.OutboxDir != "/outbox" || got.RepoMountDir != "/repo" {
		t.Errorf("defaults = %q %q %q", got.WorkDir, got.OutboxDir, got.RepoMountDir)
	}
	got = withDirDefaults(inputs{WorkDir: "/w", OutboxDir: "/o"}, func(k string) string {
		if k == "REPO_MOUNT_DIR" {
			return "/r"
		}
		return ""
	})
	if got.WorkDir != "/w" || got.OutboxDir != "/o" || got.RepoMountDir != "/r" {
		t.Errorf("explicit = %q %q %q", got.WorkDir, got.OutboxDir, got.RepoMountDir)
	}
}
