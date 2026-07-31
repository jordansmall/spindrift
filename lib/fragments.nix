# The Conditional fragment registry (issue #622, CONTEXT.md "Conditional
# fragment"): one row per opt-in prompt step. agent/entrypoint.sh's single
# fragment loop and its `_subst` substitution allowlist are both rendered
# from these rows (via lib/mkHarness.nix's fragmentRegistryPreamble), so a
# fragment can never reference a variable the substitution step doesn't know
# about, and a forgotten allowlist entry is impossible by construction.
#
# Each row:
#   gate     - bash variable name tested for non-emptiness to include the
#              step. The three knob-gated steps (auto-format, auto-lint,
#              CI-failure summary) name their launcher-delivered env var
#              directly; the computed-gate steps (skills discovery, the
#              per-skill caveman/tdd/commit deferrals, filer-enabled) name a
#              variable a short precompute line sets before the loop runs
#              (agent/entrypoint.sh, phase_prompt_assembly).
#   fragment - basename under prompts/fragments/ (templates/default or a
#              SPINDRIFT_PROMPT_DIR override) to render via `_subst` when the
#              gate is non-empty.
#   var      - bash variable the rendered (or, when the gate is off, empty)
#              fragment text is assigned to; substituted into the outer
#              prompt templates (issue-prompt.md / fix-prompt.md) via envsubst
#              and so always part of the substitution allowlist.
#   extraSubstVars - additional envsubst allowlist entries the fragment's own
#              body references (default []). Only skill-preamble.md and
#              ci-failure.md interpolate a variable inside their own text
#              (SKILLS_FOUND, CI_FAILURE_SUMMARY respectively); every other
#              fragment is static prose once its step is on.
[
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
  {
    gate = "TDD_BAKED";
    fragment = "tdd-default.md";
    var = "TDD_STEP";
  }
  {
    gate = "COMMIT_BAKED";
    fragment = "commit-default.md";
    var = "COMMIT_STEP";
  }
  {
    gate = "CODE_REVIEW_BAKED";
    fragment = "code-review-default.md";
    var = "CODE_REVIEW_STEP";
  }
  # The REVIEW section itself (issue #2037, ADR 0035): off, the implementor
  # still spawns a fresh `reviewer` subagent inline and loops until no
  # blocking findings remain, exactly as before. On, the orchestrator drives
  # that review as its own code-owned pass (agent/entrypoint.sh's
  # phase_prompt_assembly precompute block derives REVIEW_LOOP_INLINE /
  # REVIEW_LOOP_ORCHESTRATOR together from $ORCHESTRATOR, the same
  # exactly-one-on pairing style as ISSUE_TRACKER_GITHUB/LOCAL above), so this
  # pass's own prompt stops after COMMIT unless a prior review pass's
  # APPROVE is already visible in the seeded run-state handoff.
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
  # The IMPLEMENT coordinator step (issue #2056): when a `worker` subagent is
  # provisioned (WORKER_MODEL set, issue #2054, detected by
  # agent/entrypoint.sh's phase_prompt_assembly WORKER_PROVISIONED precompute
  # as a "worker" key in AGENTS_JSON_TEMPLATE), the main session runs IMPLEMENT
  # as a coordinator that delegates each slice to the worker instead of
  # implementing them itself. A single on/off gate, not a paired fork like the
  # review-loop rows above: with no worker the step renders empty (the
  # conditional-residue mechanism) so the section is byte-identical to today's
  # single-implementor prompt. Orthogonal to ORCHESTRATOR -- a worker can be
  # provisioned with the orchestrator on or off.
  {
    gate = "WORKER_PROVISIONED";
    fragment = "coordinator.md";
    var = "COORDINATOR_STEP";
  }
  # The write-mechanism split (issue #2019): a filer configured under
  # read-only + ORCHESTRATOR_ENABLED holds no write token, so its FILE
  # ISSUES step (this pair) and its own in-agent label/file steps (the two
  # pairs below) emit the host-mediated SPINDRIFT_ISSUE_INTENT relay instead
  # of gh label create/gh issue create. FILER_FILE_DIRECT/FILER_FILE_RELAY
  # (agent/entrypoint.sh's phase_prompt_assembly precompute block) are off
  # together whenever the filer isn't configured at all -- the same "no
  # trace when off" shape FILER_ENABLED alone gave this step before this
  # ticket -- and otherwise pick exactly one, mirroring the
  # BOX_ACCESS_READ_WRITE/BOX_ACCESS_READ_ONLY pairing style above. The
  # filer's own authoring judgment (dedup search, conventional-commit
  # titling, merge-vs-split) stays in the unconditional dedup/title/body
  # steps filer-prompt.md keeps outside this split -- only the write
  # mechanism itself is gated.
  {
    gate = "FILER_FILE_DIRECT";
    fragment = "file-issues-direct.md";
    var = "FILE_ISSUES_DIRECT_STEP";
  }
  {
    gate = "FILER_FILE_RELAY";
    fragment = "file-issues-relay.md";
    var = "FILE_ISSUES_RELAY_STEP";
  }
  {
    gate = "FILER_FILE_DIRECT";
    fragment = "filer-label-direct.md";
    var = "FILER_LABEL_DIRECT_STEP";
  }
  {
    gate = "FILER_FILE_RELAY";
    fragment = "filer-label-relay.md";
    var = "FILER_LABEL_RELAY_STEP";
  }
  {
    gate = "FILER_FILE_DIRECT";
    fragment = "filer-file-direct.md";
    var = "FILER_FILE_DIRECT_STEP";
  }
  {
    gate = "FILER_FILE_RELAY";
    fragment = "filer-file-relay.md";
    var = "FILER_FILE_RELAY_STEP";
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
  # The PR-body ticket-reference step (issue #1429, ADR 0029): exactly one of
  # these three gates is ever on (agent/entrypoint.sh's phase_prompt_assembly
  # precompute block derives them from ISSUE_TRACKER x LOCAL_ISSUE_REFERENCE),
  # so issue-prompt.md concatenates all three vars and only the active one
  # ever renders -- the same conditional-residue mechanism every other row
  # shares, just with three mutually exclusive gates instead of one on/off
  # knob. github (and jira, which shares the same branch -- its issue key
  # isn't a bare number GitHub's auto-close syntax would match, so it carries
  # no footgun) stays unconditional `Closes #${ISSUE_NUMBER}`; local defaults
  # to no reference at all; local's opt-in emits a non-auto-closing
  # `Local-issue: <slug>` breadcrumb, never a `Closes`/`Fixes` keyword.
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
  # The issue-read step (issue #1691, ADR 0032; forgejo third case added
  # #1963): local issues are read from the read-only /issues mount instead
  # of gh issue view, and forgejo issues are read via fj issue view instead.
  # ISSUE_TRACKER_GITHUB / ISSUE_TRACKER_LOCAL / ISSUE_TRACKER_FORGEJO
  # (agent/entrypoint.sh's phase_prompt_assembly precompute block, derived
  # from ISSUE_TRACKER) are shared by all four per-prompt row triples below --
  # one gate computation, several render sites. Each triple's fragment folds
  # in the following unconditional line(s) too (the trailing `git
  # log`/prior-research-comment bullet, the Inputs: block's git diff/git log
  # lines) rather than leaving them in the template outside the substitution:
  # the fragment loop appends a blank-line separator after every rendered
  # fragment, so a `${VAR}` sitting mid-list/mid-block would otherwise split a
  # tight list or an indented command block in two.
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
    gate = "ISSUE_TRACKER_LOCAL";
    fragment = "scout-issue-read-local.md";
    var = "SCOUT_ISSUE_READ_LOCAL_STEP";
  }
  {
    gate = "ISSUE_TRACKER_FORGEJO";
    fragment = "scout-issue-read-forgejo.md";
    var = "SCOUT_ISSUE_READ_FORGEJO_STEP";
  }
  {
    gate = "ISSUE_TRACKER_GITHUB";
    fragment = "review-issue-read-github.md";
    var = "REVIEW_ISSUE_READ_GITHUB_STEP";
  }
  {
    gate = "ISSUE_TRACKER_LOCAL";
    fragment = "review-issue-read-local.md";
    var = "REVIEW_ISSUE_READ_LOCAL_STEP";
  }
  {
    gate = "ISSUE_TRACKER_FORGEJO";
    fragment = "review-issue-read-forgejo.md";
    var = "REVIEW_ISSUE_READ_FORGEJO_STEP";
  }
  # The local content-plane write step (issue #1692, ADR 0032): a local
  # Dispatch's Box has no in-box tracker client, so it can't run
  # gh issue comment itself -- the research verdict travels as a
  # SPINDRIFT_COMMENT block on stdout instead, and the work blocked-note
  # rides the outcome line's own note= field; settle posts both host-side.
  # Reuses ISSUE_TRACKER_LOCAL (declared once above) for the local case.
  #
  # The github (and jira) case splits further on BOX_FORGE_AND_ISSUE_ACCESS
  # (issue #1917): ISSUE_TRACKER_GITHUB_READWRITE keeps the unconditional
  # in-box `gh issue comment` these two steps always rendered before this
  # split existed; ISSUE_TRACKER_GITHUB_READONLY is new -- a read-only Box
  # holds no write token, so it gets the same host-mediated relay form local
  # always has (settle's ResearchSettle.readOnly / Settle.readOnly gates,
  # generalized off the mode directly, not a LandingRecorder-shaped type
  # assertion). Distinct gates from ISSUE_TRACKER_GITHUB/ISSUE_TRACKER_LOCAL
  # on purpose: the other github/local fragment pairs above (issue-read,
  # scout-issue-read, research-issue-read, review-issue-read) are unaffected
  # by read-only mode -- a read-only token still permits `gh issue view` --
  # so their gate must stay exactly ISSUE_TRACKER_GITHUB/ISSUE_TRACKER_LOCAL.
  #
  # forgejo (issue #1963) mirrors the github split exactly:
  # ISSUE_TRACKER_FORGEJO_READWRITE keeps the in-box `fj issue comment`;
  # ISSUE_TRACKER_FORGEJO_READONLY falls back to the same host-mediated
  # relay form. The read-step forgejo rows above stay gated on the
  # undifferentiated ISSUE_TRACKER_FORGEJO for the same reason the github
  # ones do -- a read-only FORGEJO_TOKEN still permits `fj issue view`.
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
  # The OPEN A PULL REQUEST push step (issue #1918, BOX_FORGE_AND_ISSUE_ACCESS):
  # a read-only github Box holds no push-capable token, so it writes its
  # finished branch as a seam bundle to the outbox instead of git push --
  # the launcher's BundleRelay force-pushes it host-side. BOX_ACCESS_READ_WRITE
  # / BOX_ACCESS_READ_ONLY (agent/entrypoint.sh's phase_prompt_assembly
  # precompute block, derived from BOX_FORGE_AND_ISSUE_ACCESS) follow the same
  # exactly-one-on pairing as ISSUE_TRACKER_GITHUB/ISSUE_TRACKER_LOCAL above.
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
  # The OPEN A PULL REQUEST create step (issue #1919, same BOX_ACCESS_READ_
  # WRITE/BOX_ACCESS_READ_ONLY gates as the push step above): a read-only
  # github Box holds no PR-create-capable token either, so it emits its
  # intended title/body as a single nonce-guarded, base64-encoded
  # SPINDRIFT_PR_INTENT stdout line instead of running `gh pr create` --
  # (issue #1938 moved this off a multi-line block, which never survived
  # Claude Code's stream-json JSONL transport) -- the launcher opens the
  # draft PR host-side from it (forge.DraftPRCreator), the same
  # host-mediation shape RelayBundle already gives the push step.
  {
    gate = "BOX_ACCESS_READ_WRITE";
    fragment = "open-pr-create-git.md";
    var = "OPEN_PR_CREATE_READ_WRITE_STEP";
  }
  {
    gate = "BOX_ACCESS_READ_ONLY";
    fragment = "open-pr-create-outbox.md";
    var = "OPEN_PR_CREATE_READ_ONLY_STEP";
  }
  # The OUTCOME section's landing= value and "what status=ready means" close
  # (issue #1919, same BOX_ACCESS_READ_WRITE/BOX_ACCESS_READ_ONLY gates): a
  # read-only Box never learns a PR URL (it never opens the PR itself), so
  # its landing= carries the branch name instead, and "status=ready" means
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
  # The IF BLOCKED section's push step (issue #1933, same BOX_ACCESS_READ_
  # WRITE/BOX_ACCESS_READ_ONLY gates as the OPEN A PULL REQUEST push step
  # above): a read-only Box holds no push-capable token whether it reaches
  # the happy path or the failure path, so the unconditional "Push what you
  # have" step must not attempt a `git push` under read-only either.
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
  # The IF BLOCKED section's PR check/create step (issue #1933, same
  # BOX_ACCESS_READ_WRITE/BOX_ACCESS_READ_ONLY gates as the push step just
  # above and the OPEN A PULL REQUEST create step): a read-only Box holds no
  # PR-create-capable token in the failure path either, so it emits a
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
  # The IF BLOCKED section's own closing SPINDRIFT_OUTCOME line (issue
  # #1933, same BOX_ACCESS_READ_WRITE/BOX_ACCESS_READ_ONLY gates as OUTCOME's
  # own landing= step above): a read-only Box never opens a PR on the
  # blocked path either, so it never learns a URL and must report the
  # branch name instead.
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
]
