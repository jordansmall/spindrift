package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
)

func TestRemovedKnobsCheck_PassesWhenUnset(t *testing.T) {
	if _, err := removedKnobsCheck().Probe(); err != nil {
		t.Fatalf("Probe() err = %v, want nil with no removed knob set", err)
	}
}

func TestRemovedKnobsCheck_RejectsEnv(t *testing.T) {
	// Set-but-empty counts: an explicit `ORCHESTRATOR_ENABLED=` in an env file
	// is still a stale setting.
	for _, v := range []string{"", "1", "true", "false"} {
		t.Run("value="+v, func(t *testing.T) {
			t.Setenv("ORCHESTRATOR_ENABLED", v)
			_, err := removedKnobsCheck().Probe()
			assertRemovedOrchestratorErr(t, err)
		})
	}
}

func TestRemovedKnobsCheck_RejectsInputDocumentSetting(t *testing.T) {
	useDoc(t, &inputdoc.Document{Settings: map[string]string{"ORCHESTRATOR_ENABLED": "true"}})
	_, err := removedKnobsCheck().Probe()
	assertRemovedOrchestratorErr(t, err)
}

func assertRemovedOrchestratorErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Probe() err = nil, want a removed-knob rejection")
	}
	for _, want := range []string{"ORCHESTRATOR_ENABLED", "was removed", "orchestrator is now the only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
}

// Both dispatch (validate) and `spindrift doctor` (validateConfig) must see
// the rejection.
func TestRemovedKnob_FailsDispatchAndDoctorValidation(t *testing.T) {
	t.Setenv("ORCHESTRATOR_ENABLED", "true")
	c := minimalValidConfig()
	assertRemovedOrchestratorErr(t, validate(c))
	assertRemovedOrchestratorErr(t, validateConfig(c))
}

// The launcher declares no orchestrator flag, so a stale one is rejected like
// any unknown flag, not special-cased.
func TestParseFlags_StaleOrchestratorFlagIsUnknown(t *testing.T) {
	for _, arg := range []string{"--orchestrator", "--orchestrator=false", "--orchestrator-enabled"} {
		t.Run(arg, func(t *testing.T) {
			_, _, err := parseFlags([]string{arg})
			want := "unknown flag: " + arg
			if err == nil || err.Error() != want {
				t.Errorf("parseFlags(%q) err = %v, want %q", arg, err, want)
			}
		})
	}
}

// assertReadWriteRemovedErr pins the migration pointer a removed
// BOX_FORGE_AND_ISSUE_ACCESS=read-write value must carry, not the generic
// "must be read-only" choice error.
func assertReadWriteRemovedErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil, want the read-write removal rejection")
	}
	for _, want := range []string{"BOX_FORGE_AND_ISSUE_ACCESS=read-write was removed", "#6901", "host-mediated", "MIGRATING.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
}

// read-only stays a valid, silent setting: only the removed value is rejected.
func TestRemovedKnobsCheck_BoxAccessValue(t *testing.T) {
	t.Run("read-only passes", func(t *testing.T) {
		t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-only")
		if _, err := removedKnobsCheck().Probe(); err != nil {
			t.Errorf("Probe() err = %v, want nil for an explicit read-only", err)
		}
	})
	t.Run("read-write in env fails", func(t *testing.T) {
		t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-write")
		_, err := removedKnobsCheck().Probe()
		assertReadWriteRemovedErr(t, err)
	})
	t.Run("read-write in the input document fails", func(t *testing.T) {
		useDoc(t, &inputdoc.Document{Settings: map[string]string{"BOX_FORGE_AND_ISSUE_ACCESS": "read-write"}})
		_, err := removedKnobsCheck().Probe()
		assertReadWriteRemovedErr(t, err)
	})
}

