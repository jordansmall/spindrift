# The Conditional fragment registry (issue #622): one row per opt-in prompt
# step. agent/entrypoint.sh's fragment loop and its `_subst` substitution
# allowlist are both rendered from these rows by lib/mkHarness.nix's
# fragmentRegistryPreamble, so a fragment can never reference a variable the
# substitution step does not know about.

# A row's `gate` is a bash variable the loop tests for non-emptiness, not a
# Nix boolean, and `fragment` is a basename under prompts/fragments/, from
# templates/default or a SPINDRIFT_PROMPT_DIR override.

# `extraSubstVars` (default []) lists allowlist entries a fragment's own body
# interpolates; every other fragment is static prose once its gate is on.
# `inverseOf` (issue #3219) declares this row's gate the boolean inverse of
# the named on-gate; fragment-pairs.nix's `validate` asserts those pairs at
# the end of this file, before an image can bake from a malformed one.
let
  rows = [
    {
      gate = "SKILLS_FOUND";
      fragment = "skill-preamble.md";
      var = "SKILL_PREAMBLE";
      extraSubstVars = [ "SKILLS_FOUND" ];
    }
    {
      gate = "CAVEMAN_BAKED";
      fragment = "caveman-default.md";
      var = "CAVEMAN_STEP";
    }
    # The worker-role variant (issue #2706): worker-prompt.md is quarantined
    # from the marker grammar (lib/prompt-contract.nix's
    # workerForbiddenMarkers), so caveman-default.md's exemption prose would
    # leak literal forbidden markers into a worker prompt. No commit-message
    # half: the coordinator owns COMMIT (issue #3419).
    {
      gate = "CAVEMAN_BAKED";
      fragment = "caveman-default-worker.md";
      var = "CAVEMAN_STEP_WORKER";
    }
    # The review-role variant (issue #2707): a review pass emits a VERDICT
    # line, so it keeps the marker-grammar exemption the worker variant drops,
    # and adds the `## Blocking`/`## Non-blocking` findings, which a Filer
    # turns into an issue body a human reads cold. Drift in the prose these
    # rows duplicate is caught by caveman_default_fragment_parity_test.go.
    {
      gate = "CAVEMAN_BAKED";
      fragment = "caveman-default-review.md";
      var = "CAVEMAN_STEP_REVIEW";
    }
    # The research-role variant (issue #2708): a research pass produces
    # human-facing output, since a human reads the posted verdict comment to
    # decide whether to promote the issue. So this fragment exempts that whole
    # comment (verdict, rationale, context-for-a-worker, open questions, and
    # the `<!-- spindrift-research -->` marker), not just the marker lines.
    {
      gate = "CAVEMAN_BAKED";
      fragment = "caveman-default-research.md";
      var = "CAVEMAN_STEP_RESEARCH";
    }
    # The IMPLEMENT test-first step (issue #3219), an exactly-one-on pair.
    # Baking the tdd skill subtracts prose rather than adding a deferral: the
    # on arm is a bare anchor pointing at `/tdd`, and the red/green/refactor
    # fallback moves out of issue-prompt.md into the off arm.
    {
      gate = "TDD_BAKED";
      fragment = "tdd-baked.md";
      var = "TDD_BAKED_STEP";
    }
    {
      gate = "TDD_UNBAKED";
      fragment = "tdd-unbaked.md";
      var = "TDD_UNBAKED_STEP";
      inverseOf = "TDD_BAKED";
    }
    # The COMMIT pair (issue #3222): baking the commit skill subtracts the
    # inline Conventional Commits rules. The granularity preference ("several
    # small focused commits") stays unconditional in issue-prompt.md, outside
    # both arms, because commit-rework-orchestrator.md back-references it by
    # name on both of its own branches.
    {
      gate = "COMMIT_BAKED";
      fragment = "commit-baked.md";
      var = "COMMIT_BAKED_STEP";
    }
    {
      gate = "COMMIT_UNBAKED";
      fragment = "commit-unbaked.md";
      var = "COMMIT_UNBAKED_STEP";
      inverseOf = "COMMIT_BAKED";
    }
    # The code-review execution-mode pair (issues #3222, #3226). The four hunt
    # dimensions, the Blocking/Non-blocking reconcile obligation and the
    # `VERDICT:` grammar (ADR 0035 scanPassLog) stay unconditional in
    # review-prompt.md: the baked arm defers to a pinned upstream skill, so a
    # depth obligation living only here would vanish on the unbaked runs.
    {
      gate = "CODE_REVIEW_BAKED";
      fragment = "code-review-baked.md";
      var = "CODE_REVIEW_BAKED_STEP";
      # Issue #3447: the fan-out's agent type is resolved in-box from the
      # agents this run provisions, so a roster without the review-axis entry
      # orders the skill's own default rather than an agent type the driver
      # session never defines.
      extraSubstVars = [ "REVIEW_FANOUT_AGENT" ];
    }
    {
      gate = "CODE_REVIEW_UNBAKED";
      fragment = "code-review-unbaked.md";
      var = "CODE_REVIEW_UNBAKED_STEP";
      inverseOf = "CODE_REVIEW_BAKED";
    }
    # The CHECK anchor for the harness-owned /check-hygiene skill
    # (issue #3220). Gated on bakedness because a prompt naming a skill the
    # box does not carry is dead text (issue #120). No inverseOf partner: the
    # relocated guidance is not restated inline, so there is no off arm.
    {
      gate = "CHECK_HYGIENE_BAKED";
      fragment = "check-hygiene-default.md";
      var = "CHECK_HYGIENE_STEP";
    }
    # The CHECK anchor for the dogfood-only /nix-checks skill (issue #3223),
    # same bakedness gate and no off arm. Unlike the row above this gate is
    # off in a stock Consumer image: nix-checks bakes only into spindrift's
    # own dogfood image, not lib/image.nix's harnessSkills.
    {
      gate = "NIX_CHECKS_BAKED";
      fragment = "nix-checks-default.md";
      var = "NIX_CHECKS_STEP";
    }
    # The REVIEW section (issue #2037, ADR 0035): off, the implementor spawns
    # a fresh `reviewer` subagent inline and loops until no blocking findings
    # remain. On, the orchestrator drives that review as its own pass, so this
    # pass's prompt stops after COMMIT unless a prior review pass's APPROVE is
    # already visible in the seeded run-state handoff.
    {
      gate = "REVIEW_LOOP_INLINE";
      fragment = "review-loop-inline.md";
      var = "REVIEW_LOOP_INLINE_STEP";
    }
    {
      gate = "REVIEW_LOOP_ORCHESTRATOR";
      fragment = "review-loop-orchestrator.md";
      var = "REVIEW_LOOP_ORCHESTRATOR_STEP";
    }
    # The land pass's rebase-then-fix-then-gate work order (issue #3214):
    # reuses the REVIEW_LOOP_ORCHESTRATOR gate, since one prompt serves every
    # pass and a gate cannot key on "is this the land pass?". The fragment
    # scopes itself in its own prose, keyed on the seeded handoff's
    # `Last reviewer verdict: APPROVE`.
    {
      gate = "REVIEW_LOOP_ORCHESTRATOR";
      fragment = "land-pass-order-orchestrator.md";
      var = "LAND_PASS_ORDER_ORCHESTRATOR_STEP";
    }
    # The COMMIT-section fold-fix instruction (issue #2698): reuses the
    # REVIEW_LOOP_ORCHESTRATOR gate but must render earlier, in COMMIT, ahead
    # of where the pass commits. The review-loop fragment itself never acts on
    # findings in the same turn, so the instruction cannot live there.
    {
      gate = "REVIEW_LOOP_ORCHESTRATOR";
      fragment = "commit-rework-orchestrator.md";
      var = "COMMIT_REWORK_ORCHESTRATOR_STEP";
    }
    # The SCOUT section body (issue #3157), an exactly-one-on pair. The
    # `# SCOUT` heading in issue-prompt.md must stay a literal, unconditional
    # line: lib/prompt-contract.nix's "comms" injectBlocks row ends right at
    # it.
    {
      gate = "SCOUT_PROVISIONED";
      fragment = "scout-delegate.md";
      var = "SCOUT_DELEGATE_STEP";
    }
    {
      gate = "SCOUT_ABSENT";
      fragment = "scout-absent.md";
      var = "SCOUT_ABSENT_STEP";
      inverseOf = "SCOUT_PROVISIONED";
    }
    # The IMPLEMENT coordinator step (issue #2056): when a `worker` subagent
    # is provisioned (WORKER_MODEL set, issue #2054), the main session
    # delegates each slice to it. A single on/off gate, so with no worker the
    # step renders empty and the section is byte-identical to the
    # single-implementor prompt. Orthogonal to the orchestrator flag.
    {
      gate = "WORKER_PROVISIONED";
      fragment = "coordinator.md";
      var = "COORDINATOR_STEP";
    }
    # The coordinator's scout-brief guidance (issue #3157). Gated on the
    # conjunction COORDINATOR_SCOUT_BRIEF (WorkerProvisioned &&
    # ScoutProvisioned, computed in gates.go) because a row carries one gate.
    {
      gate = "COORDINATOR_SCOUT_BRIEF";
      fragment = "coordinator-scout-brief.md";
      var = "COORDINATOR_SCOUT_BRIEF_STEP";
    }
    # The worker's own brief-read directive (issue #3157).
    {
      gate = "WORKER_SCOUT_BRIEF";
      fragment = "worker-scout-brief.md";
      var = "WORKER_SCOUT_BRIEF_STEP";
    }
    # The anchor for the harness-owned /code-comments skill (issue #3221,
    # same shape as CHECK_HYGIENE_BAKED above): gated on bakedness rather than
    # the old always-true CODE_COMMENTS_MANDATORY gate (issue #2880), which
    # existed only to route mandatory prose through this registry. No
    # inverseOf partner: the policy prose is not restated inline on an off arm.
    {
      gate = "CODE_COMMENTS_BAKED";
      fragment = "code-comments-default.md";
      var = "CODE_COMMENTS_STEP";
    }
    # The write-mechanism split (issue #2019): a filer under read-only holds
    # no write token, so its label and file steps emit the host-mediated
    # SPINDRIFT_ISSUE_INTENT relay instead of gh label create/gh issue create.
    # Only the write mechanism is gated; the filer's authoring judgment stays
    # in filer-prompt.md's unconditional dedup/title/body steps.

    # The direct case forks again on ISSUE_TRACKER (issue #1963): fj has no
    # label verb and `fj issue create` has no --label flag, so forgejo renders
    # the *-forgejo fragments (curl against the REST API for the label).
    # file-issues-direct.md names no gh/fj command, so it alone stays on the
    # combined FILER_FILE_DIRECT_ANY gate.
    {
      gate = "FILER_FILE_DIRECT_ANY";
      fragment = "file-issues-direct.md";
      var = "FILE_ISSUES_DIRECT_STEP";
    }
    {
      gate = "FILER_FILE_RELAY";
      fragment = "file-issues-relay.md";
      var = "FILE_ISSUES_RELAY_STEP";
    }
    {
      gate = "FILER_FILE_DIRECT_GH";
      fragment = "filer-label-direct.md";
      var = "FILER_LABEL_DIRECT_STEP";
    }
    {
      gate = "FILER_FILE_DIRECT_FORGEJO";
      fragment = "filer-label-direct-forgejo.md";
      var = "FILER_LABEL_DIRECT_FORGEJO_STEP";
    }
    # filer-label-relay.md splits by dispatch kind (issue #2593): the relay
    # mechanism is identical for work and research, but the label the launcher
    # applies host-side differs (agent-review-finding for work, settle/gate.go;
    # agent-research-finding for research, settle/research.go) and this
    # fragment's prose names it. The two gates are never both true.
    {
      gate = "FILER_FILE_RELAY_WORK";
      fragment = "filer-label-relay.md";
      var = "FILER_LABEL_RELAY_STEP";
    }
    {
      gate = "FILER_FILE_RELAY_RESEARCH";
      fragment = "filer-label-relay-research.md";
      var = "FILER_LABEL_RELAY_RESEARCH_STEP";
    }
    {
      gate = "FILER_FILE_DIRECT_GH";
      fragment = "filer-file-direct.md";
      var = "FILER_FILE_DIRECT_STEP";
    }
    {
      gate = "FILER_FILE_DIRECT_FORGEJO";
      fragment = "filer-file-direct-forgejo.md";
      var = "FILER_FILE_DIRECT_FORGEJO_STEP";
    }
    {
      gate = "FILER_FILE_RELAY";
      fragment = "filer-file-relay.md";
      var = "FILER_FILE_RELAY_STEP";
    }
    # The research-only filing step (issue #2593, ADR 0041): reuses the
    # FILER_FILE_RELAY gate, which gates_tracker.go makes unconditionally true
    # for a research dispatch with a Filer, so no separate gate is needed. A
    # research prompt must never render a direct-file fragment in any mode;
    # `researchDirectFileCheckOk` (issue #2595) backstops that at build time.
    {
      gate = "FILER_FILE_RELAY";
      fragment = "research-file-issues-relay.md";
      var = "RESEARCH_FILE_ISSUES_RELAY_STEP";
    }
    {
      gate = "AUTO_FORMAT";
      fragment = "auto-format.md";
      var = "AUTO_FORMAT_STEP";
    }
    {
      gate = "AUTO_LINT";
      fragment = "auto-lint.md";
      var = "AUTO_LINT_STEP";
    }
    {
      gate = "CI_FAILURE_SUMMARY";
      fragment = "ci-failure.md";
      var = "CI_FAILURE_STEP";
      extraSubstVars = [ "CI_FAILURE_SUMMARY" ];
    }
    # The fix-pass CI-read step forks on CODE_FORGE (issue #1963): the
    # `gh run list` family does not exist against a Forgejo remote, so a
    # forgejo fix pass reads CI via `fj pr status`. Exactly one gate is on, so
    # fix-prompt.md concatenates both vars and only the active one renders.
    {
      gate = "FIX_CI_READ_GH";
      fragment = "fix-ci-read-github.md";
      var = "FIX_CI_READ_GITHUB_STEP";
    }
    {
      gate = "FIX_CI_READ_FORGEJO";
      fragment = "fix-ci-read-forgejo.md";
      var = "FIX_CI_READ_FORGEJO_STEP";
    }
    # The PR-body ticket-reference step (issue #1429, ADR 0029): exactly one
    # of the three gates is on, so issue-prompt.md concatenates all three
    # vars. github and jira share `Closes #${ISSUE_NUMBER}` (a jira key is not
    # a bare number, so GitHub auto-close cannot match it); local's opt-in
    # emits a `Local-issue: <slug>` breadcrumb, never a `Closes`/`Fixes` word.
    {
      gate = "PR_BODY_CLOSES";
      fragment = "pr-body-closes.md";
      var = "PR_BODY_CLOSES_STEP";
    }
    {
      gate = "PR_BODY_LOCAL_REF";
      fragment = "pr-body-local-ref.md";
      var = "PR_BODY_LOCAL_REF_STEP";
    }
    {
      gate = "PR_BODY_LOCAL_NOREF";
      fragment = "pr-body-local-noref.md";
      var = "PR_BODY_LOCAL_NOREF_STEP";
    }
    # The issue-read step (issues #1691, #1963, ADR 0032): the subject issue's
    # body and comments are injected host-side (issues #3445, #3469,
    # promptassembly.issueTextSection), so these fragments cover only a
    # *linked* issue. Each folds the trailing `git log` bullet in: the loop
    # adds a blank line per fragment, so a mid-list `${VAR}` would split it.
    {
      gate = "ISSUE_TRACKER_GITHUB";
      fragment = "issue-read-github.md";
      var = "ISSUE_READ_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL";
      fragment = "issue-read-local.md";
      var = "ISSUE_READ_LOCAL_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO";
      fragment = "issue-read-forgejo.md";
      var = "ISSUE_READ_FORGEJO_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB";
      fragment = "research-issue-read-github.md";
      var = "RESEARCH_ISSUE_READ_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL";
      fragment = "research-issue-read-local.md";
      var = "RESEARCH_ISSUE_READ_LOCAL_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO";
      fragment = "research-issue-read-forgejo.md";
      var = "RESEARCH_ISSUE_READ_FORGEJO_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB";
      fragment = "scout-issue-read-github.md";
      var = "SCOUT_ISSUE_READ_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO";
      fragment = "scout-issue-read-forgejo.md";
      var = "SCOUT_ISSUE_READ_FORGEJO_STEP";
    }
    # The local content-plane write step (issue #1692, ADR 0032): a local Box
    # has no in-box tracker client, so the research verdict travels as a
    # SPINDRIFT_COMMENT block on stdout and the work blocked-note rides the
    # outcome line's note= field; settle posts both host-side.

    # github and forgejo split further on BOX_FORGE_AND_ISSUE_ACCESS
    # (issues #1917, #1963): a read-only Box holds no write token, so it gets
    # the same host-mediated relay local always has. The read steps above keep
    # the undifferentiated ISSUE_TRACKER_* gate on purpose, since a read-only
    # token still permits `gh issue view` / `fj issue view`.
    {
      gate = "ISSUE_TRACKER_GITHUB_READWRITE";
      fragment = "research-verdict-github.md";
      var = "RESEARCH_VERDICT_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB_READONLY";
      fragment = "research-verdict-github-readonly.md";
      var = "RESEARCH_VERDICT_GITHUB_READONLY_STEP";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL";
      fragment = "research-verdict-local.md";
      var = "RESEARCH_VERDICT_LOCAL_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READWRITE";
      fragment = "research-verdict-forgejo.md";
      var = "RESEARCH_VERDICT_FORGEJO_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READONLY";
      fragment = "research-verdict-forgejo-readonly.md";
      var = "RESEARCH_VERDICT_FORGEJO_READONLY_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB_READWRITE";
      fragment = "issue-blocked-comment-github.md";
      var = "ISSUE_BLOCKED_COMMENT_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB_READONLY";
      fragment = "issue-blocked-comment-github-readonly.md";
      var = "ISSUE_BLOCKED_COMMENT_GITHUB_READONLY_STEP";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL";
      fragment = "issue-blocked-comment-local.md";
      var = "ISSUE_BLOCKED_COMMENT_LOCAL_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READWRITE";
      fragment = "issue-blocked-comment-forgejo.md";
      var = "ISSUE_BLOCKED_COMMENT_FORGEJO_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READONLY";
      fragment = "issue-blocked-comment-forgejo-readonly.md";
      var = "ISSUE_BLOCKED_COMMENT_FORGEJO_READONLY_STEP";
    }
    # The LAND THE CHANGE CODE_FORGE=git push step (issue #2510): the
    # forbidden-marker check (lib/prompt-contract.nix) flags any raw, un-gated
    # "git push" imperative in the shared templates, so this one moved into a
    # gated fragment. There is no read-only arm: issue #2526's eval-time
    # assert makes read-only with CODE_FORGE=git fail `nix build` outright.
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "land-git-push-git.md";
      var = "LAND_GIT_PUSH_READ_WRITE_STEP";
    }
    # The CODE_FORGE=git block's closing "print one line and stop" step:
    # identical body either way, but its list number depends on whether the
    # push step above precedes it. Read-write must read "2.", read-only "1.",
    # or read-only renders an orphaned "2." with no "1." before it.
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "land-git-stop-read-write.md";
      var = "LAND_GIT_STOP_READ_WRITE_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "land-git-stop-read-only.md";
      var = "LAND_GIT_STOP_READ_ONLY_STEP";
    }
    # The OPEN A PULL REQUEST push step (issue #1918): a read-only Box holds
    # no push-capable token, so it writes its finished branch as a seam bundle
    # to the outbox and the launcher's BundleRelay force-pushes it host-side.
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "open-pr-push-git.md";
      var = "OPEN_PR_PUSH_READ_WRITE_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "open-pr-push-outbox.md";
      var = "OPEN_PR_PUSH_READ_ONLY_STEP";
    }
    # The OPEN A PULL REQUEST create step (issue #1919): a read-only Box
    # cannot run `gh pr create`, so it emits title and body as one
    # nonce-guarded, base64-encoded SPINDRIFT_PR_INTENT stdout line, and the
    # launcher opens the draft PR. Issue #1938 moved that off a multi-line
    # block, which never survived Claude Code's stream-json transport.

    # The read-write case forks on CODE_FORGE (issue #1963): `gh pr create`
    # for github, `fj pr create` for forgejo. Read-only never forks, because
    # SPINDRIFT_PR_INTENT is opened host-side whichever forge backend answers.
    {
      gate = "OPEN_PR_CREATE_RW_GH";
      fragment = "open-pr-create-git.md";
      var = "OPEN_PR_CREATE_READ_WRITE_STEP";
    }
    {
      gate = "OPEN_PR_CREATE_RW_FORGEJO";
      fragment = "open-pr-create-forgejo.md";
      var = "OPEN_PR_CREATE_READ_WRITE_FORGEJO_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "open-pr-create-outbox.md";
      var = "OPEN_PR_CREATE_READ_ONLY_STEP";
    }
    # The OUTCOME section's landing= value and status=ready close
    # (issue #1919): a read-only Box never opens the PR, so it never learns a
    # URL. Its landing= carries the branch name, and "status=ready" means
    # "branch relayed, PR-intent ready to open" rather than "PR already open".
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "outcome-landing-git.md";
      var = "OUTCOME_LANDING_READ_WRITE_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "outcome-landing-outbox.md";
      var = "OUTCOME_LANDING_READ_ONLY_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "outcome-ready-means-git.md";
      var = "OUTCOME_READY_MEANS_READ_WRITE_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "outcome-ready-means-outbox.md";
      var = "OUTCOME_READY_MEANS_READ_ONLY_STEP";
    }
    # The COMMIT section's push step (issue #2462): a read-only Box has no
    # push-capable token, so the rebase+push+retry block must not render for
    # it. The pre-commit rebase-and-recheck step still renders either way: a
    # stale base is a stale base regardless of write access.
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "commit-push-git.md";
      var = "COMMIT_PUSH_READ_WRITE_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "commit-push-outbox.md";
      var = "COMMIT_PUSH_READ_ONLY_STEP";
    }
    # The IF BLOCKED push-failure triage block (issue #2462): it presupposes a
    # push was attempted and tells the agent to comment on the issue, neither
    # of which applies to a read-only Box. A denied push there is the expected
    # result of holding no write token, not evidence of a broken or
    # under-scoped one, so it must never be triaged or escalated as such.
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "if-blocked-triage-git.md";
      var = "IF_BLOCKED_TRIAGE_READ_WRITE_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "if-blocked-triage-outbox.md";
      var = "IF_BLOCKED_TRIAGE_READ_ONLY_STEP";
    }
    # The IF BLOCKED push step (issue #1933): a read-only Box holds no
    # push-capable token on the failure path either, so "Push what you have"
    # must not attempt a `git push` there.
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "if-blocked-push-git.md";
      var = "IF_BLOCKED_PUSH_READ_WRITE_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "if-blocked-push-outbox.md";
      var = "IF_BLOCKED_PUSH_READ_ONLY_STEP";
    }
    # The IF BLOCKED PR check/create step (issue #1933): a read-only Box holds
    # no PR-create-capable token on the failure path either, so it emits a
    # SPINDRIFT_PR_INTENT line instead of running `gh pr view`/`gh pr create`.
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "if-blocked-pr-git.md";
      var = "IF_BLOCKED_PR_READ_WRITE_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "if-blocked-pr-outbox.md";
      var = "IF_BLOCKED_PR_READ_ONLY_STEP";
    }
    # The IF BLOCKED section's closing SPINDRIFT_OUTCOME line (issue #1933): a
    # read-only Box never opens a PR on the blocked path either, so it never
    # learns a URL and must report the branch name instead.
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "if-blocked-outcome-landing-git.md";
      var = "IF_BLOCKED_OUTCOME_LANDING_READ_WRITE_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "if-blocked-outcome-landing-outbox.md";
      var = "IF_BLOCKED_OUTCOME_LANDING_READ_ONLY_STEP";
    }
  ];
in
assert (import ./fragment-pairs.nix).validate rows;
rows
