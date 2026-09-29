package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/butler"
	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/signalwire"
)

func runButlerGitOutput(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", full, err)
	}
	return out
}

// TestButlerOutcomeErr pins the stderr text and exit code `spindrift butler`
// reports for each Sweep Outcome; the Daemon backs off on exit 2.
func TestButlerOutcomeErr(t *testing.T) {
	cases := []struct {
		name     string
		outcome  butler.Outcome
		wantErr  string // "" means nil
		wantCode int
	}{
		{
			name:     "NotDue joins every candidate's reason and wraps errQueueEmpty",
			outcome:  butler.Outcome{Kind: butler.NotDue, Reasons: []string{`chore "bugs" not due: interval not elapsed`, `chore "docs-drift" not due: budget spent`}},
			wantErr:  `butler: chore "bugs" not due: interval not elapsed; chore "docs-drift" not due: budget spent: queue empty`,
			wantCode: 2,
		},
		{
			name:     "LostRace names the chore and wraps errQueueEmpty",
			outcome:  butler.Outcome{Kind: butler.LostRace, Chore: "bugs"},
			wantErr:  `butler: chore "bugs" claimed by another run first: queue empty`,
			wantCode: 2,
		},
		{
			name:     "ClaimLeft names the chore, not wrapped in errQueueEmpty",
			outcome:  butler.Outcome{Kind: butler.ClaimLeft, Chore: "bugs"},
			wantErr:  `butler: chore "bugs" run did not complete (claim left standing)`,
			wantCode: 1,
		},
		{
			name:     "Swept prints nothing and exits 0",
			outcome:  butler.Outcome{Kind: butler.Swept, Chore: "bugs", Filed: 2, Promoted: 1},
			wantErr:  "",
			wantCode: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := butlerOutcomeErr(tc.outcome)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("butlerOutcomeErr(%+v) = %v, want nil", tc.outcome, err)
				}
			} else if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("butlerOutcomeErr(%+v) = %v, want %q", tc.outcome, err, tc.wantErr)
			}
			if code := exitCodeFor(err); code != tc.wantCode {
				t.Errorf("exitCodeFor(butlerOutcomeErr(%+v)) = %d, want %d", tc.outcome, code, tc.wantCode)
			}
		})
	}
}

// fakeBranchPusher/fakeDraftPRCreator are minimal stand-ins for
// forge.BranchPusher/forge.DraftPRCreator -- just enough to populate
// Capabilities and prove butlerPatchForge's gating and field wiring, not to
// exercise the push/draft-PR mechanics themselves (issue #4074).
type fakeBranchPusher struct{}

func (fakeBranchPusher) PushBranch(srcDir, localRef, branch string) error { return nil }

type fakeDraftPRCreator struct{}

func (fakeDraftPRCreator) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	return "", false, nil
}

// fakeIssueLabeler is a minimal stand-in for forge.IssueLabeler, the third
// leg butlerPatchForge requires alongside push+draft-PR (issue #4074).
type fakeIssueLabeler struct{}

func (fakeIssueLabeler) AddLabels(num string, labels []string) error { return nil }

// pushDraftForge wraps a forge.CodeForge with the BranchPusher and
// DraftPRCreator methods a write-capable adapter would carry, so
// forge.ResolveCapabilities can populate Capabilities.BranchPusher/
// DraftPRCreator off a real type assertion rather than a hand-built
// Capabilities value (issue #4074's wiring subtest below).
type pushDraftForge struct {
	forge.CodeForge
	fakeBranchPusher
	fakeDraftPRCreator
}

