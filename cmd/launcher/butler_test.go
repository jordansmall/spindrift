package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/signalwire"
)

// testClaimTimeout is the claim timeout every test passes explicitly now
// that BUTLER_CLAIM_TIMEOUT is an operator knob rather than a package const.
const testClaimTimeout = 6 * time.Hour

// noEvery is the zero Every: it never blocks a due check
// (IntervalNotElapsed), the shape most tests want when the interval itself
// isn't what's under test.
const noEvery time.Duration = 0

// testChores builds the []chore.Chore testButlerPolicy takes: every name gets
// the same Every and no Classes -- the shape most tests want when a per-chore
// interval or class allow-list isn't what's under test.
func testChores(every time.Duration, names ...string) []chore.Chore {
	chores := make([]chore.Chore, len(names))
	for i, name := range names {
		chores[i] = chore.Chore{Name: name, Every: every}
	}
	return chores
}

// testButlerPolicy builds a butlerPolicy for tests that don't exercise
// budgets or the day zone: testClaimTimeout, an unlimited (zero) Budgets,
// UTC, and chores built by testChores -- everything but budget/zone tests,
// which build their own butlerPolicy explicitly.
func testButlerPolicy(every time.Duration, chores ...string) butlerPolicy {
	return butlerPolicy{
		chores:       testChores(every, chores...),
		claimTimeout: testClaimTimeout,
		zone:         time.UTC,
		label:        "ready-for-agent",
	}
}

// newButlerTestRepo builds a bare repo with one commit on "main" holding one
// tracked file, the fixture every runButler test claims and sweeps against
// (same exec-git convention as internal/chore/git_test.go and
// internal/settle/butler_test.go's own bare-repo fixtures).
func newButlerTestRepo(t *testing.T) (repo, head string) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")

	dir := t.TempDir()
	bare := filepath.Join(dir, "repo.git")
	runButlerGit(t, "", "init", "--bare", "-q", bare)
	runButlerGit(t, bare, "config", "gc.auto", "0")

	hashCmd := exec.Command("git", "-C", bare, "hash-object", "-w", "--stdin")
	hashCmd.Stdin = strings.NewReader("package a\n")
	out, err := hashCmd.Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	blob := strings.TrimSpace(string(out))

	mktreeCmd := exec.Command("git", "-C", bare, "mktree")
	mktreeCmd.Stdin = strings.NewReader("100644 blob " + blob + "\ta.go\n")
	treeOut, err := mktreeCmd.Output()
	if err != nil {
		t.Fatalf("mktree: %v", err)
	}
	tree := strings.TrimSpace(string(treeOut))

	commitOut, err := exec.Command("git", "-C", bare, "commit-tree", tree, "-m", "base").Output()
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	head = strings.TrimSpace(string(commitOut))
	runButlerGit(t, bare, "update-ref", "refs/heads/main", head)
	return bare, head
}

func runButlerGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", full, err, out)
	}
}

