# The Conditional fragment registry (issue #622): one row per opt-in prompt
# step. Box's prompt assembly reads the rows as JSON (lib/mkHarness.nix's
# fragmentsRegistryJson), so a fragment row names every variable it reads.

# A row's `gate` is a bash variable the loop tests for non-emptiness, not a
# Nix boolean, and `fragment` is a basename under prompts/fragments/, from
# templates/default or a SPINDRIFT_PROMPT_DIR override. A row that omits `gate`
# is ungated and renders unconditionally; the export normalizes it to "".
# A row with `axis` ("tracker" | "filer" | "forge", issues #5646/#5647, ADR
# 0068) names its `fragment` relative to a backend folder instead:
# `fragments/<folder>/<fragment>`. <folder> comes from the axis's signal, the
# one its gates test, not from the backend's name: "tracker" the tracker read
# arm (jira rides github's), "filer" the tracker Filer arm (a local tracker's
# rides github's), "forge" the CODE_FORGE arm (git and local ride github's). A
# folder with no such file renders empty unless the row is `shared = true`, which
# falls back to the root-level `fragments/<fragment>` (one body naming the active
# backend through ${FORGE_DISPLAY_NAME}, the `displayName` of a backend row in
# lib/backends/); a folder's own file still wins. ${FORGE_DISPLAY_NAME} is
# bound per row to the folder backend's display name (a plain row takes the
# tracker folder's), and a row whose fragment uses it lists it in
# `extraSubstVars`. An omitted `axis` means a root-level fragment (Go's omitempty
# reads the absent key as ""); lib/fragment-paths.nix expands a row to its
# on-disk paths, and a new axis row must be declared in its `shipped` map.

# `extraSubstVars` (default []) lists allowlist entries a fragment's own body
# interpolates; every other fragment is static prose once its gate is on,
# except for parents splicing in the `var` of a row above them (see the
# Filer twin rows below).
# `inverseOf` (issue #3219) declares this row's gate the boolean inverse of
# the named on-gate; fragment-pairs.nix's `validate` asserts those pairs at
# the end of this file, before an image can bake from a malformed one.

