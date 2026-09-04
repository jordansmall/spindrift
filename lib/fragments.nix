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
#   inverseOf - (optional, issue #3219) declares this row's gate the boolean
#              inverse of the named on-gate, for an exactly-one-on pair like
#              the SCOUT_PROVISIONED/SCOUT_ABSENT rows below -- the
#              off-member row carries it, naming the on-member's gate.
#              fragment-pairs.nix's `validate` runs at the end of this file
#              as an eval-time assert, catching a malformed declaration
#              (self-reference, a dangling on-gate, an inverse-of-an-inverse
#              chain, or an off-gate claimed by two different on-gates)
#              before an image bakes from it.
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
    # The worker-role variant of the row above (issue #2706): worker-prompt.md
    # is structurally quarantined from the outcome/verdict marker grammar
    # (lib/prompt-contract.nix's workerForbiddenMarkers) -- a worker never
    # emits SPINDRIFT_OUTCOME/VERDICT: APPROVE/VERDICT: BLOCK/SPINDRIFT_PR_INTENT
    # by design, so caveman-default.md's marker-grammar-is-exempt-too prose
    # (which names those markers verbatim) would leak literal forbidden-marker
    # strings into the rendered worker prompt if reused as-is. This fragment
    # keeps only the `/caveman` default directive and the code/commands/
    # error-messages/commit-messages exemption paragraph; same CAVEMAN_BAKED
    # gate, since a worker dispatch only exists when the same caveman skill is
    # baked. This row duplicates that paragraph from base's prose verbatim
    # rather than composing it from a shared source (issue #2753): drift is
    # caught by
    # cmd/launcher/orchestrator/caveman_default_fragment_parity_test.go.
    {
      gate = "CAVEMAN_BAKED";
      fragment = "caveman-default-worker.md";
      var = "CAVEMAN_STEP_WORKER";
    }
    # The review-role variant of the same row (issue #2707): review-prompt.md
    # keeps the full marker-grammar-is-exempt-too paragraph the worker variant
    # above drops (a review pass still emits a VERDICT line, unlike a worker
    # dispatch, though it never emits SPINDRIFT_OUTCOME/SPINDRIFT_PR_INTENT --
    # those are the coordinator's own markers, not the reviewer's), plus one
    # addition: the VERDICT line and every `## Non-blocking` finding -- the
    # text the orchestrator hands a Filer subagent to become a GitHub issue
    # body -- are called out as exempt from caveman compression too, same
    # tier as the existing commit-message/SPINDRIFT_OUTCOME note= exemptions,
    # since that finding text reaches a human reader cold. Same CAVEMAN_BAKED
    # gate as the two rows above: this row only renders caveman narration
    # into the review prompt when the caveman skill is baked, independent of
    # whether a review pass itself runs (that depends on the orchestrator
    # being on, not on this gate).
    #
    # Unlike the worker row above, this row is not a verbatim duplicate of
    # base: it re-wraps base's marker-grammar-exemption prose (minus the
    # SPINDRIFT_ISSUE_INTENT clause review legitimately omits) and appends its
    # own `## Blocking`/`## Non-blocking` paragraph base has no counterpart
    # for. Issue #2753 weighed three options for this row and the worker row
    # above -- (a) a lightweight parity check, (b) accept the duplication as
    # precedent (the pattern #2706 already established for the worker row),
    # (c) a real composition mechanism -- and chose (a) over (c) as the
    # lower-cost option, rejecting (b) because a third duplicate of the same
    # prose raises the silent-drift risk past what human discipline alone
    # covers. Drift in the spans this row does share with base is caught by
    # cmd/launcher/orchestrator/caveman_default_fragment_parity_test.go.
    {
      gate = "CAVEMAN_BAKED";
      fragment = "caveman-default-review.md";
      var = "CAVEMAN_STEP_REVIEW";
    }
    # The research-role variant of the CAVEMAN_STEP row above (issue #2708,
    # reusing the existing CAVEMAN_BAKED gate): unlike worker, a research pass
    # DOES produce human-facing output -- the posted verdict comment is the
    # product a human reads to decide whether to promote the issue, and a
    # later worker picks up its context-for-a-worker section cold.
    # caveman-default.md's marker-only exemption (naming just the
    # SPINDRIFT_OUTCOME/VERDICT/PR-intent lines) isn't wide enough here, so
    # this fragment instead exempts the entire posted verdict comment in full
    # -- the verdict line and rationale, the context-for-a-worker section, the
    # open-questions section, and the `<!-- spindrift-research -->` machine
    # marker -- on top of the usual SPINDRIFT_OUTCOME/note=/SPINDRIFT_COMMENT
    # exemption. Same CAVEMAN_BAKED gate as the rows above: the gate controls
    # whether this step renders into the prompt, not whether a research
    # dispatch exists at all -- a research dispatch runs with an empty step
    # when the caveman skill isn't baked, same as the CAVEMAN_STEP/
    # CAVEMAN_STEP_WORKER/CAVEMAN_STEP_REVIEW rows.
    {
      gate = "CAVEMAN_BAKED";
      fragment = "caveman-default-research.md";
      var = "CAVEMAN_STEP_RESEARCH";
    }
    # The IMPLEMENT section's test-first step (issue #3219), a paired
    # exactly-one-on fork like SCOUT_PROVISIONED/SCOUT_ABSENT below. Baking
    # the tdd skill now SUBTRACTS prose: the on arm is a bare anchor line
    # pointing at `/tdd`, and the full red/green/refactor fallback moves out
    # of issue-prompt.md into the off arm. The retired shape added a
    # "supersedes the inline steps below" deferral on top of prose the
    # driver still had to read past to reach the skill it was told to obey.
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
    # The COMMIT section's paired fork (issue #3222, same
    # exactly-one-on mechanic as TDD_BAKED/TDD_UNBAKED above): baking the
    # commit skill subtracts the inline Conventional Commits format rules
    # rather than adding a "supersedes the inline rules below" deferral on
    # top of them. The granularity preference ("several small focused
    # commits") stays inline in issue-prompt.md unconditionally -- it is not
    # part of either arm -- because commit-rework-orchestrator.md
    # back-references it by name on both of its own branches.
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
    # The review prompt's dimensions-hunting pair (issue #3222, same
    # exactly-one-on mechanic as TDD/COMMIT above): baking the code-review
    # skill subtracts the inline SPEC/CORRECTNESS/SECURITY/STANDARDS &
    # SMELLS coaching rather than adding a "renders either way" deferral on
    # top of it. The Severity bullets, output shape, and `VERDICT:`
    # first-line grammar (ADR 0035 scanPassLog) stay inline unconditionally
    # in review-prompt.md -- they are machine contract, not coaching, so
    # neither arm owns them.
    {
      gate = "CODE_REVIEW_BAKED";
      fragment = "code-review-baked.md";
      var = "CODE_REVIEW_BAKED_STEP";
    }
    {
      gate = "CODE_REVIEW_UNBAKED";
      fragment = "code-review-unbaked.md";
      var = "CODE_REVIEW_UNBAKED_STEP";
      inverseOf = "CODE_REVIEW_BAKED";
    }
    # The CHECK section's anchor for the harness-owned /check-hygiene skill
    # (issue #3220). Gated on bakedness like its caveman/commit/code-review
    # siblings above rather than inlined into issue-prompt.md: a prompt that
    # names a skill the box does not carry is dead text, and issue #120's
    # "no skill reference when the skills dir is empty" contract holds for
    # every skill, harness-owned or not. Unlike the TDD pair above this row
    # has no inverseOf partner -- the relocated guidance is not restated
    # inline on an off arm, so there is no off arm to author.
    {
      gate = "CHECK_HYGIENE_BAKED";
      fragment = "check-hygiene-default.md";
      var = "CHECK_HYGIENE_STEP";
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
    # The land pass's fixed rebase-then-fix-then-gate work order (issue #3214):
    # reuses the REVIEW_LOOP_ORCHESTRATOR gate above, same as the fold-fix row
    # below -- it's still just the run/pass-scoped ORCHESTRATOR-on-ness, and
    # the fragment scopes itself to the land pass in its own prose (keyed on
    # the seeded handoff's `Last reviewer verdict: APPROVE`), since one prompt
    # serves every pass and a gate can't key on "is this the land pass?". It
    # renders in the REVIEW section, right after review-loop-orchestrator.md's
    # own which-pass-am-I determination and before FILE ISSUES.
    {
      gate = "REVIEW_LOOP_ORCHESTRATOR";
      fragment = "land-pass-order-orchestrator.md";
      var = "LAND_PASS_ORDER_ORCHESTRATOR_STEP";
    }
    # The COMMIT-section fold-fix instruction (issue #2698): reuses the
    # REVIEW_LOOP_ORCHESTRATOR gate above rather than a new one -- it's the
    # same run/pass-scoped ORCHESTRATOR-on-ness, just needs to render earlier,
    # in the COMMIT section, ahead of where the pass actually commits (the
    # REVIEW_LOOP_ORCHESTRATOR fragment itself never acts on findings in the
    # same turn, so the fold-fix instruction can't live there).
    {
      gate = "REVIEW_LOOP_ORCHESTRATOR";
      fragment = "commit-rework-orchestrator.md";
      var = "COMMIT_REWORK_ORCHESTRATOR_STEP";
    }
    # The SCOUT section body (issue #3157), a paired exactly-one-on fork like
    # REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR above. The `# SCOUT` heading
    # in issue-prompt.md must stay a literal, unconditional line --
    # lib/prompt-contract.nix's "comms" injectBlocks row ends right at it.
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
    # The coordinator's own scout-brief guidance (issue #3157). Gated on
    # COORDINATOR_SCOUT_BRIEF -- WorkerProvisioned && ScoutProvisioned -- rather
    # than either alone, since a single row can only carry one gate; the
    # conjunction is computed in gates.go.
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
    # Gates on CODE_COMMENTS_MANDATORY, not WORKER_PROVISIONED -- see that
    # gate's comment in cmd/launcher/internal/promptassembly/gates.go for why.
    {
      gate = "CODE_COMMENTS_MANDATORY";
      fragment = "code-comments.md";
      var = "CODE_COMMENTS_STEP";
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
    #
    # The direct case forks further on ISSUE_TRACKER (issue #1963): fj has no
    # label verb and `fj issue create` has no --label flag, so a forgejo
    # tracker's direct filer writes render the *-forgejo fragments below
    # (curl against the REST API for the label) instead of the gh-flavored
    # ones, now gated FILER_FILE_DIRECT_GH. file-issues-direct.md carries no
    # gh/fj command -- it's forge-agnostic intro guidance -- so it alone stays
    # on the combined FILER_FILE_DIRECT_ANY gate (either direct fork).
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
    # filer-label-relay.md's write-mechanism gate was split by dispatch kind
    # (issue #2593 review finding): the relay write-mechanism itself is
    # identical for work and research (both go through the host-mediated
    # SPINDRIFT_ISSUE_INTENT path, so FILER_FILE_RELAY above stays combined
    # and keeps driving file-issues-relay.md/filer-file-relay.md unchanged
    # for both kinds), but the label the launcher applies host-side once it
    # files each relayed issue differs by kind -- agent-review-finding for
    # work (settle/gate.go), agent-research-finding for research
    # (settle/research.go:97) -- and this fragment's prose names that label
    # explicitly. FILER_FILE_RELAY_WORK/FILER_FILE_RELAY_RESEARCH
    # (gates_tracker.go) are the kind-split view of the same
    # FILER_FILE_RELAY boolean, mutually exclusive and never both true, so
    # exactly one of this row and the one below ever renders.
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
    # The research-only filing step (issue #2593, ADR 0041): reuses the same
    # FILER_FILE_RELAY gate the filer's own write-mechanism steps above use --
    # a prior slice (gates_tracker.go) made that gate unconditionally true
    # whenever DispatchKind == "research" and the Filer is provisioned,
    # regardless of BOX_WRITE_ENABLED or the orchestrator flag, so reusing it
    # here for real gives research prompts their own always-relay filing step
    # for free, with no separate gate needed. Deliberately has no
    # FILER_FILE_DIRECT_* counterpart: a research prompt must never render a
    # direct-file fragment in any mode (this is the only row wiring this
    # fragment in, and neither research-prompt.md nor
    # research-self-contained-prompt.md ever reference a DIRECT-gated
    # variable), so "never renders direct-file instructions" holds by
    # construction. lib/mkHarness.nix's `researchDirectFileCheckOk` (issue
    # #2595, ADR 0041) now also backstops this claim with a build-time check,
    # rather than relying on construction alone.
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
    # The fix-pass CONTEXT CI-read step forks on CODE_FORGE (issue #1963,
    # FIX_CI_READ_GH/FIX_CI_READ_FORGEJO computed in entrypoint.sh): a github
    # fix pass reads CI via `gh pr view`/`gh run list`/`gh run view`, none of
    # which exist against a Forgejo remote, so a forgejo fix pass reads it via
    # `fj pr status` instead. Exactly one of the two gates is ever on, so
    # fix-prompt.md concatenates both vars and only the active one renders --
    # the same conditional-residue mechanism the CODE_FORGE PR-create fork
    # (OPEN_PR_CREATE_RW_GH/_FORGEJO below) already uses.
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
    # The LAND THE CHANGE CODE_FORGE=git push step (issue #2510): the
    # structural forbidden-marker check (lib/prompt-contract.nix's
    # forbiddenMarkers, lib/mkHarness.nix's forbiddenMarkerCheckOk) flags any
    # raw, un-gated "git push" imperative in the shared templates, since it
    # can't tell a genuinely unconditional imperative from one a nix-time gate
    # already protects. The CODE_FORGE=git branch's push line in
    # issue-prompt.md was exactly that: a real, ungated imperative. Moving it
    # into a BOX_ACCESS_READ_WRITE-gated fragment mirrors
    # commit-push-git.md/open-pr-push-git.md's read-write arms. There is no
    # matching BOX_ACCESS_READ_ONLY arm: issue #2526's eval-time assert
    # (lib/mkHarness.nix) makes BOX_FORGE_AND_ISSUE_ACCESS=read-only paired
    # with CODE_FORGE=git fail `nix build` outright, since CODE_FORGE=git
    # implements no bundle-relay mechanism for a host-mediated push, so no
    # image with that combination can ever exist for this step to run in.
    {
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "land-git-push-git.md";
      var = "LAND_GIT_PUSH_READ_WRITE_STEP";
    }
    # The LAND THE CHANGE CODE_FORGE=git block's own final "Print exactly one
    # line and stop" step: identical body text either way, but its leading
    # list number depends on whether a numbered step precedes it in this
    # block. Read-write follows the git-push step above ("1. `git push`
    # ..."), so it must read "2."; read-only has no preceding step at all
    # (the eval-time assert two rows up means there is no
    # BOX_ACCESS_READ_ONLY arm for the push step), so it must read "1." --
    # without this pair, BOX_ACCESS_READ_ONLY renders an orphaned "2." with
    # nothing numbered "1." before it. Same BOX_ACCESS_READ_WRITE/
    # BOX_ACCESS_READ_ONLY exactly-one-on pairing as OPEN_PR_PUSH_READ_WRITE_
    # STEP/OPEN_PR_PUSH_READ_ONLY_STEP below.
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
    # The OPEN A PULL REQUEST push step (issue #1918, BOX_FORGE_AND_ISSUE_ACCESS):
    # a read-only github or forgejo Box holds no push-capable token, so it
    # writes its finished branch as a seam bundle to the outbox instead of git push --
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
    #
    # The read-write case further forks on CODE_FORGE (issue #1963,
    # OPEN_PR_CREATE_RW_GH/OPEN_PR_CREATE_RW_FORGEJO computed in
    # entrypoint.sh): a github Box opens its draft PR with `gh pr create`, a
    # forgejo Box with `fj pr create` (#1961's forgejo PRForge watches CI and
    # merges the box-opened draft host-side). Read-only stays forge-agnostic
    # -- it never forks on CODE_FORGE -- because SPINDRIFT_PR_INTENT is
    # relayed and opened host-side regardless of which forge backend answers.
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
    # The COMMIT section's push step (issue #2462, same BOX_ACCESS_READ_WRITE/
    # BOX_ACCESS_READ_ONLY gates as the OPEN A PULL REQUEST push step above): a
    # read-only Box has no push-capable token, so the unconditional
    # rebase+push+retry block must not render for it -- but the pre-commit
    # rebase-and-recheck step must still render either way, since a stale base
    # is still a stale base regardless of write access.
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
    # The IF BLOCKED section's push-failure triage block (issue #2462, same
    # BOX_ACCESS_READ_WRITE/BOX_ACCESS_READ_ONLY gates as the COMMIT section's
    # push step above): the triage block presupposes a push was attempted and
    # its "Genuine .github/workflows/ change" branch tells the agent to
    # comment on the issue -- neither applies to a read-only Box, which never
    # pushes and never comments directly. A denied push there is the expected
    # outcome of holding no write-capable token, not evidence of a broken or
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
  ];
in
assert (import ./fragment-pairs.nix).validate rows;
rows