// butlerPatchForge opts the patch rung on only when Capabilities proves the
// resolved CodeForge can both push a branch and open a draft PR host-side
// (forge.Capabilities.HostCanOpenPR), the resolved IssueTracker can add
// labels to an already-filed issue (forge.Capabilities.IssueLabeler), and
// the tracker and forge descriptors name the same backend, so the issue
// number both "Closes #N" and "agent/issue-N" carry actually names an issue
// on that forge (issue #4074); any one of the four missing keeps the rung
// off exactly as WithPatchForge(nil, nil) does.
func TestButlerPatchForge(t *testing.T) {
	cf := forge.NewFake()
	cf.BranchPrefix = "agent/issue-"

	t.Run("neither capability", func(t *testing.T) {
		if pf := butlerPatchForge(cf, forge.Capabilities{}); pf != nil {
			t.Fatalf("butlerPatchForge = %v, want nil", pf)
		}
	})

	t.Run("push only", func(t *testing.T) {
		caps := forge.Capabilities{BranchPusher: fakeBranchPusher{}}
		if pf := butlerPatchForge(cf, caps); pf != nil {
			t.Fatalf("butlerPatchForge = %v, want nil", pf)
		}
	})

	t.Run("draft PR only", func(t *testing.T) {
		caps := forge.Capabilities{DraftPRCreator: fakeDraftPRCreator{}}
		if pf := butlerPatchForge(cf, caps); pf != nil {
			t.Fatalf("butlerPatchForge = %v, want nil", pf)
		}
	})

	t.Run("push and draft PR, no issue labeler", func(t *testing.T) {
		caps := forge.Capabilities{BranchPusher: fakeBranchPusher{}, DraftPRCreator: fakeDraftPRCreator{}}
		if pf := butlerPatchForge(cf, caps); pf != nil {
			t.Fatalf("butlerPatchForge = %v, want nil: a tracker with no IssueLabeler must never opt the rung on", pf)
		}
	})

	githubDesc, _ := backend.ByName("github")
	forgejoDesc, _ := backend.ByName("forgejo")

	t.Run("both capabilities", func(t *testing.T) {
		caps := forge.Capabilities{
			BranchPusher: fakeBranchPusher{}, DraftPRCreator: fakeDraftPRCreator{}, IssueLabeler: fakeIssueLabeler{},
			ForgeDescriptor: githubDesc, TrackerDescriptor: githubDesc,
		}
		pf := butlerPatchForge(cf, caps)
		if pf == nil {
			t.Fatal("butlerPatchForge = nil, want a non-nil PatchForge")
		}
		if got, want := pf.AgentBranch("42"), "agent/issue-42"; got != want {
			t.Errorf("AgentBranch(42) = %q, want %q", got, want)
		}
		if err := pf.PushBranch("dir", "ref", "branch"); err != nil {
			t.Errorf("PushBranch: %v", err)
		}
		if _, _, err := pf.CreateDraftPR("t", "b", "base", "head"); err != nil {
			t.Errorf("CreateDraftPR: %v", err)
		}
		if err := pf.AddLabels("42", []string{"ready-for-agent"}); err != nil {
			t.Errorf("AddLabels: %v", err)
		}
	})

	// (issue #4074) a github CODE_FORGE paired with a forgejo ISSUE_TRACKER
	// is a valid pairing generally, but the two backends' issue numbers are
	// foreign namespaces: a finding filed on forgejo as issue N shares no
	// relationship with GitHub issue N. Even though both capabilities are
	// otherwise present, the rung must stay off, or the draft PR's "Closes
	// #N" and its "agent/issue-N" branch would name the wrong issue.
	t.Run("mismatched tracker and forge namespaces", func(t *testing.T) {
		caps := forge.Capabilities{
			BranchPusher: fakeBranchPusher{}, DraftPRCreator: fakeDraftPRCreator{}, IssueLabeler: fakeIssueLabeler{},
			ForgeDescriptor: githubDesc, TrackerDescriptor: forgejoDesc,
		}
		if pf := butlerPatchForge(cf, caps); pf != nil {
			t.Fatalf("butlerPatchForge = %v, want nil: forge and tracker descriptors name different backends", pf)
		}
	})

	// (wiring) ISSUE_TRACKER=local paired with a push+draft-PR-capable
	// CODE_FORGE: the local tracker's PostIssue returns "local:"+slug, not a
	// forge issue number, so it implements no IssueLabeler. Resolving real
	// Capabilities off that pair must come out nil here, not a hand-built
	// Capabilities value that could paper over a missing IssueLabeler.
	t.Run("wiring: local tracker never resolves an issue labeler", func(t *testing.T) {
		pushCf := pushDraftForge{CodeForge: cf}
		it := forge.NewFake().AsLocalIssueFiler()
		caps := forge.ResolveCapabilities(pushCf, it, backend.Descriptor{}, backend.Descriptor{})
		if caps.IssueLabeler != nil {
			t.Fatalf("caps.IssueLabeler = %v, want nil: the local tracker implements no IssueLabeler", caps.IssueLabeler)
		}
		if pf := butlerPatchForge(pushCf, caps); pf != nil {
			t.Fatalf("butlerPatchForge = %v, want nil", pf)
		}
	})

	// (wiring) forgejo paired with forgejo: same backend on both axes, so
	// the namespace leg passes and only the two capability legs matter.
	// AsForgejoShaped's AddLabels (promoted like the real adapter's) proves
	// the wiring resolves a real IssueLabeler, not a hand-built one.
	t.Run("wiring: forgejo tracker paired with forgejo forge resolves", func(t *testing.T) {
		pushCf := pushDraftForge{CodeForge: cf}
		it := forge.NewFake().AsForgejoShaped()
		caps := forge.ResolveCapabilities(pushCf, it, forgejoDesc, forgejoDesc)
		if caps.IssueLabeler == nil {
			t.Fatal("caps.IssueLabeler = nil, want the forgejo-shaped fake's AddLabels")
		}
		if pf := butlerPatchForge(pushCf, caps); pf == nil {
			t.Fatal("butlerPatchForge = nil, want a non-nil PatchForge")
		}
	})

	// (wiring) github forge paired with a forgejo-shaped tracker: proves the
	// namespace leg alone blocks the rung even when both capability legs
	// resolve for real off ResolveCapabilities, not a hand-built Capabilities.
	t.Run("wiring: mismatched real capabilities never resolve", func(t *testing.T) {
		pushCf := pushDraftForge{CodeForge: cf}
		it := forge.NewFake().AsForgejoShaped()
		caps := forge.ResolveCapabilities(pushCf, it, githubDesc, forgejoDesc)
		if caps.IssueLabeler == nil {
			t.Fatal("caps.IssueLabeler = nil, want the forgejo-shaped fake's AddLabels")
		}
		if pf := butlerPatchForge(pushCf, caps); pf != nil {
			t.Fatalf("butlerPatchForge = %v, want nil: forge and tracker descriptors name different backends", pf)
		}
	})
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
	repo := ledgertest.NewRepo(t)
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
	repo := ledgertest.NewRepo(t)
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
			repo := ledgertest.NewRepo(t)
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
// syntax, the BUTLER_PATCH_CLASSES cross-checks (ADR 0057, only when
// butlerMaxPatchesPerDay > 0), and the Filer gate in that order, before
// cmdButler ever claims a Ledger -- in particular a chore run with no
// provisioned Filer (DRIVER=opencode, or FILER_MODEL="") must be refused up
// front rather than sweep and silently drop findings.
func TestButlerPreflight(t *testing.T) {
	cases := []struct {
		name                   string
		codeForge              string
		butlerChores           string
		butlerChoreClasses     string
		butlerMaxPerSweep      int
		butlerMaxPerDay        int
		butlerPatchClasses     string
		butlerMaxPatchesPerDay int
		chore                  string
		filerEnabled           bool
		catalogNames           []string
		catalogKnown           bool
		wantErr                string // substring of the error, checked in guard order; "" means no error
	}{
		{"all clear", "local", "bugs", "bugs=error-handling", 0, 0, "", 0, "bugs", true, nil, false, ""},
		{"github clear", "github", "bugs", "", 0, 0, "", 0, "bugs", true, nil, false, ""},
		{"forgejo clear", "forgejo", "bugs", "", 0, 0, "", 0, "bugs", true, nil, false, ""},
		{"forge with no ledger rejected", "git", "bugs", "", 0, 0, "", 0, "bugs", true, nil, false, "cannot host a butler Ledger (supported: github, forgejo, local)"},
		{"chore not enabled", "local", "other-chore", "", 0, 0, "", 0, "bugs", true, nil, false, "is not enabled"},
		{"malformed classes rejected", "local", "bugs", "bugs", 0, 0, "", 0, "bugs", true, nil, false, "BUTLER_CHORE_CLASSES"},
		{"non-slug class rejected", "local", "bugs", "bugs=Error_Handling", 0, 0, "", 0, "bugs", true, nil, false, "Error_Handling"},
		{"filer not provisioned", "local", "bugs", "", 0, 0, "", 0, "bugs", false, nil, false, "needs a provisioned Filer"},
		{"forge checked before chore", "git", "other-chore", "", 0, 0, "", 0, "bugs", false, nil, false, "cannot host a butler Ledger"},
		{"chore checked before filer", "local", "other-chore", "", 0, 0, "", 0, "bugs", false, nil, false, "is not enabled"},
		{"classes checked before filer", "local", "bugs", "bugs", 0, 0, "", 0, "bugs", false, nil, false, "BUTLER_CHORE_CLASSES"},
		{"chore with no classes entry passes", "local", "tidy-deps", "bugs=error-handling", 0, 0, "", 0, "tidy-deps", true, nil, false, ""},
		{"class-list entry naming an unknown non-built-in chore rejected", "local", "bugs", "notachore=error-handling", 0, 0, "", 0, "bugs", true, nil, false, "is not enabled and not a built-in chore"},
		{"dogfood-shaped config passes", "local", "bugs docs-drift", schemaDefault("BUTLER_CHORE_CLASSES"), 0, 0, "", 0, "", true, nil, false, ""},
		{"no chore: all clear with chores enabled", "local", "bugs", "", 0, 0, "", 0, "", true, nil, false, ""},
		{"no chore: empty BUTLER_CHORES rejected", "local", "", "", 0, 0, "", 0, "", true, nil, false, "BUTLER_CHORES is empty"},
		{"no chore: filer checked after empty-chores guard", "local", "", "", 0, 0, "", 0, "", false, nil, false, "BUTLER_CHORES is empty"},
		{"sweep exceeds day rejected", "local", "bugs", "", 6, 5, "", 0, "bugs", true, nil, false, "BUTLER_MAX_FINDINGS_PER_SWEEP (6) exceeds BUTLER_MAX_FINDINGS_PER_DAY (5); no run could ever start"},
		{"sweep equals day ok", "local", "bugs", "", 5, 5, "", 0, "bugs", true, nil, false, ""},
		{"day zero with sweep set ok", "local", "bugs", "", 5, 0, "", 0, "bugs", true, nil, false, ""},
		{"bad --chore name rejected", "local", "bugs", "", 0, 0, "", 0, "bad name!", true, nil, false, "invalid name format"},
		{"bad BUTLER_CHORES entry rejected", "local", "bad name!", "", 0, 0, "", 0, "", true, nil, false, "invalid name format"},
		{"chore missing from catalog rejected", "local", "bugs", "", 0, 0, "", 0, "bugs", true, []string{"docs-drift"}, true, "prompt file missing"},
		{"chore present in catalog passes", "local", "bugs", "", 0, 0, "", 0, "bugs", true, []string{"bugs"}, true, ""},
		{"empty known catalog rejected", "local", "bugs", "", 0, 0, "", 0, "bugs", true, nil, true, "prompt file missing"},
		{"catalog absent skips prompt check", "local", "bugs", "", 0, 0, "", 0, "bugs", true, nil, false, ""},
		{"patch rung off ignores a malformed BUTLER_PATCH_CLASSES", "local", "bugs", "", 0, 0, "bugs", 0, "bugs", true, nil, false, ""},
		{"patch class outside promotion classes rejected", "local", "bugs", "bugs=error-handling", 0, 0, "bugs=resource-leak", 1, "bugs", true, nil, false, `chore "bugs": class "resource-leak" is not on its BUTLER_CHORE_CLASSES allow-list`},
		{"patch class for disabled chore rejected", "local", "bugs", "bugs=error-handling", 0, 0, "docs-drift=stale-reference", 1, "bugs", true, nil, false, `chore "docs-drift" is not enabled`},
		{"patch rung on with allow-listed class passes", "local", "bugs", "bugs=error-handling", 0, 0, "bugs=error-handling", 1, "bugs", true, nil, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config{schemaConfig: schemaConfig{
				codeForge:                 tc.codeForge,
				butlerChores:              tc.butlerChores,
				butlerChoreClasses:        tc.butlerChoreClasses,
				butlerMaxFindingsPerSweep: tc.butlerMaxPerSweep,
				butlerMaxFindingsPerDay:   tc.butlerMaxPerDay,
				butlerPatchClasses:        tc.butlerPatchClasses,
				butlerMaxPatchesPerDay:    tc.butlerMaxPatchesPerDay,
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
// due Chore") but, when given, takes one non-empty name at most once;
// --no-build is the one other flag butler shares with dispatch/research.
func TestParseButlerArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantChore string
		wantNo    bool
		wantErr   string // substring of the expected error; "" means none
	}{
		{"chore only", []string{"--chore", "bugs"}, "bugs", false, ""},
		{"chore plus no-build", []string{"--chore", "bugs", "--no-build"}, "bugs", true, ""},
		{"no-build before chore", []string{"--no-build", "--chore", "bugs"}, "bugs", true, ""},
		{"no args at all: pick a due chore", []string{}, "", false, ""},
		{"no-build alone: pick a due chore", []string{"--no-build"}, "", true, ""},
		{"--chore with no value", []string{"--chore"}, "", false, "flag --chore requires a non-empty value"},
		{"--chore with empty value", []string{"--chore", ""}, "", false, "flag --chore requires a non-empty value"},
		{"--chore swallowing a flag", []string{"--chore", "-x"}, "", false, "flag --chore requires a chore name"},
		{"--chore given twice", []string{"--chore", "bugs", "--chore", "refactor"}, "", false, "flag --chore given more than once"},
		{"unrecognized token", []string{"bogus"}, "", false, "unrecognized argument: bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			choreName, noBuild, err := parseButlerArgs(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseButlerArgs(%v): err = %v, want one containing %q", tc.args, err, tc.wantErr)
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

// The two host promotion knobs (BUTLER_MAX_PROMOTIONS_PER_DAY,
// BUTLER_PROMOTION_MAX_FILES) resolve through the generated schemaFlags
// table and loadSchemaConfig, the same wiring every other schema knob uses
// (issue #3880).
func TestButlerPromotionKnobsParseFromSchema(t *testing.T) {
	for _, tc := range []struct {
		env  string
		dflt string
	}{
		{"BUTLER_MAX_PROMOTIONS_PER_DAY", "0"},
		{"BUTLER_PROMOTION_MAX_FILES", "3"},
	} {
		found := false
		for _, f := range schemaFlags {
			if f.env != tc.env {
				continue
			}
			found = true
			if f.dflt != tc.dflt {
				t.Errorf("%s default = %q, want %q", tc.env, f.dflt, tc.dflt)
			}
		}
		if !found {
			t.Errorf("%s missing from schemaFlags", tc.env)
		}
	}

	t.Setenv("BUTLER_MAX_PROMOTIONS_PER_DAY", "7")
	t.Setenv("BUTLER_PROMOTION_MAX_FILES", "9")
	cfg := loadSchemaConfig()
	if cfg.butlerMaxPromotionsPerDay != 7 {
		t.Errorf("butlerMaxPromotionsPerDay = %d, want 7", cfg.butlerMaxPromotionsPerDay)
	}
	if cfg.butlerPromotionMaxFiles != 9 {
		t.Errorf("butlerPromotionMaxFiles = %d, want 9", cfg.butlerPromotionMaxFiles)
	}
}

// The two host patch knobs (BUTLER_MAX_PATCHES_PER_DAY, BUTLER_PATCH_CLASSES)
// resolve through the generated schemaFlags table and loadSchemaConfig, the
// same wiring every other schema knob uses (ADR 0057).
func TestButlerPatchKnobsParseFromSchema(t *testing.T) {
	for _, tc := range []struct {
		env  string
		dflt string
	}{
		{"BUTLER_MAX_PATCHES_PER_DAY", "0"},
		{"BUTLER_PATCH_CLASSES", "docs-drift=stale-reference"},
	} {
		found := false
		for _, f := range schemaFlags {
			if f.env != tc.env {
				continue
			}
			found = true
			if f.dflt != tc.dflt {
				t.Errorf("%s default = %q, want %q", tc.env, f.dflt, tc.dflt)
			}
		}
		if !found {
			t.Errorf("%s missing from schemaFlags", tc.env)
		}
	}

	t.Setenv("BUTLER_MAX_PATCHES_PER_DAY", "4")
	t.Setenv("BUTLER_PATCH_CLASSES", "bugs=error-handling")
	cfg := loadSchemaConfig()
	if cfg.butlerMaxPatchesPerDay != 4 {
		t.Errorf("butlerMaxPatchesPerDay = %d, want 4", cfg.butlerMaxPatchesPerDay)
	}
	if cfg.butlerPatchClasses != "bugs=error-handling" {
		t.Errorf("butlerPatchClasses = %q, want %q", cfg.butlerPatchClasses, "bugs=error-handling")
	}
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

// promotableRunFunc builds a runner.Fake.RunFunc that reports one finding
// clearing every promotion gate (allow-listed class, one file, non-blank
// concurrence) -- the real-Dispatch, log-carrier equivalent of internal/
// butler's own promotableDispatcher fixture, since cmdButler always drives a
// real *dispatch.Dispatch (via lc.factory.NewChore), never a fake Dispatcher
// (issue #3993).
func promotableRunFunc(class string) func(runner.Box) error {
	return func(box runner.Box) error {
		nonce := box.Env["RUN_NONCE"]
		outcomeLine := outcome.Outcome{Landing: "none", Status: outcome.StatusReady, Note: "swept"}.Line()
		if _, err := box.Output.Write([]byte(outcomeLine + "\n")); err != nil {
			return err
		}
		payload := fmt.Sprintf(`{"title":"bug found","body":"repro","dedupTerms":["a.go:Foo"],"class":%q,"concurrence":"agreed"}`, class)
		encoded := base64.StdEncoding.EncodeToString([]byte(payload))
		line := fmt.Sprintf("%s %s %s\n", outcome.IssueIntentToken, nonce, encoded)
		_, err := box.Output.Write([]byte(line))
		return err
	}
}

// cmdButler hands a promoted finding the operator's configured work label
// through lc.config.workLabel (issue #3993, #4054). Every host-side gate
// clears: "bugs" is allow-listed for class "error-handling", the finding
// names one file, it carries a reviewer concurrence, and
// BUTLER_MAX_PROMOTIONS_PER_DAY leaves room -- so the filed issue carries
// both the configured label and the standing "agent-butler-finding"
// provenance label.
func TestCmdButler_PromotesFindingWithConfiguredWorkLabel(t *testing.T) {
	t.Setenv("FILER_MODEL", "test-model")
	repo := ledgertest.NewRepo(t)

	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/9301"

	fr := runner.NewFake()
	fr.RunFunc = promotableRunFunc("error-handling")

	lc := &launchContext{
		config: config{schemaConfig: schemaConfig{
			codeForge:                    "local",
			codeForgeAccumulationRepoDir: repo,
			butlerChores:                 "bugs",
			butlerChoreClasses:           "bugs=error-handling",
			butlerEvery:                  schemaDefault("BUTLER_EVERY"),
			butlerClaimTimeout:           schemaDefault("BUTLER_CLAIM_TIMEOUT"),
			daemonAwakeWindow:            schemaDefault("DAEMON_AWAKE_WINDOW"),
			baseBranch:                   schemaDefault("BASE_BRANCH"),
			butlerMaxPromotionsPerDay:    1,
			butlerPromotionMaxFiles:      3,
		}, workLabel: "agent-go"},
		factory:      testFactory(t, t.TempDir(), fr),
		issueTracker: fc.AsIssueFiler(),
		// This test never runs the patch rung (no PatchForge is wired), so
		// the Fake's SettleAdopted is never called; it only needs to satisfy
		// butler.PatchGate for lc.patchGate() (issue #4076).
		settle:  settle.NewFake(),
		cleanup: func() {},
	}

	code := cmdButler(lc, "bugs")
	if code != 0 {
		t.Fatalf("cmdButler code = %d, want 0", code)
	}
	if len(fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
	}
	got := fc.PostIssueCalls[0].Labels
	if !slices.Contains(got, "agent-go") {
		t.Errorf("labels = %v, want agent-go (the configured work label)", got)
	}
	if !slices.Contains(got, "agent-butler-finding") {
		t.Errorf("labels = %v, want agent-butler-finding", got)
	}
}
