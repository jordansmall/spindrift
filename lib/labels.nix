# The label registry (issue #2528): the one root every label family the
# Harness writes hangs off of -- work-tier (operator-configurable via
# lib/env-schema.nix), research-tier (ADR 0022, fixed names), the
# researchVerdicts sourced from lib/research-verdicts.nix's defaultVerdicts,
# priority-tier (ADR 0040), the ambiguous-spec label, the local-only
# recoverable marker, the reviewFinding provenance label the Filer creates
# directly (never through doctor), and the trigger-only vocabulary. Rendered
# into
# cmd/launcher/internal/doctor/labelmeta_gen.go by lib/renderers.nix's
# renderLabelRegistryGo, guarded against drift by
# nix/checks/schema-drift.nix's label-registry-gen check, written by
# `nix run .#regen`. Before this registry, doctor.go hand-typed the whole
# TriageLabelMeta map as a Go literal -- a name/color/description could drift
# from the schema default (work tier) or from research-verdicts.nix's verdict
# label (research-verdict tier) with nothing to catch it.
#
# A plain attrset (no `{ lib }:` wrapper), mirroring lib/research-verdicts.nix
# and lib/env-schema.nix, so it stays importable with zero arguments.
let
  schema = import ./env-schema.nix;
  verdicts = import ./research-verdicts.nix;

  # Verdict-terminal colors, keyed by verdict token -- paired below against
  # research-verdicts.nix's defaultVerdicts so the label *name* is never
  # retyped here (issue #2528).
  #
  # Deliberately NOT also sourcing `description` from defaultVerdicts: that
  # file's descriptions are prompt-facing prose explaining verdict semantics
  # to the research agent (long, second-person-adjacent, e.g. "false
  # positive, not worth doing, or a duplicate. Name the duplicate issue by
  # number..."), while a GitHub label description is a short, third-person
  # summary for a human skimming the repo's Labels page (e.g. "False
  # positive, not worth it, or a duplicate — close it"). These are two
  # different audiences with independently-worded text today
  # (docs/reference.md's `gh label create` snippets carry the short form);
  # collapsing them onto one string would either bloat the GitHub label UI
  # with prompt prose or blunt the prompt's guidance to a label-sized
  # summary. Only the *label name* -- the one fact that must never drift
  # between "which label does verdict X map to" in the prompt and in
  # doctor -- is shared.
  verdictColors = {
    recommend = "2cbe4e";
    reject = "e11d21";
    unclear = "d4c5f9";
  };
  verdictDescriptions = {
    recommend = "Relevant and enriched — promote it";
    reject = "False positive, not worth it, or a duplicate — close it";
    unclear = "Needs a human answer — answer, then re-apply agent-research";
  };
  # role names for verdict-terminal rows, keyed by verdict token -- a small
  # hardcoded map is simpler than a generic PascalCase helper for a set of
  # three tokens that isn't expected to grow arbitrarily (a fourth verdict
  # token showing up here without a matching role throws below, same as an
  # unknown backend field in lib/backends/default.nix).
  verdictRoles = {
    recommend = "ResearchVerdictRecommend";
    reject = "ResearchVerdictReject";
    unclear = "ResearchVerdictUnclear";
  };
