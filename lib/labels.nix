# The label registry (issue #2528): every label family the Harness writes.
# lib/renderers.nix's renderLabelRegistryGo renders it into
# cmd/launcher/internal/doctor/labelmeta_gen.go via `nix run .#regen`, and
# nix/checks/schema-drift.nix's label-registry-gen check catches drift. A plain
# attrset with no `{ lib }:` wrapper, so it imports with zero arguments.
let
  schema = import ./env-schema.nix;
  verdicts = import ./research-verdicts.nix;

  # Colors keyed by verdict token, paired below against research-verdicts.nix's
  # defaultVerdicts so the label name is never retyped here (issue #2528). Only
  # the name is shared: that file's descriptions are prompt prose aimed at the
  # research agent, while a GitHub label description is a short summary for a
  # human reading the repo's Labels page.
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
  # Go role names keyed by verdict token. A fourth verdict token with no role
  # here throws below, same as an unknown backend field in
  # lib/backends/default.nix.
  verdictRoles = {
    recommend = "ResearchVerdictRecommend";
    reject = "ResearchVerdictReject";
    unclear = "ResearchVerdictUnclear";
  };
in
{
  # Work-tier names are operator-configurable, so the schema knobs flow through
  # and a renamed LABEL/IN_PROGRESS_LABEL/FAILED_LABEL/COMPLETE_LABEL still gets
  # correct metadata. role is the Go identifier suffix of the generated
  # Meta<Role> var, so doctor.go resolves a label's metadata by role even after
  # a rename.
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

  # name comes from research-verdicts.nix's defaultVerdicts, never retyped here;
  # color, description and role come from the maps above, keyed by the same
  # verdict token.
  researchVerdicts = map (v: {
    role = verdictRoles.${v.verdict};
    name = v.label;
    color = verdictColors.${v.verdict};
    description = verdictDescriptions.${v.verdict};
  }) verdicts.defaultVerdicts;

  # Fixed names (ADR 0040), never operator-configurable.
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

  # A distinct shade from agent-research-unclear's d4c5f9 so the two never
  # collide in the GitHub label UI (TestTriageLabelMeta_ColorsAreDistinct).
  ambiguous = [
    {
      role = "Ambiguous";
      name = "agent-ambiguous-spec";
      color = "e0cffc";
      description = "An internally-contradictory issue; needs a human decision — not a crash";
    }
  ];

  # Research-finding provenance (issue #2591, ADR 0041): the label a research
  # Box's Filer applies to every issue it files. Unlike reviewFinding, this one
  # does render into TriageLabelMeta, so doctor probes and offers to create it,
  # still advisory. Its color must stay distinct from reviewFinding's d4c5f9,
  # or TestTriageLabelMeta_ColorsAreDistinct trips.
  researchFinding = [
    {
      role = "ResearchFinding";
      name = "agent-research-finding";
      color = "c5def5";
      description = "Filed from a research finding";
    }
  ];

  # A local-only frontmatter marker, never a real created GitHub/Forgejo label:
  # forge.DispatchLabels.AllLabels() (cmd/launcher/internal/forge/dispatch.go)
  # excludes it, and the Go renderer must not emit it into TriageLabelMeta or
  # any doctor-visible create path. It is here only so the registry covers every
  # label family (issue #2528).
  recoverable = [
    {
      role = "Recoverable";
      name = "agent-recoverable";
      color = "ededed";
      description = "Work is salvageable; run `spindrift recover` instead of a fresh dispatch";
    }
  ];

  # Review-finding provenance (issue #393, ADR 0041): the Filer creates this
  # label from its prompt fragments, never through doctor.Run(), so the renderer
  # must not emit it into TriageLabelMeta, where its d4c5f9 would collide with
  # agent-research-unclear's and trip TestTriageLabelMeta_ColorsAreDistinct. It
  # is here only for registry coverage (issue #2528 AC1).
  reviewFinding = [
    {
      role = "ReviewFinding";
      name = "agent-review-finding";
      color = "d4c5f9";
      description = "Filed from a non-blocking review finding";
    }
  ];

  # The closed bug/enhancement/chore type vocabulary for a filed issue-intent's
  # `type` field (issue #2594, ADR 0041). settle/issue_intent.go's
  # ensureTypeLabel resolves a type at runtime, so this family needs its own Go
  # map, FindingTypeLabels: folded into TriageLabelMeta, a lookup of
  # "ready-for-agent" would resolve as a valid finding type (issue #1949).
  findingType = [
    {
      role = "FindingTypeBug";
      name = "bug";
      color = "ee0701";
      description = "Filed as a bug finding";
    }
    {
      role = "FindingTypeEnhancement";
      name = "enhancement";
      color = "a2eeef";
      description = "Filed as an enhancement finding";
    }
    {
      role = "FindingTypeChore";
      name = "chore";
      color = "fef2c0";
      description = "Filed as a chore finding";
    }
  ];

  # Fires a workflow dispatch/recover run: the workflows write it and clear it
  # on claim, and doctor never creates or colors it.
  # nix/checks/dispatch-labels.nix's requiredLabels sources this list rather
  # than duplicating it.
  triggerOnly = [
    "agent-trigger"
    "agent-recover"
  ];
}