// fetchCmdCount counts top-level `git fetch` processes recorded in a
// GIT_TRACE2_EVENT log at path: one "cmd_name" event per git process, so a
// fetch's own upload-pack child (its own cmd_name "upload-pack") never
// counts as a second fetch.
func fetchCmdCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace2 log %s: %v", path, err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Event string `json:"event"`
			Name  string `json:"name"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse trace2 line %q: %v", line, err)
		}
		if ev.Event == "cmd_name" && ev.Name == "fetch" {
			count++
		}
	}
	return count
}

// newButlerLedgerURL builds a fresh bare repo at a temp path to stand in for
// a Remote ledger's URL, separate from the scratch repo Remote syncs into.
func newButlerLedgerURL(t *testing.T) string {
	t.Helper()
	url := filepath.Join(t.TempDir(), "ledger.git")
	runButlerGit(t, "", "init", "--bare", "-q", url)
	runButlerGit(t, url, "config", "gc.auto", "0")
	return url
}

func runButlerGitOutput(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", full, err)
	}
	return out
}

// readyDispatcher builds a dispatch.Fake whose Run() reports a ready outcome
// carrying one filed-issue intent, the "clean run" shape ButlerSettle expects
// (internal/settle/butler_test.go's readyResult mirrors this). Still used by
// backend_ledger_test.go's TestRunButler_AgainstRemoteLedger; the rest of
// this file's own TestRunButler_* callers moved to internal/butler (issue
// #3990).
func readyDispatcher() *dispatch.Fake {
	d := dispatch.NewFake()
	d.RunResult = dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "butler-bugs", Status: outcome.StatusReady, Note: "swept"},
		},
		IssueIntentsFound: true,
		IssueIntents:      []string{`{"title":"bug found","body":"repro","dedupTerms":["a.go:Foo"]}`},
	}
	return d
}

// gitLogSubjects returns ref's commit subjects in refs, newest first, the
// same shape the issue's acceptance criterion inspects with `git log
// --format=%s`. Still used by backend_ledger_test.go's
// TestRunButler_AgainstRemoteLedger (issue #3990).
func gitLogSubjects(t *testing.T, repo, ref string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "log", "--format=%s", ref).Output()
	if err != nil {
		t.Fatalf("git log %s: %v", ref, err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// (k) cmdButler rejects a chore not named in BUTLER_CHORES, before ever
// touching the factory or issue tracker.
func TestCmdButler_RejectsChoreNotEnabled(t *testing.T) {
	lc := &launchContext{
		config:  config{schemaConfig: schemaConfig{codeForge: "local", butlerChores: "other-chore"}},
		cleanup: func() {},
	}
	code := cmdButler(lc, "bugs")
	if code != exitConfigInvalid {
		t.Errorf("cmdButler code = %d, want %d", code, exitConfigInvalid)
	}
}

// assertNoButlerLedgerClaims fails the test if repo holds any ref under
// ledger.RefPrefix -- proof a butlerPreflight rejection never reaches the
// Ledger claim (issue #3905).
func assertNoButlerLedgerClaims(t *testing.T, repo string) {
	t.Helper()
	out := runButlerGitOutput(t, repo, "for-each-ref", ledger.RefPrefix)
	if len(strings.TrimSpace(string(out))) != 0 {
		t.Errorf("for-each-ref %s = %q, want no claim refs", ledger.RefPrefix, out)
	}
}

// testButlerLaunchContext builds an otherwise-complete launchContext for a
// preflight-rejection test: every knob past butlerPreflight (BUTLER_EVERY,
// BUTLER_CLAIM_TIMEOUT, DAEMON_AWAKE_WINDOW, a real *dispatch.Factory) is
// filled in at its schema default, so that if the guard under test were
// missing, cmdButler would actually reach ledger.Claim instead of failing
// earlier at, say, parseButlerClaimTimeout on a zero-value config (issue
// #3905).
func testButlerLaunchContext(t *testing.T, repo, butlerChores string) *launchContext {
	t.Helper()
	return &launchContext{
		config: config{schemaConfig: schemaConfig{
			codeForge:                    "local",
			codeForgeAccumulationRepoDir: repo,
			butlerChores:                 butlerChores,
			butlerEvery:                  schemaDefault("BUTLER_EVERY"),
			butlerClaimTimeout:           schemaDefault("BUTLER_CLAIM_TIMEOUT"),
			daemonAwakeWindow:            schemaDefault("DAEMON_AWAKE_WINDOW"),
			baseBranch:                   schemaDefault("BASE_BRANCH"),
		}},
		factory: testFactory(t, t.TempDir(), runner.NewFake()),
		cleanup: func() {},
	}
}

// (k2) cmdButler rejects a malformed BUTLER_CHORES entry before ever writing
// a Ledger claim (issue #3905). "bad.name" is a legal git ref component (so a
// claim would be writable if the name-format guard were missing) but fails
// promptassembly.ValidChoreName, which is what butlerPreflight must catch
// first.
func TestCmdButler_RejectsMalformedChoreName(t *testing.T) {
	t.Setenv("FILER_MODEL", "test-model")
	repo, _ := newButlerTestRepo(t)
	lc := testButlerLaunchContext(t, repo, "bad.name")
	var code int
	stderr := captureStderrFile(t, func() { code = cmdButler(lc, "") })
	if code != exitConfigInvalid {
		t.Errorf("cmdButler code = %d, want %d", code, exitConfigInvalid)
	}
	if !strings.Contains(stderr, "invalid name format") {
		t.Errorf("stderr = %q, want it to name the invalid format", stderr)
	}
	assertNoButlerLedgerClaims(t, repo)
}

// (k3) cmdButler rejects an enabled chore absent from the baked CHORE_CATALOG
// before writing a Ledger claim, same guarantee as above (issue #3905).
func TestCmdButler_RejectsChoreMissingFromCatalog(t *testing.T) {
	t.Setenv("FILER_MODEL", "test-model")
	withLoadedDoc(t, &inputdoc.Document{Artifacts: map[string]string{"CHORE_CATALOG": "docs-drift"}})
	repo, _ := newButlerTestRepo(t)
	lc := testButlerLaunchContext(t, repo, "bugs")
	var code int
	stderr := captureStderrFile(t, func() { code = cmdButler(lc, "") })
	if code != exitConfigInvalid {
		t.Errorf("cmdButler code = %d, want %d", code, exitConfigInvalid)
	}
	if !strings.Contains(stderr, "prompt file missing") {
		t.Errorf("stderr = %q, want it to name the missing prompt", stderr)
	}
	assertNoButlerLedgerClaims(t, repo)
}

// cmdButler exits exitConfigInvalid, not 1, for a malformed BUTLER_EVERY,
// BUTLER_CLAIM_TIMEOUT, or DAEMON_AWAKE_WINDOW too, even though each fails
// past butlerPreflight: all three parse inside resolveButlerSettings, the
// same config-validation prelude, so their errors get the same treatment
// (issue #3920, keeps a config failure out of the daemon's shared breaker).
func TestCmdButler_RejectsMalformedButlerSettings(t *testing.T) {
	tests := []struct {
		name       string
		set        func(cfg *config)
		wantSubstr string
	}{
		{
			name:       "BUTLER_EVERY",
			set:        func(cfg *config) { cfg.butlerEvery = "not-a-duration" },
			wantSubstr: "BUTLER_EVERY",
		},
		{
			name:       "BUTLER_CLAIM_TIMEOUT",
			set:        func(cfg *config) { cfg.butlerClaimTimeout = "not-a-duration" },
			wantSubstr: "BUTLER_CLAIM_TIMEOUT",
		},
		{
			name:       "DAEMON_AWAKE_WINDOW",
			set:        func(cfg *config) { cfg.daemonAwakeWindow = "not-a-window" },
			wantSubstr: "DAEMON_AWAKE_WINDOW",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FILER_MODEL", "test-model")
			repo, _ := newButlerTestRepo(t)
			lc := testButlerLaunchContext(t, repo, "bugs")
			tt.set(&lc.config)
			var code int
			stderr := captureStderrFile(t, func() { code = cmdButler(lc, "") })
			if code != exitConfigInvalid {
				t.Errorf("cmdButler code = %d, want %d", code, exitConfigInvalid)
			}
			if !strings.Contains(stderr, tt.wantSubstr) {
				t.Errorf("stderr = %q, want it to name %s", stderr, tt.wantSubstr)
			}
			assertNoButlerLedgerClaims(t, repo)
		})
	}
}

// cmdButler rejects a BUTLER_EVERY override naming a Chore not in
// BUTLER_CHORES, so a misspelled key fails loudly instead of never firing.
func TestCmdButler_RejectsOverrideForChoreNotEnabled(t *testing.T) {
	t.Setenv("FILER_MODEL", "test-model")
	lc := &launchContext{
		config:  config{schemaConfig: schemaConfig{codeForge: "local", butlerChores: "bugs", butlerEvery: "6h bgus=1h"}},
		cleanup: func() {},
	}
	var code int
	stderr := captureStderrFile(t, func() { code = cmdButler(lc, "") })
	if code != exitConfigInvalid {
		t.Errorf("cmdButler code = %d, want %d", code, exitConfigInvalid)
	}
	if !strings.Contains(stderr, `override for chore "bgus"`) {
		t.Errorf("stderr = %q, want the rejected override named", stderr)
	}
}

// (h) cmdButler rejects a CODE_FORGE with no butler Ledger backend (issue
// #3876: only local, github, and forgejo have one).
func TestCmdButler_RejectsForgeWithNoLedger(t *testing.T) {
	lc := &launchContext{
		config:  config{schemaConfig: schemaConfig{codeForge: "git", butlerChores: "bugs"}},
		cleanup: func() {},
	}
	code := cmdButler(lc, "bugs")
	if code != exitConfigInvalid {
		t.Errorf("cmdButler code = %d, want %d", code, exitConfigInvalid)
	}
}

// (h2) A fresh Consumer -- BUTLER_CHORES left at its schema default, "" --
// never starts a butler run for any built-in Chore: cmdButler must refuse
// before ever touching lc.factory (left nil here), since a nil factory would
// panic on NewChore.
func TestCmdButler_FreshConsumerNeverStartsRun(t *testing.T) {
	if def := schemaDefault("BUTLER_CHORES"); def != "" {
		t.Fatalf("schemaDefault(BUTLER_CHORES) = %q, want \"\" (test assumes opt-in-only default)", def)
	}
	for _, choreName := range []string{"bugs", "refactor", "docs-drift"} {
		t.Run(choreName, func(t *testing.T) {
			lc := &launchContext{
				config:  config{schemaConfig: schemaConfig{codeForge: "local", butlerChores: schemaDefault("BUTLER_CHORES")}},
				cleanup: func() {},
			}
			code := cmdButler(lc, choreName)
			if code != exitConfigInvalid {
				t.Errorf("cmdButler(%q) code = %d, want %d", choreName, code, exitConfigInvalid)
			}
		})
	}
}

// (m) butlerPreflight guards codeForge, BUTLER_CHORES membership (or, with
// no --chore, BUTLER_CHORES being non-empty at all), BUTLER_CHORE_CLASSES
// syntax, and the Filer gate in that order, before cmdButler ever claims a
// Ledger -- in particular a chore run with no provisioned Filer
// (DRIVER=opencode, or FILER_MODEL="") must be refused up front rather than
// sweep and silently drop findings.
func TestButlerPreflight(t *testing.T) {
	cases := []struct {
		name               string
		codeForge          string
		butlerChores       string
		butlerChoreClasses string
		butlerMaxPerSweep  int
		butlerMaxPerDay    int
		chore              string
		filerEnabled       bool
		catalogNames       []string
		catalogKnown       bool
		wantErr            string // substring of the error, checked in guard order; "" means no error
	}{
		{"all clear", "local", "bugs", "bugs=error-handling", 0, 0, "bugs", true, nil, false, ""},
		{"github clear", "github", "bugs", "", 0, 0, "bugs", true, nil, false, ""},
		{"forgejo clear", "forgejo", "bugs", "", 0, 0, "bugs", true, nil, false, ""},
		{"forge with no ledger rejected", "git", "bugs", "", 0, 0, "bugs", true, nil, false, "cannot host a butler Ledger (supported: github, forgejo, local)"},
		{"chore not enabled", "local", "other-chore", "", 0, 0, "bugs", true, nil, false, "is not enabled"},
		{"malformed classes rejected", "local", "bugs", "bugs", 0, 0, "bugs", true, nil, false, "BUTLER_CHORE_CLASSES"},
		{"non-slug class rejected", "local", "bugs", "bugs=Error_Handling", 0, 0, "bugs", true, nil, false, "Error_Handling"},
		{"filer not provisioned", "local", "bugs", "", 0, 0, "bugs", false, nil, false, "needs a provisioned Filer"},
		{"forge checked before chore", "git", "other-chore", "", 0, 0, "bugs", false, nil, false, "cannot host a butler Ledger"},
		{"chore checked before filer", "local", "other-chore", "", 0, 0, "bugs", false, nil, false, "is not enabled"},
		{"classes checked before filer", "local", "bugs", "bugs", 0, 0, "bugs", false, nil, false, "BUTLER_CHORE_CLASSES"},
		{"chore with no classes entry passes", "local", "tidy-deps", "bugs=error-handling", 0, 0, "tidy-deps", true, nil, false, ""},
		{"class-list entry naming an unknown non-built-in chore rejected", "local", "bugs", "notachore=error-handling", 0, 0, "bugs", true, nil, false, "is not enabled and not a built-in chore"},
		{"dogfood-shaped config passes", "local", "bugs docs-drift", schemaDefault("BUTLER_CHORE_CLASSES"), 0, 0, "", true, nil, false, ""},
		{"no chore: all clear with chores enabled", "local", "bugs", "", 0, 0, "", true, nil, false, ""},
		{"no chore: empty BUTLER_CHORES rejected", "local", "", "", 0, 0, "", true, nil, false, "BUTLER_CHORES is empty"},
		{"no chore: filer checked after empty-chores guard", "local", "", "", 0, 0, "", false, nil, false, "BUTLER_CHORES is empty"},
		{"sweep exceeds day rejected", "local", "bugs", "", 6, 5, "bugs", true, nil, false, "BUTLER_MAX_FINDINGS_PER_SWEEP (6) exceeds BUTLER_MAX_FINDINGS_PER_DAY (5); no run could ever start"},
		{"sweep equals day ok", "local", "bugs", "", 5, 5, "bugs", true, nil, false, ""},
		{"day zero with sweep set ok", "local", "bugs", "", 5, 0, "bugs", true, nil, false, ""},
		{"bad --chore name rejected", "local", "bugs", "", 0, 0, "bad name!", true, nil, false, "invalid name format"},
		{"bad BUTLER_CHORES entry rejected", "local", "bad name!", "", 0, 0, "", true, nil, false, "invalid name format"},
		{"chore missing from catalog rejected", "local", "bugs", "", 0, 0, "bugs", true, []string{"docs-drift"}, true, "prompt file missing"},
		{"chore present in catalog passes", "local", "bugs", "", 0, 0, "bugs", true, []string{"bugs"}, true, ""},
		{"empty known catalog rejected", "local", "bugs", "", 0, 0, "bugs", true, nil, true, "prompt file missing"},
		{"catalog absent skips prompt check", "local", "bugs", "", 0, 0, "bugs", true, nil, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config{schemaConfig: schemaConfig{
				codeForge:                 tc.codeForge,
				butlerChores:              tc.butlerChores,
				butlerChoreClasses:        tc.butlerChoreClasses,
				butlerMaxFindingsPerSweep: tc.butlerMaxPerSweep,
				butlerMaxFindingsPerDay:   tc.butlerMaxPerDay,
			}}
			catalog := choreCatalog{names: tc.catalogNames, known: tc.catalogKnown}
			_, err := butlerPreflight(cfg, tc.chore, tc.filerEnabled, catalog)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("butlerPreflight(%+v): %v", tc, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("butlerPreflight(%+v) err = %v, want substring %q", tc, err, tc.wantErr)
			}
			if tc.name == "non-slug class rejected" && !strings.Contains(err.Error(), signalwire.ClassRule) {
				t.Errorf("butlerPreflight(%+v) err = %v, want it to contain signalwire.ClassRule %q", tc, err, signalwire.ClassRule)
			}
		})
	}
}