# `signalChannel` (issue #3726) names which of lib/prompt-contract.nix's
# markerChannels ("comment" | "pr-intent" | "issue-intent") a _LOG/_SOCKET
# fragment pair's row belongs to, so buildTimeSignalFragmentViolations can
# derive the required marker/verb from that registry instead of a
# hand-listed per-file table. Only the _LOG/_SOCKET-paired rows carry it;
# every other row is silently skipped by that check, and
# buildTimeSignalFragmentPairViolations (issue #3754) asserts each keeps its
# partner.
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
    # The review-role variant (issue #2707): a review pass emits a VERDICT
    # line, so it keeps the marker-grammar exemption of caveman-default.md,
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
    # The IMPLEMENT and worker-persona test-first step (issues #3219, #6182),
    # an exactly-one-on pair. Baking the tdd skill subtracts prose rather than
    # adding a deferral: the on arm is a bare anchor pointing at `/tdd`, and
    # the red/green/refactor fallback moves out of issue-prompt.md into the
    # off arm, which worker-prompt.md also splices.
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
    # Anchors for the three pstack-derived principle skills, each at the
    # decision point it governs: the skill preamble already names every baked
    # skill, so an anchor's only job is timing. Dogfood-only gates, no off arm.
    # PRINCIPLE_FIX_ROOT_CAUSES_STEP renders twice -- fix-prompt.md's FIX step
    # and issue-prompt.md's CHECK -- guarding the cheapest way to turn CI
    # green: a nil check or loosened assertion that buries the bug.
    {
      gate = "PRINCIPLE_FIX_ROOT_CAUSES_BAKED";
      fragment = "principle-fix-root-causes-default.md";
      var = "PRINCIPLE_FIX_ROOT_CAUSES_STEP";
    }
    # The other two anchor IMPLEMENT and the worker persona (worker-prompt.md,
    # issue #6182), never fix-prompt.md, which already says to make the
    # smallest change and not to refactor or redesign.
    {
      gate = "PRINCIPLE_LAZINESS_PROTOCOL_BAKED";
      fragment = "principle-laziness-protocol-default.md";
      var = "PRINCIPLE_LAZINESS_PROTOCOL_STEP";
    }
    {
      gate = "PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_BAKED";
      fragment = "principle-redesign-from-first-principles-default.md";
      var = "PRINCIPLE_REDESIGN_FROM_FIRST_PRINCIPLES_STEP";
    }
    # The FILE ISSUES step exists only with a Filer (issue #5300), so the
    # prose naming it forks too, as twin sub-fragments the two parent rows
    # below splice in. A fragment may reference only the `var` of a row ABOVE
    # it: rows render in order, so a later row's var is still unset and the
    # literal ${VAR} leaks into the prompt. promptassembly's
    # TestFragmentsReferenceOnlyRowsAbove pins this.
    {
      gate = "FILER_ENABLED";
      fragment = "review-loop-orchestrator-filer.md";
      var = "REVIEW_LOOP_FILER_STEP";
    }
    {
      gate = "FILER_ABSENT";
      fragment = "review-loop-orchestrator-no-filer.md";
      var = "REVIEW_LOOP_NO_FILER_STEP";
      inverseOf = "FILER_ENABLED";
    }
    {
      gate = "FILER_ENABLED";
      fragment = "land-pass-order-finish-filer.md";
      var = "LAND_PASS_FINISH_FILER_STEP";
    }
    {
      gate = "FILER_ABSENT";
      fragment = "land-pass-order-finish-no-filer.md";
      var = "LAND_PASS_FINISH_NO_FILER_STEP";
      inverseOf = "FILER_ENABLED";
    }
    {
      gate = "FILER_ENABLED";
      fragment = "land-pass-order-gate-default-filer.md";
      var = "LAND_PASS_GATE_DEFAULT_FILER_STEP";
    }
    {
      gate = "FILER_ABSENT";
      fragment = "land-pass-order-gate-default-no-filer.md";
      var = "LAND_PASS_GATE_DEFAULT_NO_FILER_STEP";
      inverseOf = "FILER_ENABLED";
    }
    # The REVIEW section (issue #2037, ADR 0035): the orchestrator drives the
    # review as its own pass, so each pass's prompt stops after COMMIT unless a
    # prior review pass's APPROVE is already visible in the seeded run-state
    # handoff. These three rows are ungated: the orchestrator is the only Box
    # path, so no switch is left to condition them on.
    {
      fragment = "review-loop-orchestrator.md";
      var = "REVIEW_LOOP_ORCHESTRATOR_STEP";
    }
    # The land pass's rebase-then-fix-then-gate work order (issue #3214). One
    # prompt serves every pass, so a gate cannot key on "is this the land
    # pass?"; the fragment scopes itself in its own prose, keyed on the seeded
    # handoff's `Last reviewer verdict: APPROVE`.
    {
      fragment = "land-pass-order-orchestrator.md";
      var = "LAND_PASS_ORDER_ORCHESTRATOR_STEP";
    }
    # The COMMIT-section fold-fix instruction (issue #2698): must render
    # earlier, in COMMIT, ahead of where the pass commits. The review-loop
    # fragment itself never acts on findings in the same turn, so the
    # instruction cannot live there.
    {
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
    # The triage recap opening all three FILE ISSUES rows below, spliced in
    # rather than copied three times (issue #6184); issue-prompt.md never
    # names its var.
    {
      gate = "FILER_ENABLED";
      fragment = "file-issues-triage-recap.md";
      var = "FILE_ISSUES_TRIAGE_RECAP_STEP";
    }

    # The write-mechanism split (issue #2019): a filer under read-only holds
    # no write token, so its label and file steps emit the host-mediated
    # SPINDRIFT_ISSUE_INTENT relay instead of gh label create/gh issue create.
    # Only the write mechanism is gated here; the filer's authoring judgment
    # stays in filer-prompt.md's unconditional dedup/title/body steps (the
    # dedup search commands fork separately on the read axis, below).

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
    # The carrier (issue #3726) forks the issue-filing relay the same way as
    # the comment/pr-intent channels above: a read-only Filer emits either a
    # SPINDRIFT_ISSUE_INTENT stdout log line or a `driver-exec signal
    # issue-intent` socket call, so this row and its socket sibling fork on
    # BOX_SIGNAL_CARRIER via the FILER_FILE_RELAY_LOG/_SOCKET gate pair
    # gates.go computes.
    {
      gate = "FILER_FILE_RELAY_LOG";
      fragment = "file-issues-relay.md";
      var = "FILE_ISSUES_RELAY_STEP";
      signalChannel = "issue-intent";
    }
    {
      gate = "FILER_FILE_RELAY_SOCKET";
      fragment = "file-issues-relay-socket.md";
      var = "FILE_ISSUES_RELAY_SOCKET_STEP";
      signalChannel = "issue-intent";
    }
    # One slot per direct step (issue #5647, ADR 0068): fj has no label verb,
    # so each Filer folder carries its own file, and the gate is the combined
    # one because the folder already picks the arm.
    {
      axis = "filer";
      gate = "FILER_FILE_DIRECT_ANY";
      fragment = "filer-direct-label.md";
      var = "FILER_DIRECT_LABEL_STEP";
    }
    # Deprecation-window fills for override prompts still naming the old vars.
    {
      gate = "FILER_FILE_DIRECT_GH";
      fragment = "github/filer-direct-label.md";
      legacyFragment = "filer-label-direct.md";
      var = "FILER_LABEL_DIRECT_STEP";
    }
    {
      gate = "FILER_FILE_DIRECT_FORGEJO";
      fragment = "forgejo/filer-direct-label.md";
      legacyFragment = "filer-label-direct-forgejo.md";
      var = "FILER_LABEL_DIRECT_FORGEJO_STEP";
    }
    # filer-label-relay.md splits by dispatch kind (issue #2593): the relay
    # mechanism is identical for work and research, but the label the launcher
    # applies host-side differs (agent-review-finding for work,
    # agent-research-finding for research -- each kind's lib/dispatch-kinds row
    # FindingLabel) and this fragment's prose names it. The two gates are
    # never both true.
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
    # The butler's own label fork (ADR 0056, issue #3875): same relay
    # mechanism as work/research, but the launcher applies
    # agent-butler-finding, not agent-review-finding or
    # agent-research-finding, so this needs its own prose row too.
    {
      gate = "FILER_FILE_RELAY_BUTLER";
      fragment = "filer-label-relay-butler.md";
      var = "FILER_LABEL_RELAY_BUTLER_STEP";
    }
    # The records-scoped Chore's cite/metric hand-off (issue #5008): fragments
    # do not nest, so this is its own row spliced right after the row above.
    # CHORE_INPUT is only ever forwarded to a butler run, so it alone gates it.
    {
      gate = "CHORE_INPUT";
      fragment = "filer-label-relay-butler-records.md";
      var = "FILER_LABEL_RELAY_BUTLER_RECORDS_STEP";
    }
    # Step 2's tracker search commands fork on the read axis (issue #5127),
    # not FILER_FILE_DIRECT_GH: a relay Filer still reads through the tracker.
    # jira has no read axis (lib/backends/jira.nix, no trackerAxisRead), so
    # it takes the GitHub arm like every other ISSUE_TRACKER_GITHUB fragment.
    # One slot (issue #5647, ADR 0068): each backend's search commands differ
    # in more than its name, so each folder carries its own file.
    {
      axis = "tracker";
      fragment = "filer-dedup.md";
      var = "FILER_DEDUP_STEP";
    }
    # Deprecation-window fills for override prompts still naming the old vars.
    {
      gate = "ISSUE_TRACKER_GITHUB";
      fragment = "github/filer-dedup.md";
      legacyFragment = "filer-dedup-search.md";
      var = "FILER_DEDUP_SEARCH_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO";
      fragment = "forgejo/filer-dedup.md";
      legacyFragment = "filer-dedup-search-forgejo.md";
      var = "FILER_DEDUP_SEARCH_FORGEJO_STEP";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL";
      fragment = "local/filer-dedup.md";
      legacyFragment = "filer-dedup-search-local.md";
      var = "FILER_DEDUP_SEARCH_LOCAL_STEP";
    }
    {
      axis = "filer";
      gate = "FILER_FILE_DIRECT_ANY";
      fragment = "filer-direct-file.md";
      var = "FILER_DIRECT_FILE_STEP";
    }
    {
      gate = "FILER_FILE_DIRECT_GH";
      fragment = "github/filer-direct-file.md";
      legacyFragment = "filer-file-direct.md";
      var = "FILER_FILE_DIRECT_STEP";
    }
    {
      gate = "FILER_FILE_DIRECT_FORGEJO";
      fragment = "forgejo/filer-direct-file.md";
      legacyFragment = "filer-file-direct-forgejo.md";
      var = "FILER_FILE_DIRECT_FORGEJO_STEP";
    }
    {
      gate = "FILER_FILE_RELAY_LOG";
      fragment = "filer-file-relay.md";
      var = "FILER_FILE_RELAY_STEP";
      signalChannel = "issue-intent";
    }
    {
      gate = "FILER_FILE_RELAY_SOCKET";
      fragment = "filer-file-relay-socket.md";
      var = "FILER_FILE_RELAY_SOCKET_STEP";
      signalChannel = "issue-intent";
    }
    # The research-only filing step (issue #2593, ADR 0041): reuses the
    # FILER_FILE_RELAY gate, which gates_tracker.go makes unconditionally true
    # for a research dispatch with a Filer, so no separate gate is needed. A
    # research prompt must never render a direct-file fragment in any mode;
    # `researchDirectFileCheckOk` (issue #2595) backstops that at build time.
    {
      gate = "FILER_FILE_RELAY_LOG";
      fragment = "research-file-issues-relay.md";
      var = "RESEARCH_FILE_ISSUES_RELAY_STEP";
      signalChannel = "issue-intent";
    }
    {
      gate = "FILER_FILE_RELAY_SOCKET";
      fragment = "research-file-issues-relay-socket.md";
      var = "RESEARCH_FILE_ISSUES_RELAY_SOCKET_STEP";
      signalChannel = "issue-intent";
    }
    # The butler's own file-findings step (ADR 0056, issue #3875): reuses the
    # same FILER_FILE_RELAY_LOG/_SOCKET gates as the research rows above (the
    # butler is advise-only too), but its own var/fragment, since it names a
    # Chore rather than an issue number and has no verdict comment to fold
    # the finding into.
    {
      gate = "FILER_FILE_RELAY_LOG";
      fragment = "butler-file-issues-relay.md";
      var = "BUTLER_FILE_ISSUES_RELAY_STEP";
      signalChannel = "issue-intent";
    }
    {
      gate = "FILER_FILE_RELAY_SOCKET";
      fragment = "butler-file-issues-relay-socket.md";
      var = "BUTLER_FILE_ISSUES_RELAY_SOCKET_STEP";
      signalChannel = "issue-intent";
    }
    # The butler's patch-instructions step (ADR 0057, issue #4073): the
    # forwarded CHORE_PATCH_CLASSES fact's presence is its own gate, same
    # shape as CI_FAILURE_SUMMARY below, so a run with the patch rung off (or
    # today's patch room spent) carries no patch prose at all.
    {
      gate = "CHORE_PATCH_CLASSES";
      fragment = "butler-patch.md";
      var = "BUTLER_PATCH_STEP";
    }
    # The butler reviewer's patch rubric line (ADR 0057, issue #4073): same
    # gate as the row above, so the "Exact" rubric item only renders into
    # the reviewer prompt on a run that can actually hand it a diff.
    {
      gate = "CHORE_PATCH_CLASSES";
      fragment = "butler-review-patch.md";
      var = "BUTLER_REVIEW_PATCH_STEP";
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
    # One slot (issue #5647, ADR 0068): the forge's folder carries the file.
    {
      axis = "forge";
      fragment = "fix-ci-read.md";
      var = "FIX_CI_READ_STEP";
    }
    {
      gate = "FIX_CI_READ_GH";
      fragment = "github/fix-ci-read.md";
      legacyFragment = "fix-ci-read-github.md";
      var = "FIX_CI_READ_GITHUB_STEP";
    }
    {
      gate = "FIX_CI_READ_FORGEJO";
      fragment = "forgejo/fix-ci-read.md";
      legacyFragment = "fix-ci-read-forgejo.md";
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
    # The local pair is two slots keyed on LOCAL_ISSUE_REFERENCE, not on the
    # backend's name (issue #5647): only local/ carries their files, so every
    # other tracker renders them empty.
    {
      axis = "tracker";
      gate = "PR_BODY_ISSUE_REF";
      fragment = "pr-body-issue-ref.md";
      var = "PR_BODY_ISSUE_REF_STEP";
    }
    {
      axis = "tracker";
      gate = "PR_BODY_NO_ISSUE_REF";
      fragment = "pr-body-no-issue-ref.md";
      var = "PR_BODY_NO_ISSUE_REF_STEP";
    }
    {
      gate = "PR_BODY_LOCAL_REF";
      fragment = "local/pr-body-issue-ref.md";
      legacyFragment = "pr-body-local-ref.md";
      var = "PR_BODY_LOCAL_REF_STEP";
    }
    {
      gate = "PR_BODY_LOCAL_NOREF";
      fragment = "local/pr-body-no-issue-ref.md";
      legacyFragment = "pr-body-local-noref.md";
      var = "PR_BODY_LOCAL_NOREF_STEP";
    }
    # The issue-read step (issues #1691, #1963, ADR 0032): the subject issue's
    # body and comments are injected host-side (issues #3445, #3469,
    # promptassembly.issueTextSection), so these fragments cover only a
    # *linked* issue. Each folds the trailing `git log` bullet in: the loop
    # adds a blank line per fragment, so a mid-list `${VAR}` would split it.
    {
      axis = "tracker";
      shared = true;
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      fragment = "issue-read.md";
      var = "ISSUE_READ_STEP";
    }
    # Deprecation-window fill for override prompts that still name the
    # per-backend vars (issues #5646, #5647); shipped prompts use the slot
    # vars. legacyFragment is the pre-move root file, read only when
    # `fragment` is missing, so an override dir still on the old layout
    # renders as before.
    {
      gate = "ISSUE_TRACKER_GITHUB";
      fragment = "issue-read.md";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      legacyFragment = "issue-read-github.md";
      var = "ISSUE_READ_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL";
      fragment = "local/issue-read.md";
      legacyFragment = "issue-read-local.md";
      var = "ISSUE_READ_LOCAL_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO";
      fragment = "issue-read.md";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      legacyFragment = "issue-read-forgejo.md";
      var = "ISSUE_READ_FORGEJO_STEP";
    }
    # The research issue-read step (issue #5647): one shared body naming the
    # tracker; local's differs in more than its name and has its own file.
    {
      axis = "tracker";
      shared = true;
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      fragment = "research-issue-read.md";
      var = "RESEARCH_ISSUE_READ_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB";
      fragment = "research-issue-read.md";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      legacyFragment = "research-issue-read-github.md";
      var = "RESEARCH_ISSUE_READ_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL";
      fragment = "local/research-issue-read.md";
      legacyFragment = "research-issue-read-local.md";
      var = "RESEARCH_ISSUE_READ_LOCAL_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO";
      fragment = "research-issue-read.md";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      legacyFragment = "research-issue-read-forgejo.md";
      var = "RESEARCH_ISSUE_READ_FORGEJO_STEP";
    }
    # The scout's issue-read step (issue #5647): every tracker the Box can
    # reach, so local (which has none in-Box) renders nothing.
    {
      axis = "tracker";
      shared = true;
      gate = "ISSUE_TRACKER_REACHABLE";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      fragment = "scout-issue-read.md";
      var = "SCOUT_ISSUE_READ_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB";
      fragment = "scout-issue-read.md";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      legacyFragment = "scout-issue-read-github.md";
      var = "SCOUT_ISSUE_READ_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO";
      fragment = "scout-issue-read.md";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      legacyFragment = "scout-issue-read-forgejo.md";
      var = "SCOUT_ISSUE_READ_FORGEJO_STEP";
    }
    # The local content-plane write step (issue #1692, ADR 0032): a local Box
    # has no in-box tracker client, so the research verdict travels as a
    # SPINDRIFT_COMMENT block on stdout and the work blocked-note rides the
    # outcome line's note= field; settle posts both host-side.

    # github and forgejo split further on BOX_FORGE_AND_ISSUE_ACCESS
    # (issues #1917, #1963): a read-only Box holds no write token, so it gets
    # the same host-mediated relay local always has. The read steps above are
    # not split on access, since a read-only token still permits `gh issue
    # view` / `fj issue view`; only the deprecation fills gate per backend.
    {
      axis = "tracker";
      gate = "ISSUE_TRACKER_READWRITE";
      fragment = "research-verdict.md";
      var = "RESEARCH_VERDICT_STEP";
    }
    # The carrier (issue #3726) is orthogonal to the tracker backend: a
    # read-only/local research Box relays its verdict either as a stdout
    # SPINDRIFT_COMMENT log line or over the Signal socket, so the relay
    # slot forks in two on BOX_SIGNAL_CARRIER via the _LOG/_SOCKET gate pair
    # gates.go computes. The registry only allows one gate per row, so the
    # conjunction itself lives in gates.go, not here. The read-write rows
    # are untouched: a write-capable Box posts the comment with its own
    # tracker client and never uses either carrier. github and forgejo share
    # one relay body; local keeps its own.
    {
      axis = "tracker";
      shared = true;
      gate = "ISSUE_TRACKER_RELAYED_LOG";
      fragment = "research-verdict-relay.md";
      var = "RESEARCH_VERDICT_RELAY_STEP";
      signalChannel = "comment";
    }
    {
      axis = "tracker";
      shared = true;
      gate = "ISSUE_TRACKER_RELAYED_SOCKET";
      fragment = "research-verdict-relay-socket.md";
      var = "RESEARCH_VERDICT_RELAY_SOCKET_STEP";
      signalChannel = "comment";
    }
    # Deprecation-window fill for override prompts that still name the
    # per-backend vars (issue #5647); shipped prompts use the slot vars above.
    {
      gate = "ISSUE_TRACKER_GITHUB_READWRITE";
      fragment = "github/research-verdict.md";
      legacyFragment = "research-verdict-github.md";
      var = "RESEARCH_VERDICT_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB_READONLY_LOG";
      fragment = "research-verdict-relay.md";
      legacyFragment = "research-verdict-github-readonly.md";
      var = "RESEARCH_VERDICT_GITHUB_READONLY_STEP";
      signalChannel = "comment";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB_READONLY_SOCKET";
      fragment = "research-verdict-relay-socket.md";
      legacyFragment = "research-verdict-github-readonly-socket.md";
      var = "RESEARCH_VERDICT_GITHUB_READONLY_SOCKET_STEP";
      signalChannel = "comment";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL_LOG";
      fragment = "local/research-verdict-relay.md";
      legacyFragment = "research-verdict-local.md";
      var = "RESEARCH_VERDICT_LOCAL_STEP";
      signalChannel = "comment";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL_SOCKET";
      fragment = "local/research-verdict-relay-socket.md";
      legacyFragment = "research-verdict-local-socket.md";
      var = "RESEARCH_VERDICT_LOCAL_SOCKET_STEP";
      signalChannel = "comment";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READWRITE";
      fragment = "forgejo/research-verdict.md";
      legacyFragment = "research-verdict-forgejo.md";
      var = "RESEARCH_VERDICT_FORGEJO_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READONLY_LOG";
      fragment = "research-verdict-relay.md";
      legacyFragment = "research-verdict-forgejo-readonly.md";
      var = "RESEARCH_VERDICT_FORGEJO_READONLY_STEP";
      signalChannel = "comment";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READONLY_SOCKET";
      fragment = "research-verdict-relay-socket.md";
      legacyFragment = "research-verdict-forgejo-readonly-socket.md";
      var = "RESEARCH_VERDICT_FORGEJO_READONLY_SOCKET_STEP";
      signalChannel = "comment";
    }
    # The research OUTCOME's `landing` guidance (issue #5130): only a
    # write-capable Box posts the comment itself and sees a URL; every
    # relayed path (read-only, local, Filer-forced) reports landing=none.
    {
      axis = "tracker";
      gate = "ISSUE_TRACKER_READWRITE";
      fragment = "research-outcome-landing.md";
      var = "RESEARCH_OUTCOME_LANDING_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB_READWRITE";
      fragment = "github/research-outcome-landing.md";
      legacyFragment = "research-outcome-landing-github.md";
      var = "RESEARCH_OUTCOME_LANDING_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READWRITE";
      fragment = "forgejo/research-outcome-landing.md";
      legacyFragment = "research-outcome-landing-forgejo.md";
      var = "RESEARCH_OUTCOME_LANDING_FORGEJO_STEP";
    }
    # The relayed variant forks on carrier: only the socket acknowledges a
    # relay (`signal comment accepted`), so the log wording says what counts.
    {
      gate = "RESEARCH_VERDICT_RELAYED_LOG";
      fragment = "research-outcome-landing-relayed.md";
      var = "RESEARCH_OUTCOME_LANDING_RELAYED_STEP";
      signalChannel = "comment";
    }
    {
      gate = "RESEARCH_VERDICT_RELAYED_SOCKET";
      fragment = "research-outcome-landing-relayed-socket.md";
      var = "RESEARCH_OUTCOME_LANDING_RELAYED_SOCKET_STEP";
      signalChannel = "comment";
    }
    # The work IF BLOCKED comment step: a write-capable Box comments itself;
    # a relayed one (read-only, local) rides the outcome line's note= field.
    {
      axis = "tracker";
      gate = "ISSUE_TRACKER_READWRITE";
      fragment = "issue-blocked-comment.md";
      var = "ISSUE_BLOCKED_COMMENT_STEP";
    }
    {
      axis = "tracker";
      shared = true;
      gate = "ISSUE_TRACKER_RELAYED";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      fragment = "issue-blocked-comment-relay.md";
      var = "ISSUE_BLOCKED_COMMENT_RELAY_STEP";
    }
    # A tracker the Box reads but cannot write (jira, issue #5263): the same
    # host-posted note= relay as local, but only on a write-capable Box.
    {
      gate = "ISSUE_TRACKER_UNWRITABLE_READWRITE";
      fragment = "issue-blocked-comment-unwritable.md";
      var = "ISSUE_BLOCKED_COMMENT_UNWRITABLE_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB_READWRITE";
      fragment = "github/issue-blocked-comment.md";
      legacyFragment = "issue-blocked-comment-github.md";
      var = "ISSUE_BLOCKED_COMMENT_GITHUB_STEP";
    }
    {
      gate = "ISSUE_TRACKER_GITHUB_READONLY";
      fragment = "issue-blocked-comment-relay.md";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      legacyFragment = "issue-blocked-comment-github-readonly.md";
      var = "ISSUE_BLOCKED_COMMENT_GITHUB_READONLY_STEP";
    }
    {
      gate = "ISSUE_TRACKER_LOCAL";
      fragment = "local/issue-blocked-comment-relay.md";
      legacyFragment = "issue-blocked-comment-local.md";
      var = "ISSUE_BLOCKED_COMMENT_LOCAL_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READWRITE";
      fragment = "forgejo/issue-blocked-comment.md";
      legacyFragment = "issue-blocked-comment-forgejo.md";
      var = "ISSUE_BLOCKED_COMMENT_FORGEJO_STEP";
    }
    {
      gate = "ISSUE_TRACKER_FORGEJO_READONLY";
      fragment = "issue-blocked-comment-relay.md";
      extraSubstVars = [ "FORGE_DISPLAY_NAME" ];
      legacyFragment = "issue-blocked-comment-forgejo-readonly.md";
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
    # One slot (issue #5647, ADR 0068): the forge's folder carries the file.
    {
      axis = "forge";
      gate = "BOX_ACCESS_READ_WRITE";
      fragment = "open-pr-create.md";
      var = "OPEN_PR_CREATE_DIRECT_STEP";
    }
    {
      gate = "OPEN_PR_CREATE_RW_GH";
      fragment = "github/open-pr-create.md";
      legacyFragment = "open-pr-create-git.md";
      var = "OPEN_PR_CREATE_READ_WRITE_STEP";
    }
    {
      gate = "OPEN_PR_CREATE_RW_FORGEJO";
      fragment = "forgejo/open-pr-create.md";
      legacyFragment = "open-pr-create-forgejo.md";
      var = "OPEN_PR_CREATE_READ_WRITE_FORGEJO_STEP";
    }
    # The carrier (issue #3726) forks the PR-intent send itself: a read-only
    # Box emits the nonce-guarded SPINDRIFT_PR_INTENT stdout line above, or
    # sends the same intent over the Signal socket via `driver-exec signal
    # pr-intent`, per BOX_SIGNAL_CARRIER's _LOG/_SOCKET gate pair.
    {
      gate = "BOX_ACCESS_READ_ONLY_LOG";
      fragment = "open-pr-create-outbox.md";
      var = "OPEN_PR_CREATE_READ_ONLY_STEP";
      signalChannel = "pr-intent";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY_SOCKET";
      fragment = "open-pr-create-outbox-socket.md";
      var = "OPEN_PR_CREATE_READ_ONLY_SOCKET_STEP";
      signalChannel = "pr-intent";
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
      gate = "BOX_PUSH_READ_WRITE";
      fragment = "commit-push-git.md";
      var = "COMMIT_PUSH_READ_WRITE_STEP";
    }
    # Host-mediated read-write (issue #5251): the clone source is read-only.
    {
      gate = "HOST_MEDIATED_READ_WRITE";
      fragment = "commit-push-host-mediated.md";
      var = "COMMIT_PUSH_HOST_MEDIATED_STEP";
    }
    {
      gate = "HOST_MEDIATED_READ_WRITE";
      fragment = "commit-push-host-mediated.md";
      legacyFragment = "commit-push-local.md";
      var = "COMMIT_PUSH_LOCAL_STEP";
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
      gate = "BOX_PUSH_READ_WRITE";
      fragment = "if-blocked-triage-git.md";
      var = "IF_BLOCKED_TRIAGE_READ_WRITE_STEP";
    }
    # Host-mediated read-write (issue #5251): no push exists to triage.
    {
      gate = "HOST_MEDIATED_READ_WRITE";
      fragment = "if-blocked-triage-host-mediated.md";
      var = "IF_BLOCKED_TRIAGE_HOST_MEDIATED_STEP";
    }
    {
      gate = "HOST_MEDIATED_READ_WRITE";
      fragment = "if-blocked-triage-host-mediated.md";
      legacyFragment = "if-blocked-triage-local.md";
      var = "IF_BLOCKED_TRIAGE_LOCAL_STEP";
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
      gate = "BOX_PUSH_READ_WRITE";
      fragment = "if-blocked-push-git.md";
      var = "IF_BLOCKED_PUSH_READ_WRITE_STEP";
    }
    # Host-mediated read-write (issue #5251): hand the branch to the harness.
    {
      gate = "HOST_MEDIATED_READ_WRITE";
      fragment = "if-blocked-push-host-mediated.md";
      var = "IF_BLOCKED_PUSH_HOST_MEDIATED_STEP";
    }
    {
      gate = "HOST_MEDIATED_READ_WRITE";
      fragment = "if-blocked-push-host-mediated.md";
      legacyFragment = "if-blocked-push-local.md";
      var = "IF_BLOCKED_PUSH_LOCAL_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "if-blocked-push-outbox.md";
      var = "IF_BLOCKED_PUSH_READ_ONLY_STEP";
    }
    # The IF BLOCKED PR check/create step (issue #1933): a read-only Box holds
    # no PR-create-capable token on the failure path either, so it emits a
    # SPINDRIFT_PR_INTENT line instead of running `gh pr view`/`gh pr create`.
    # The read-write case forks on CODE_FORGE (issue #5099), as the OPEN A PULL
    # REQUEST create step does: a forgejo Box has no `gh` for the PR check. A
    # git or local Box has no PR at all (issue #5233), so it gets a no-PR step
    # in the same slot.
    {
      axis = "forge";
      gate = "PR_FORGE_READ_WRITE";
      fragment = "if-blocked-pr.md";
      var = "IF_BLOCKED_PR_DIRECT_STEP";
    }
    {
      gate = "IF_BLOCKED_PR_RW_GH";
      fragment = "github/if-blocked-pr.md";
      legacyFragment = "if-blocked-pr-git.md";
      var = "IF_BLOCKED_PR_READ_WRITE_STEP";
    }
    {
      gate = "IF_BLOCKED_PR_RW_FORGEJO";
      fragment = "forgejo/if-blocked-pr.md";
      legacyFragment = "if-blocked-pr-forgejo.md";
      var = "IF_BLOCKED_PR_READ_WRITE_FORGEJO_STEP";
    }
    {
      gate = "NO_PR_FORGE_READ_WRITE";
      fragment = "if-blocked-pr-none.md";
      var = "IF_BLOCKED_NO_PR_STEP";
    }
    # Same carrier fork (issue #3726) on the IF BLOCKED path's PR-intent send.
    {
      gate = "BOX_ACCESS_READ_ONLY_LOG";
      fragment = "if-blocked-pr-outbox.md";
      var = "IF_BLOCKED_PR_READ_ONLY_STEP";
      signalChannel = "pr-intent";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY_SOCKET";
      fragment = "if-blocked-pr-outbox-socket.md";
      var = "IF_BLOCKED_PR_READ_ONLY_SOCKET_STEP";
      signalChannel = "pr-intent";
    }
    # The IF BLOCKED section's closing SPINDRIFT_OUTCOME line (issue #1933): a
    # read-only Box never opens a PR on the blocked path either, so it never
    # learns a URL and must report the branch name instead. So does a
    # read-write Box on a PR-less forge (issue #5233).
    {
      gate = "PR_FORGE_READ_WRITE";
      fragment = "if-blocked-outcome-landing-git.md";
      var = "IF_BLOCKED_OUTCOME_LANDING_READ_WRITE_STEP";
    }
    {
      gate = "NO_PR_FORGE_READ_WRITE";
      fragment = "if-blocked-outcome-landing-branch.md";
      var = "IF_BLOCKED_OUTCOME_LANDING_BRANCH_STEP";
    }
    {
      gate = "BOX_ACCESS_READ_ONLY";
      fragment = "if-blocked-outcome-landing-outbox.md";
      var = "IF_BLOCKED_OUTCOME_LANDING_READ_ONLY_STEP";
    }
  ];
  normalized = map (r: { gate = ""; } // r) rows;
in
assert (import ./fragment-pairs.nix).validate normalized;
normalized
