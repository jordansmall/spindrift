//go:build integration

package promptassembly_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/seamtest"
)

// These tests pin the bytes of the Nix-rendered prompts (the seam fixtures'
// "prompts" directory), the same obligations tests/prompt.bats used to pin
// with grep/sed. They read files only and spawn no process.

func pbPrompt(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(seamtest.Path(t, "prompts"), name))
	if err != nil {
		t.Fatalf("read prompt %s: %v", name, err)
	}
	return string(b)
}

func pbFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(seamtest.Path(t, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

// pbSedRange mirrors `sed -n '/start/,/end/p'`: each range opens on a line
// matching start and closes on the first later line matching end (inclusive),
// or runs to EOF when end is "" or never matches. Ranges repeat. The end
// pattern is only tried on lines after the one that opened the range.
func pbSedRange(text, start, end string) string {
	startRE := regexp.MustCompile(start)
	var endRE *regexp.Regexp
	if end != "" {
		endRE = regexp.MustCompile(end)
	}
	var out []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		if !in {
			if !startRE.MatchString(line) {
				continue
			}
			in = true
			out = append(out, line)
			continue
		}
		out = append(out, line)
		if endRE != nil && endRE.MatchString(line) {
			in = false
		}
	}
	return strings.Join(out, "\n")
}

func pbSection(t *testing.T, text, start, end string) string {
	t.Helper()
	s := pbSedRange(text, start, end)
	if strings.TrimSpace(s) == "" {
		t.Fatalf("section /%s/,/%s/ is empty", start, end)
	}
	return s
}

// pbOutcomeSection is issue-prompt.md's OUTCOME section (issue #1901); several
// cases assert on this slice, so the anchors live in one place.
func pbOutcomeSection(t *testing.T) string {
	t.Helper()
	return pbSection(t, pbPrompt(t, "issue-prompt.md"), `^# OUTCOME$`, `^# IF BLOCKED$`)
}

// pbMatch asserts each BRE-style pattern matches some line (grep -q).
func pbMatch(t *testing.T, text string, pats ...string) {
	t.Helper()
	for _, p := range pats {
		if !regexp.MustCompile("(?m)" + p).MatchString(text) {
			t.Errorf("no line matches /%s/", p)
		}
	}
}

// pbMatchFold is pbMatch case-insensitively (grep -qi).
func pbMatchFold(t *testing.T, text string, pats ...string) {
	t.Helper()
	for _, p := range pats {
		if !regexp.MustCompile("(?mi)" + p).MatchString(text) {
			t.Errorf("no line matches /%s/ (case-insensitive)", p)
		}
	}
}

// pbContains asserts each literal appears in text (grep -qF).
func pbContains(t *testing.T, text string, lits ...string) {
	t.Helper()
	for _, l := range lits {
		if !strings.Contains(text, l) {
			t.Errorf("missing literal %q", l)
		}
	}
}

func pbNoMatch(t *testing.T, text string, pats ...string) {
	t.Helper()
	for _, p := range pats {
		if regexp.MustCompile("(?m)" + p).MatchString(text) {
			t.Errorf("unexpected line matches /%s/", p)
		}
	}
}

func pbNoMatchFold(t *testing.T, text string, pats ...string) {
	t.Helper()
	for _, p := range pats {
		if regexp.MustCompile("(?mi)" + p).MatchString(text) {
			t.Errorf("unexpected line matches /%s/ (case-insensitive)", p)
		}
	}
}

func pbNoContains(t *testing.T, text string, lits ...string) {
	t.Helper()
	for _, l := range lits {
		if strings.Contains(text, l) {
			t.Errorf("unexpected literal %q", l)
		}
	}
}

func TestPromptBytes_ScoutForbidsMidTurnNarration(t *testing.T) {
	pbMatchFold(t, pbPrompt(t, "scout-prompt.md"), `between tool calls`)
}

func TestPromptBytes_ReviewForbidsMidTurnNarration(t *testing.T) {
	pbMatchFold(t, pbPrompt(t, "review-prompt.md"), `between tool calls`)
}

func TestPromptBytes_ReviewCapsFindingAtOneLine(t *testing.T) {
	pbMatchFold(t, pbPrompt(t, "review-prompt.md"), `one line`)
}

// The implementor rebases against origin/BASE_BRANCH; the reviewer must diff
// against the same fetched ref, not a possibly-stale local one.
func TestPromptBytes_ReviewFetchesBeforeDiffingOriginBase(t *testing.T) {
	prompt := pbPrompt(t, "review-prompt.md")
	pbMatch(t, prompt, `git fetch origin`)
	pbContains(t, prompt,
		"git diff origin/${BASE_BRANCH}...HEAD",
		"git log origin/${BASE_BRANCH}..HEAD")
}

func TestPromptBytes_ConflictResolveForbidsMidTurnNarration(t *testing.T) {
	pbMatchFold(t, pbPrompt(t, "conflict-resolve-prompt.md"), `between tool calls`)
}

// A conflicted file that declares itself generated must be resolved in its
// source of truth and regenerated, never hand-merged. The wording stays
// repo-agnostic (issue #403).
func TestPromptBytes_ConflictResolveRequiresSourceOfTruthForGeneratedFiles(t *testing.T) {
	pbMatchFold(t, pbPrompt(t, "conflict-resolve-prompt.md"),
		`do not edit`, `source of truth`, `regenerat`, `never hand-merge`)
}

// issue #1653: the launcher already gates on CI green (gateToGreen) before
// flipping the PR ready and merging (issue #1651), so the Driver polling for
// CI registration too is a redundant LLM step.
func TestPromptBytes_IssueDropsDriverWatchCIPolling(t *testing.T) {
	prompt := pbPrompt(t, "issue-prompt.md")
	pbMatch(t, prompt, `OPEN A PULL REQUEST`)
	pbNoMatch(t, prompt, `^# WATCH CI$`)
}

// Each pin below holds one obligation from the pre-tightening prose of issue
// #3224's pass, so the section can shrink while every rule it enforces
// survives.
func TestPromptBytes_IssueCoherenceGateKeepsEveryObligation(t *testing.T) {
	section := pbSection(t, pbPrompt(t, "issue-prompt.md"), `^# ISSUE COHERENCE GATE$`, `^# COMMS$`)
	pbMatchFold(t, section,
		`compare`,
		`title`,
		// A body that only elaborates on the title's own topic is exempt and
		// must pass through.
		`elaborat`,
		`pass through`,
		`genuine`,
		`contradictory`,
		`halt immediately`,
		`do not scout`,
		`do not write any diff`,
		`both interpretations`,
		`title implies`,
		`body implies`,
		`which one governs`,
		// The escalation lives in the note= field; the launcher posts it
		// host-side, never the agent itself.
		`note=`,
		`launcher posts`,
		`host-side`)
	pbContains(t, section,
		"SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=ambiguous note=<escalation naming both interpretations>")
	pbMatchFold(t, section,
		`raw plain text`,
		`backticks`,
		`nothing after it`,
		// status=ambiguous is a successful stop, never status=blocked.
		`non-crash stop`)
	pbContains(t, section, "status=blocked")
	pbMatchFold(t, section, `proceed straight`)
	pbContains(t, section, "# SCOUT")
}

// issue #4015: a change already present on the fetched default branch is a
// distinct, successful, non-crash stop, mirroring the coherence gate above.
func TestPromptBytes_AlreadyResolvedGateSitsAfterScoutBeforeImplement(t *testing.T) {
	section := pbSection(t, pbPrompt(t, "issue-prompt.md"), `# ALREADY RESOLVED$`, `^# IMPLEMENT$`)
	// Verified against the fetched default branch, not the worker's own seed.
	pbMatchFold(t, section, `git fetch origin`)
	pbContains(t, section, "origin/${BASE_BRANCH}")
	pbMatchFold(t, section,
		// A partial or speculative match does not qualify.
		`partial`, `speculative`, `does not qualify`,
		// Name the resolving commit or PR, ideally with the proving check/test.
		`commit`, `PR`, `check or test`,
		// No commits, push, PR, in-box comment or close: the launcher does the
		// rest host-side.
		`no commits`, `no push`, `no PR`, `no.*comment`, `no.*close`, `host-side`)
	pbContains(t, section,
		"SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=already-resolved note=")
	pbMatchFold(t, section,
		`raw plain text`, `backticks`, `nothing after it`,
		// A distinct successful stop, never status=blocked.
		`non-crash stop`)
	pbContains(t, section, "status=blocked", "# IMPLEMENT")
}

// Output is a machine-parsed log except on the parts that stay human prose
// (commits, PR body, IF BLOCKED comment, outcome note=). Scoped to the COMMS
// section so dropping the carve-out sentence still fails even though those
// parts are named elsewhere in the prompt.
func TestPromptBytes_CommsSectionMachineLogVoiceWithHumanProseCarveOuts(t *testing.T) {
	comms := pbSection(t, pbPrompt(t, "issue-prompt.md"), `^# COMMS$`, `^# [A-Z]`)
	pbMatchFold(t, comms,
		`machine-parsed log`, `not a conversation`, `no pleasantries`,
		`never restate`, `one terse`, `no narrative framing`,
		`reserved exclusively`, `Conventional Commits`, `PR title and body`,
		`IF BLOCKED`, `note=`)
}

// The WATCH CI never-background rule (issue #571) must generalize to CHECK's
// long build/test gates: an agent that backgrounds `nix build .#checks-inbox`
// and ends its turn never emits SPINDRIFT_OUTCOME. Every CHECK slice anchors
// on the end of "# CHECK" because it trails ${PRINCIPLE_LAZINESS_PROTOCOL_STEP}
// in the raw template (issues #3221, #3505).
func TestPromptBytes_CheckSectionForbidsBackgroundingGateAndRequiresTerminalOutcome(t *testing.T) {
	check := pbSection(t, pbPrompt(t, "issue-prompt.md"), `# CHECK$`, `^# REVIEW$`)
	pbMatchFold(t, check, `never background`, `foreground`)
	pbMatch(t, check, `SPINDRIFT_OUTCOME`)
}

// issue #3220: CHECK keeps the obligation and the foreground-gate rule; the
// elaborated guidance lives in the skill (#3223 moved the Nix lore to
// /nix-checks). A restated copy here is the duplication the move removed, so
// pin its absence, not just the anchor's presence.
func TestPromptBytes_CheckSectionAnchorsCheckHygieneWithoutRestatingSkill(t *testing.T) {
	check := pbSection(t, pbPrompt(t, "issue-prompt.md"), `# CHECK$`, `^# REVIEW$`)
	// The anchor renders from a bakedness-gated fragment, so the section
	// carries the variable and the fragment carries the prose.
	pbContains(t, check, "CHECK_HYGIENE_STEP")
	fragment := filepath.Join("fragments", "check-hygiene-default.md")
	pbContains(t, pbPrompt(t, fragment), "/check-hygiene")
	// The dispatcher parses SPINDRIFT_OUTCOME, so the terminal-outcome mandate
	// must not depend on the agent having read an on-demand skill body.
	pbMatchFold(t, check, `do not stop this run`)
	pbContains(t, check, "status=blocked")
	pbNoMatchFold(t, check, `vanished`, "never `cat`")
}

// issue #726. The raw template has no CHECK section; it is injected at build
// time, so this reads the rendered fix prompt.
func TestPromptBytes_FixCheckSectionAnchorsCheckHygiene(t *testing.T) {
	check := pbSection(t, pbPrompt(t, "fix-prompt.md"), `^# CHECK$`, `^# LAND THE CHANGE$`)
	pbContains(t, check, "CHECK_HYGIENE_STEP")
}

// issue #3505: the ${CODE_COMMENTS_STEP} anchor and its bakedness-gated
// fragment are gone. This pins placement (the policy renders inside
// IMPLEMENT); nix/checks/prompts.nix's prompt-code-comments-inlined owns
// verbatim equality against SKILL.md, so one phrase here is enough.
func TestPromptBytes_ImplementSectionInlinesCodeCommentsPolicy(t *testing.T) {
	prompt := pbPrompt(t, "issue-prompt.md")
	implement := pbSection(t, prompt, `# IMPLEMENT$`, `# CHECK$`)
	pbMatch(t, implement, `# CHECK$`)
	pbMatchFold(t, implement, `the non-obvious why, a constraint, or a gotcha`)
	pbNoContains(t, prompt, "CODE_COMMENTS_STEP", "/code-comments", "# CODE COMMENTS")
}

// issue #3505. "# FIX" trails ${FIX_CI_READ_GITHUB_STEP}${FIX_CI_READ_FORGEJO_STEP}
// in the rendered file, so the slice anchors on the end of the line, not the
// start.
func TestPromptBytes_FixSectionInlinesCodeCommentsPolicy(t *testing.T) {
	prompt := pbPrompt(t, "fix-prompt.md")
	fix := pbSection(t, prompt, `# FIX$`, `^# COMMS$`)
	pbMatch(t, fix, `^# COMMS$`)
	pbMatchFold(t, fix, `the non-obvious why, a constraint, or a gotcha`)
	pbNoContains(t, prompt, "CODE_COMMENTS_STEP", "/code-comments")
}

// issue #714: nix flakes only evaluate git-tracked files, so an agent that
// runs `nix build` before staging a new file burns a checks cycle. The
// guidance must land before the flake.nix paragraph so it primes every nix
// invocation. issue #3223 moved it into the nix-checks skill.
func TestPromptBytes_NixChecksSkillGitAddBeforeNixBuild(t *testing.T) {
	body := pbFile(t, "nix-checks-skill.md")
	if strings.TrimSpace(body) == "" {
		t.Fatal("nix-checks skill is empty")
	}
	pbMatchFold(t, body, `git add`, `tracked by`)
	firstLine := func(re string) int {
		for i, line := range strings.Split(body, "\n") {
			if regexp.MustCompile(re).MatchString(line) {
				return i + 1
			}
		}
		return 0
	}
	addLine, flakeLine := firstLine(`(?i)git add`), firstLine(`flake.nix`)
	if addLine == 0 || flakeLine == 0 {
		t.Fatalf("anchor lines missing: git add=%d flake.nix=%d", addLine, flakeLine)
	}
	if addLine >= flakeLine {
		t.Errorf("git add (line %d) must precede flake.nix (line %d)", addLine, flakeLine)
	}
}

// issue #782: a bare 'tracked' pin false-passes if the "is not tracked by Git"
// sentence is rewritten away, because "git-tracked files" earlier in the same
// body still contains the word "tracked". issue #3223: the pin follows its
// subject to the nix-checks skill body; on the now Nix-free CHECK section it
// would pass vacuously.
func TestPromptBytes_NixChecksTrackedByPinRejectsDecoy(t *testing.T) {
	body := pbFile(t, "nix-checks-skill.md")
	if strings.TrimSpace(body) == "" {
		t.Fatal("nix-checks skill is empty")
	}
	decoy := strings.ReplaceAll(body, "is not tracked by Git", "failed for an unrelated reason")
	pbNoMatchFold(t, decoy, `tracked by`)
}

// issue #3223 moved the tracked-by sentence into the nix-checks skill. What
// the fix pass still needs from this section is the route to the relocated
// lore, so pin the anchor.
func TestPromptBytes_FixCheckSectionAnchorsNixChecks(t *testing.T) {
	check := pbSection(t, pbPrompt(t, "fix-prompt.md"), `^# CHECK$`, `^# LAND THE CHANGE$`)
	pbContains(t, check, "NIX_CHECKS_STEP")
}

// CODE_FORGE=git (#330) must skip PR creation and CI-watch entirely and emit a
// branch ref where a PR URL would go. issue #2526: the stop step lives in a
// read-write/read-only fragment pair, so the section itself carries only the
// LAND_GIT_STOP_*_STEP references.
func TestPromptBytes_CodeForgeGitBranchesToPushOnlyOutcome(t *testing.T) {
	prompt := pbPrompt(t, "issue-prompt.md")
	pbMatch(t, prompt,
		`CODE_FORGE=git`,
		`skip OPEN A PULL REQUEST below entirely`,
		`LAND_GIT_STOP_READ_WRITE_STEP`,
		`LAND_GIT_STOP_READ_ONLY_STEP`)
	for _, f := range []string{"land-git-stop-read-write.md", "land-git-stop-read-only.md"} {
		frag := pbPrompt(t, filepath.Join("fragments", f))
		pbContains(t, frag, "landing=${BRANCH} status=ready")
		pbMatch(t, frag, `Do not open a pull request and do not attempt to merge`)
	}
}

// issue #1614: the draft bit is the readiness signal the entrypoint backstop
// and launcher trust, so the PR must never open non-draft. issue #1919: the
// `gh pr create` invocation lives in open-pr-create-git.md, so the section
// itself carries only the OPEN_PR_CREATE_READ_WRITE_STEP reference.
func TestPromptBytes_OpenPullRequestOpensAsDraft(t *testing.T) {
	section := pbSection(t, pbPrompt(t, "issue-prompt.md"), `^# OPEN A PULL REQUEST$`, `^# OUTCOME$`)
	pbMatch(t, section, `OPEN_PR_CREATE_READ_WRITE_STEP`)
	pbMatch(t, pbPrompt(t, filepath.Join("fragments", "open-pr-create-git.md")), `gh pr create --draft`)
}

// issue #1653: the launcher, not the Driver, owns the draft to ready flip
// (issue #1651), so the Driver's OUTCOME step must print status=ready without
// ever calling `gh pr ready`. issue #3224 reshaped the invalid-examples block
// to bare fragments, so the real "print status=ready" instruction lives in the
// OUTCOME_LANDING_READ_*_STEP fragments checked here.
func TestPromptBytes_OutcomeLeavesPRDraftAndNeverFlipsReady(t *testing.T) {
	section := pbOutcomeSection(t)
	pbMatch(t, section, `OUTCOME_LANDING_READ_WRITE_STEP`, `OUTCOME_LANDING_READ_ONLY_STEP`)
	pbContains(t, pbPrompt(t, filepath.Join("fragments", "outcome-landing-git.md")), "status=ready")
	pbContains(t, pbPrompt(t, filepath.Join("fragments", "outcome-landing-outbox.md")), "status=ready")
	// Anchored to a bare invocation line, not the prose forbidding it below.
	pbNoMatch(t, section, `^gh pr ready`)
}

// issue #1901: an agent following the prompt must be able to see the accepted
// grammar and status enum, not infer it from one worked example. issue #1919
// generalized the placeholder from <pr-url> to <landing-ref>, since landing=
// carries a branch name, not a PR URL, under read-only.
func TestPromptBytes_OutcomeStatesExactGrammarAndStatusValues(t *testing.T) {
	section := pbOutcomeSection(t)
	pbContains(t, section,
		"SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=<landing-ref> status=<status> note=<text>",
		"valid `status` values here are `ready` and `blocked`")
	pbMatchFold(t, section, `already-resolved`, `ALREADY RESOLVED gate`)
}

// issue #1901: SPINDRIFT_OUTCOME: (colon, not space) fails the parser's
// literal prefix match, so the prompt must warn against it explicitly. issue
// #3224 reshaped it to a bare token+colon fragment, not a whole copyable line,
// to cut the parrot risk.
func TestPromptBytes_OutcomeMarksTrailingColonFragmentInvalid(t *testing.T) {
	section := pbOutcomeSection(t)
	pbContains(t, section, "SPINDRIFT_OUTCOME:")
	pbMatchFold(t, section, `trailing colon`)
}

// issue #1901: LastInLog only recognizes a line that *starts* with the prefix,
// so text before the token hides the whole line the same way. issue #3224
// reshaped the example to a truncated prefix fragment, so this pins the rule's
// own wording plus the column-one consequence.
func TestPromptBytes_OutcomeMarksTokenPrecededByTextInvalid(t *testing.T) {
	section := pbOutcomeSection(t)
	pbMatchFold(t, section, `prefix before the token`, `starting at column one`)
}

// issue #1901: Parse() does not enforce a status enum, so ready/blocked is a
// prompt-level contract and the prompt must call out that a value like SUCCESS
// is wrong. Unlike the colon/prefix variants this one does parse, so the prompt
// must not claim it is lost; issue #3224 renamed the wording but not the rule.
func TestPromptBytes_OutcomeMarksOutOfEnumStatusInvalidNotLost(t *testing.T) {
	section := pbOutcomeSection(t)
	pbContains(t, section, "status=SUCCESS")
	pbMatchFold(t, section, `out-of-enum status`, `still parses`)
}

// issue #3224, the grammar-edit incident class: an agent editing this prose
// parrots a fully-formed counter-example back out as its own final message.
// Only the canonical Grammar line may carry the full token+status=+note=
// shape; any other line in this section is copyable as a fake outcome.
func TestPromptBytes_OutcomeInvalidExamplesNeverFormCopyableLine(t *testing.T) {
	for _, line := range strings.Split(pbOutcomeSection(t), "\n") {
		if strings.Contains(line, "Grammar:") {
			continue
		}
		if strings.Contains(line, "SPINDRIFT_OUTCOME") &&
			strings.Contains(line, "status=") && strings.Contains(line, "note=") {
			t.Errorf("copyable outcome line outside Grammar: %q", line)
		}
	}
}

// issue #1901 AC3: the agent must be told, in the OUTCOME section itself, that
// nothing may follow the line.
func TestPromptBytes_OutcomeRequiresLiteralFinalMessage(t *testing.T) {
	pbMatchFold(t, pbOutcomeSection(t), `literal final message`, `nothing after it`)
}

// issue #1653: the Driver never flips a PR to ready (only the launcher does,
// at green, issue #1651), so a blocked run has nothing to revert. issue #1933:
// the closing SPINDRIFT_OUTCOME line renders from a read-write/read-only
// fragment pair, so both fragments are folded in.
func TestPromptBytes_IfBlockedNeverRevertsPRToDraft(t *testing.T) {
	section := pbSection(t, pbPrompt(t, "issue-prompt.md"), `^# IF BLOCKED$`, "")
	section += "\n" + pbPrompt(t, filepath.Join("fragments", "if-blocked-outcome-landing-git.md")) +
		"\n" + pbPrompt(t, filepath.Join("fragments", "if-blocked-outcome-landing-outbox.md"))
	pbMatch(t, section, `SPINDRIFT_OUTCOME.*status=blocked`)
	pbNoContains(t, section, "--undo")
}

// issue #1653: the Driver never flips a PR to ready, so fix-prompt.md's
// override bullet must no longer tell a blocked fix pass to convert the PR
// back to draft. It never left draft.
func TestPromptBytes_FixDropsDraftRevertOverride(t *testing.T) {
	prompt := pbPrompt(t, "fix-prompt.md")
	pbNoMatchFold(t, prompt, `leave the existing PR as-is`, `draft-revert`)
	pbMatch(t, prompt, "do not run `gh pr create`")
}

// Each harness-owned contract block must reach the rendered prompt that
// injects it: a prompt missing its block ships an agent that never emits the
// outcome line (issue #419). Compared on trimmed content, since injection
// manages the blank-line separators around the block.
func TestPromptBytes_ContractBlocksPresent(t *testing.T) {
	for _, c := range []struct{ contract, prompt string }{
		{"outcome-contract.md", "issue-prompt.md"},
		{"outcome-contract.md", "fix-prompt.md"},
		{"comms-contract.md", "fix-prompt.md"},
		{"check-contract.md", "fix-prompt.md"},
		{"research-outcome-contract.md", "research-prompt.md"},
	} {
		t.Run(c.contract+" in "+c.prompt, func(t *testing.T) {
			block := strings.TrimSpace(pbFile(t, c.contract))
			if block == "" {
				t.Fatalf("contract fixture %s is empty", c.contract)
			}
			if !strings.Contains(pbPrompt(t, c.prompt), block) {
				t.Errorf("%s does not contain the bytes of %s", c.prompt, c.contract)
			}
		})
	}
}