// (m2) SPINDRIFT_PROMPT_DIR, when it names a real directory, overrides the
// baked CHORE_CATALOG entirely for the prompt-exists check -- matching
// internal/runner/mount.go's own candidateMount treatment of the same knob
// (issue #3905).
func TestButlerPreflight_PromptDirOverride(t *testing.T) {
	cfgWith := func(promptDir string) config {
		return config{schemaConfig: schemaConfig{
			codeForge:          "local",
			butlerChores:       "bugs",
			spindriftPromptDir: promptDir,
		}}
	}
	// A catalog that would reject "bugs" on its own, to prove the override
	// directory -- not the catalog -- decides the outcome when it applies.
	rejectingCatalog := choreCatalog{names: []string{"docs-drift"}, known: true}

	t.Run("override dir with prompt present passes", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "chores"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "chores", "bugs.md"), []byte("prompt"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := butlerPreflight(cfgWith(dir), "bugs", true, rejectingCatalog); err != nil {
			t.Errorf("butlerPreflight: %v, want nil", err)
		}
	})

	t.Run("override dir missing prompt names the path", func(t *testing.T) {
		dir := t.TempDir()
		_, err := butlerPreflight(cfgWith(dir), "bugs", true, rejectingCatalog)
		wantPath := filepath.Join(dir, "chores", "bugs.md")
		if err == nil || !strings.Contains(err.Error(), "prompt file missing") || !strings.Contains(err.Error(), wantPath) {
			t.Errorf("butlerPreflight err = %v, want it to name %q", err, wantPath)
		}
	})

	t.Run("override dir with prompt path a directory is rejected", func(t *testing.T) {
		dir := t.TempDir()
		// promptassembly.choreSection reads the prompt file with os.ReadFile,
		// which fails on a directory -- butlerPreflight must reject this
		// before the claim, not let os.Stat's IsDir-agnostic success through
		// (issue #3905).
		if err := os.MkdirAll(filepath.Join(dir, "chores", "bugs.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := butlerPreflight(cfgWith(dir), "bugs", true, rejectingCatalog)
		if err == nil || !strings.Contains(err.Error(), "prompt file missing") {
			t.Errorf("butlerPreflight err = %v, want prompt file missing", err)
		}
	})

	t.Run("override dir set but not a directory falls back to the catalog", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := butlerPreflight(cfgWith(file), "bugs", true, rejectingCatalog)
		if err == nil || !strings.Contains(err.Error(), "prompt file missing") || !strings.Contains(err.Error(), "CHORE_CATALOG") {
			t.Errorf("butlerPreflight err = %v, want the catalog-based error", err)
		}
	})
}

