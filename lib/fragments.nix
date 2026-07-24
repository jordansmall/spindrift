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
  {
    gate = "FILER_ENABLED";
    fragment = "file-issues.md";
    var = "FILE_ISSUES_STEP";
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
  # The issue-read step (issue #1691, ADR 0032): local issues are read from
  # the read-only /issues mount instead of gh issue view. ISSUE_TRACKER_GITHUB
  # / ISSUE_TRACKER_LOCAL (agent/entrypoint.sh's phase_prompt_assembly
  # precompute block, derived from ISSUE_TRACKER) are shared by all four
  # per-prompt row pairs below -- one gate computation, several render sites.
  # Each pair's fragment folds in the following unconditional line(s) too
  # (the trailing `git log`/prior-research-comment bullet, the Inputs: block's
  # git diff/git log lines) rather than leaving them in the template outside
  # the substitution: the fragment loop appends a blank-line separator after
  # every rendered fragment, so a `${VAR}` sitting mid-list/mid-block would
  # otherwise split a tight list or an indented command block in two.
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
    gate = "ISSUE_TRACKER_GITHUB";
    fragment = "review-issue-read-github.md";
    var = "REVIEW_ISSUE_READ_GITHUB_STEP";
  }
  {
    gate = "ISSUE_TRACKER_LOCAL";
    fragment = "review-issue-read-local.md";
    var = "REVIEW_ISSUE_READ_LOCAL_STEP";
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
]