// Launch time: dispatch (validate) and doctor (validateConfig) both reject
// read-write whichever tier supplied it, and the flag's deprecated alias too.
func TestValidate_RejectsReadWriteBoxAccess(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"env": func(t *testing.T) { t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-write") },
		"document": func(t *testing.T) {
			useDoc(t, &inputdoc.Document{Settings: map[string]string{"BOX_FORGE_AND_ISSUE_ACCESS": "read-write"}})
		},
		"flag": func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
			setKnob(t, "BOX_FORGE_AND_ISSUE_ACCESS", "read-write")
		},
		"deprecated flag alias": func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
			restoreLauncherInput(t)
			testFlagArgs = append(slices.Clone(testFlagArgs), "--box-forge-and-issue-access", "read-write")
			resolveTestInput(t)
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			set(t)
			c := loadConfig()
			c.runtime = "echo"
			c.GhToken, c.RepoSlug, c.GitUserName, c.GitUserEmail = "ghp_test", "owner/repo", "bot", "bot@example.com"
			assertReadWriteRemovedErr(t, validate(c))
			assertReadWriteRemovedErr(t, validateConfig(c))
		})
	}
}

// The doctor's choice walk skips only a removed value the removed-knobs row
// already reported, not every choice set in the environment.
func TestValidateConfig_ReportsInvalidChoiceFromEnv(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("MERGE_MODE", "bogus")
	c := loadConfig()
	c.runtime = "echo"
	c.GhToken, c.RepoSlug, c.GitUserName, c.GitUserEmail = "ghp_test", "owner/repo", "bot", "bot@example.com"
	c.MergeMode = "bogus"
	if err := validateConfig(c); err == nil || !strings.Contains(err.Error(), `MERGE_MODE="bogus" is not valid`) {
		t.Fatalf("validateConfig() = %v, want the MERGE_MODE choice rejection", err)
	}
}

// Without the removed-knobs row in the checks, the walk is the only reporter
// and must still reject an env-set removed choice, once.
func TestValidateConfigChecks_WithoutRemovedKnobsRowStillRejects(t *testing.T) {
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-write")
	c := loadConfig()
	c.BoxForgeAndIssueAccess = "read-write"
	err := validateConfigChecks(c, nil)
	if n := strings.Count(fmt.Sprint(err), "BOX_FORGE_AND_ISSUE_ACCESS=read-write was removed"); n != 1 {
		t.Fatalf("validateConfigChecks() names the removal %d times, want once: %v", n, err)
	}
}

// Doctor names a removed choice once, not from both the row and the walk. The
// flag makes the resolved config read-write, so the walk really sees it.
func TestValidateConfig_ReportsRemovedChoiceOnce(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("BOX_FORGE_AND_ISSUE_ACCESS", "read-write")
	setKnob(t, "BOX_FORGE_AND_ISSUE_ACCESS", "read-write")
	c := loadConfig()
	if c.BoxForgeAndIssueAccess != "read-write" {
		t.Fatalf("BoxForgeAndIssueAccess = %q, want read-write", c.BoxForgeAndIssueAccess)
	}
	c.runtime = "echo"
	c.GhToken, c.RepoSlug, c.GitUserName, c.GitUserEmail = "ghp_test", "owner/repo", "bot", "bot@example.com"
	err := validateConfig(c)
	if n := strings.Count(fmt.Sprint(err), "BOX_FORGE_AND_ISSUE_ACCESS=read-write was removed"); n != 1 {
		t.Fatalf("validateConfig() names the removal %d times, want once: %v", n, err)
	}
}

func TestValidateChoice_BoxAccessReadWriteIsRemoved(t *testing.T) {
	assertReadWriteRemovedErr(t, validateChoice("BOX_FORGE_AND_ISSUE_ACCESS", "read-write"))
	if err := validateChoice("BOX_FORGE_AND_ISSUE_ACCESS", "read-only"); err != nil {
		t.Errorf("validateChoice(read-only) = %v, want nil", err)
	}
	// A value that was never valid keeps the generic choice error.
	if err := validateChoice("BOX_FORGE_AND_ISSUE_ACCESS", "bogus"); err == nil || strings.Contains(err.Error(), "removed") {
		t.Errorf("validateChoice(bogus) = %v, want the generic invalid-choice error", err)
	}
}