// (n) parseButlerArgs: --chore is optional (an empty return means "pick a
// due Chore"); --no-build is the one other flag butler shares with
// dispatch/research.
func TestParseButlerArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantChore string
		wantNo    bool
		wantErr   bool
	}{
		{"chore only", []string{"--chore", "bugs"}, "bugs", false, false},
		{"chore plus no-build", []string{"--chore", "bugs", "--no-build"}, "bugs", true, false},
		{"no-build before chore", []string{"--no-build", "--chore", "bugs"}, "bugs", true, false},
		{"no args at all: pick a due chore", []string{}, "", false, false},
		{"no-build alone: pick a due chore", []string{"--no-build"}, "", true, false},
		{"--chore with no value", []string{"--chore"}, "", false, true},
		{"unrecognized token", []string{"bogus"}, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			choreName, noBuild, err := parseButlerArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseButlerArgs(%v): got nil error, want one", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseButlerArgs(%v): %v", tc.args, err)
			}
			if choreName != tc.wantChore || noBuild != tc.wantNo {
				t.Errorf("parseButlerArgs(%v) = (%q, %v), want (%q, %v)", tc.args, choreName, noBuild, tc.wantChore, tc.wantNo)
			}
		})
	}
}

// chore.DefaultEvery must match BUTLER_EVERY's schema default, or an unset
// value and an overrides-only value would get different intervals.
func TestButlerEveryDefaultMatchesSchema(t *testing.T) {
	for _, f := range schemaFlags {
		if f.env != "BUTLER_EVERY" {
			continue
		}
		if d, err := time.ParseDuration(f.dflt); err != nil || d != chore.DefaultEvery {
			t.Errorf("schema default %q, want %v (chore.DefaultEvery)", f.dflt, chore.DefaultEvery)
		}
		return
	}
	t.Fatal("BUTLER_EVERY missing from schemaFlags")
}