in
{
  # Work-tier: operator-configurable names (schema knobs flow through so a
  # renamed LABEL/IN_PROGRESS_LABEL/FAILED_LABEL/COMPLETE_LABEL still gets
  # correct metadata) -- role is the Go identifier suffix the generated
  # per-role Meta<Role> vars use, so doctor.go can resolve a work-tier
  # label's metadata by role even after a rename.
  work = [
    {
      role = "Dispatchable";
      name = schema.label.default;
      color = "0075ca";
      description = "Fully specified; ready for an AFK agent";
    }
    {
      role = "InProgress";
      name = schema.inProgressLabel.default;
      color = "e4e669";
      description = "An AFK agent is actively working this issue";
    }
    {
      role = "Failed";
      name = schema.failedLabel.default;
      color = "d93f0b";
      description = "Box exited non-zero; needs human triage";
    }
    {
      role = "Complete";
      name = schema.completeLabel.default;
      color = "0e8a16";
      description = "Agent work merged and green";
    }
  ];

  # Research standing/in-progress/failed: fixed names (ADR 0022), never
  # operator-configurable.
  research = [
    {
      role = "ResearchDispatchable";
      name = "agent-research";
      color = "fbca04";
      description = "Apply to fire a research dispatch";
    }
    {
      role = "ResearchInProgress";
      name = "agent-research-in-progress";
      color = "bfd4f2";
      description = "A Box is reviewing this issue";
    }
    {
      role = "ResearchFailed";
      name = "agent-research-failed";
      color = "b60205";
      description = "Box crashed or produced no verdict; needs human triage";
    }
  ];

  # Verdict-terminal rows: name sourced from research-verdicts.nix's
  # defaultVerdicts (never retyped here); color/description/role sourced
  # from the small maps above, keyed by the same verdict token (see the
  # verdictColors comment for why description isn't also shared).
  researchVerdicts = map (v: {
    role = verdictRoles.${v.verdict};
    name = v.label;
    color = verdictColors.${v.verdict};
    description = verdictDescriptions.${v.verdict};
  }) verdicts.defaultVerdicts;

  priority = [
    {
      role = "PriorityCritical";
      name = "agent-priority-critical";
      color = "d73a4a";
      description = "Drop everything — highest dispatch priority";
    }
    {
      role = "PriorityHigh";
      name = "agent-priority-high";
      color = "ff8c00";
      description = "Dispatch ahead of normal-priority issues";
    }
    {
      role = "PriorityLow";
      name = "agent-priority-low";
      color = "8a9ba8";
      description = "Dispatch behind normal-priority issues";
    }
  ];

  # Same "needs a human answer" semantic family as agent-research-unclear,
  # but a distinct shade (e0cffc vs. d4c5f9) so the two never collide in the
  # GitHub label UI (TestTriageLabelMeta_ColorsAreDistinct) -- the choice of
  # exact hex isn't load-bearing beyond staying visually adjacent and
  # distinct.
  ambiguous = [
    {
      role = "Ambiguous";
      name = "agent-ambiguous-spec";
      color = "e0cffc";
      description = "An internally-contradictory issue; needs a human decision — not a crash";
    }
  ];

  # Recoverable: a local-only frontmatter marker, NEVER a real created
  # GitHub/Forgejo label -- forge.DispatchLabels.AllLabels()
  # (cmd/launcher/internal/forge/dispatch.go) deliberately excludes it, with
  # its own doc comment explaining why. Carried here for completeness of
  # "every label family" (issue #2528), but the Go renderer must NOT emit it
  # into TriageLabelMeta or any doctor-visible create surface: doctor never
  # creates it and no docs/reference.md `gh label create` snippet exists for
  # it.
  recoverable = [
    {
      role = "Recoverable";
      name = "agent-recoverable";
      color = "ededed";
      description = "Work is salvageable; run `spindrift recover` instead of a fresh dispatch";
    }
  ];

  # Review-finding provenance: the label the Filer applies to every issue it
  # files from a non-blocking review finding (issue #393, ADR 0041). Written
  # by the provenanceLabel argument cmd/launcher/internal/settle/gate.go's
  # work-path settle passes to fileIssueIntents (issue #2590 parameterized
  # that call; the Launcher's own non-agent-trusted literal for
  # SPINDRIFT_ISSUE_INTENT filing, never the payload's own labels) and
  # created directly by the Filer prompt
  # (templates/default/prompts/fragments/filer-label-direct.md /
  # filer-label-direct-forgejo.md) via a bare `gh label create`/REST call --
  # never through doctor.Run(), so (like `recoverable` above) it must NOT be
  # rendered into TriageLabelMeta: doctor never probes or offers to create
  # it, and folding it into that map would collide its color
  # (d4c5f9, matching what the prompt fragments already hardcode) with
  # agent-research-unclear's and trip
  # TestTriageLabelMeta_ColorsAreDistinct. Carried here so the registry
  # covers every label family the Harness writes (issue #2528 AC1), and so a
  # rename of this literal in either fragment or in gate.go without a
  # matching registry update is something a future check can catch.
  reviewFinding = [
    {
      role = "ReviewFinding";
      name = "agent-review-finding";
      color = "d4c5f9";
      description = "Filed from a non-blocking review finding";
    }
  ];

  # Trigger-only vocabulary: fires a workflow dispatch/recover run; written
  # by the workflows (self-clearing on claim), never created or colored by
  # doctor. nix/checks/dispatch-labels.nix's requiredLabels sources this list
  # directly instead of a locally hardcoded duplicate.
  triggerOnly = [
    "agent-trigger"
    "agent-recover"
  ];
}