// The Box token knobs (issue #6902) have no reader left: each is rejected like
// any removed knob, in the environment and in an input document, with a
// pointer to MIGRATING.md.
func TestRemovedKnobsCheck_RejectsBoxTokenKnobs(t *testing.T) {
	for _, env := range []string{"BOX_GH_TOKEN", "BOX_FORGEJO_TOKEN", "BOX_FORGE_CREDENTIAL"} {
		t.Run(env+"/env", func(t *testing.T) {
			t.Setenv(env, "")
			assertRemovedBoxTokenErr(t, env)
		})
		t.Run(env+"/document", func(t *testing.T) {
			useDoc(t, &inputdoc.Document{Settings: map[string]string{env: "x"}})
			assertRemovedBoxTokenErr(t, env)
		})
	}
}

func assertRemovedBoxTokenErr(t *testing.T, env string) {
	t.Helper()
	_, err := removedKnobsCheck().Probe()
	if err == nil {
		t.Fatalf("%s: Probe() err = nil, want a removed-knob rejection", env)
	}
	for _, want := range []string{env, "was removed", "MIGRATING.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %q, want it to contain %q", env, err, want)
		}
	}
}

func TestRemovedKnobsCheck_RejectsConnectorSandbox(t *testing.T) {
	assertRejected := func(t *testing.T) {
		t.Helper()
		_, err := removedKnobsCheck().Probe()
		if err == nil {
			t.Fatal("Probe() err = nil, want a removed-knob rejection")
		}
		for _, want := range []string{"CONNECTOR_SANDBOX", "was removed", "always on"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to contain %q", err, want)
			}
		}
	}
	t.Run("env", func(t *testing.T) {
		t.Setenv("CONNECTOR_SANDBOX", "false")
		assertRejected(t)
	})
	t.Run("input document", func(t *testing.T) {
		useDoc(t, &inputdoc.Document{Settings: map[string]string{"CONNECTOR_SANDBOX": "1"}})
		assertRejected(t)
	})
}

// A row needs only env and reason: an env-only knob (one a Connector module
// declared, with no flake option) is caught here from the environment or the
// input document; mkHarness's `defaults` key also rejects it.
func TestRemovedKnobsCheck_RejectsEnvOnlyRow(t *testing.T) {
	old := removedKnobs
	removedKnobs = append(slices.Clone(old), struct{ env, reason string }{"FIXTURE_ENV_ONLY", "a fixture knob with no flake option"})
	t.Cleanup(func() { removedKnobs = old })
	want := "FIXTURE_ENV_ONLY was removed: a fixture knob with no flake option; delete the setting"
	assertRejected := func(t *testing.T) {
		t.Helper()
		if _, err := removedKnobsCheck().Probe(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Probe() err = %v, want it to contain %q", err, want)
		}
	}

	t.Run("env", func(t *testing.T) {
		t.Setenv("FIXTURE_ENV_ONLY", "")
		assertRejected(t)
	})
	t.Run("document", func(t *testing.T) {
		useDoc(t, &inputdoc.Document{Settings: map[string]string{"FIXTURE_ENV_ONLY": "x"}})
		assertRejected(t)
	})
}

// The github Connector's own token knob is gone: GH_TOKEN is the credential,
// so the old env is refused with a message that names its replacement.
func TestRemovedKnobsCheck_RejectsGithubConnectorToken(t *testing.T) {
	t.Setenv("SPINDRIFT_GITHUB_CONNECTOR_TOKEN", "ghp_stale")
	_, err := removedKnobsCheck().Probe()
	if err == nil {
		t.Fatal("Probe() err = nil, want a removed-knob rejection")
	}
	for _, want := range []string{"SPINDRIFT_GITHUB_CONNECTOR_TOKEN was removed", "GH_TOKEN", "MIGRATING.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
}