func TestParseButlerClaimTimeout(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{"valid duration", "6h", 6 * time.Hour, false},
		{"short valid duration", "30m", 30 * time.Minute, false},
		{"zero is rejected", "0s", 0, true},
		{"negative is rejected", "-1h", 0, true},
		{"unparseable is rejected", "bogus", 0, true},
		{"empty is rejected", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseButlerClaimTimeout(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseButlerClaimTimeout(%q): got nil error, want one", tc.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseButlerClaimTimeout(%q): %v", tc.value, err)
			}
			if got != tc.want {
				t.Errorf("parseButlerClaimTimeout(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// (t) promotionPolicy's Room re-walks the Ledger fresh each call (unlike
// runButler's shared Snapshot, per its own doc comment), but still costs
// exactly one fetch for that one call, however many Chores are enabled --
// ledger.Snapshot inside Room, not a raw DayTotalsAll(backend, ...) that
// would sync once per enabled Chore (issue #3918).
func TestPromotionPolicy_RoomFetchesOnceAcrossEnabledChores(t *testing.T) {
	ledgerURL := newButlerLedgerURL(t)

	remote, err := ledger.NewRemote(t.TempDir(), ledgerURL)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}

	policy := testButlerPolicy(noEvery, "bugs", "docs-drift")
	policy.maxPromotionsPerDay = 1
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pp := policy.promotionPolicy(remote, policy.chores[0], func() time.Time { return now }) // chores[0] is "bugs"

	trace := filepath.Join(t.TempDir(), "trace2.log")
	t.Setenv("GIT_TRACE2_EVENT", trace)

	if got := pp.Room(); got != 1 {
		t.Errorf("Room() = %d, want 1 (no promotions yet)", got)
	}
	if got := fetchCmdCount(t, trace); got != 1 {
		t.Errorf("fetch count = %d, want exactly 1 for one Room() call", got)
	}
}
