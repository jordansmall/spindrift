# The backend descriptor registry (issue #2521; ADR 0005/0007, "nix computes,
# generated code executes"): one row per ISSUE_TRACKER/CODE_FORGE backend.
# lib/renderers.nix's renderBackendRegistryGo writes it into
# cmd/launcher/internal/backend/registry_gen.go on `nix run .#regen`, and
# nix/checks/schema-drift.nix's backend-registry-gen check guards it.

# A plain list with no `{ lib }:` wrapper, because lib/env-schema.nix imports
# it with no `lib` in scope.

# Declaration order is load-bearing. lib/env-schema.nix derives
# issueTracker.choices and codeForge.choices as an order-preserving filter over
# this list, and this order reproduces both axes' pinned choice orders without
# a separate per-axis ordering table.

# An omitted bool means false. An omitted trackerAxisRead or trackerAxisWrite
# means "GITHUB"; an omitted trackerAxisFiler or forgeBackend means "GH"; an
# omitted doctorTokenHint or doctorSlugHint means doctor falls back to its
# github-shaped default.

# goVar is an explicit field rather than a derived title-case transform,
# because "github" must render as "GitHub" and no capitalize-first rule
# produces that.

# relayCapable covers every host-mediation seam a CODE_FORGE needs under
# BOX_FORGE_AND_ISSUE_ACCESS=read-only: bundle relay always, plus draft-PR
# create and commit subjects when the backend has a PR concept.
# outboxRelayCapable is narrower: it covers only the outbox mount and relay
# treatment (issues #1918, #2267, #2927).
[
  {
    name = "github";
    goVar = "GitHub";
    validAsTracker = true;
    validAsCodeForge = true;
    tokenEnvVar = "GH_TOKEN";
    outboxRelayCapable = true;
    relayCapable = true;
    hostPostingCapable = true;
  }
  {
    name = "git";
    goVar = "Git";
    validAsCodeForge = true;
  }
  {
    name = "local";
    goVar = "Local";
    validAsTracker = true;
    validAsCodeForge = true;
    # ADR 0033: local has no writable remote to push to.
    hostMediatedRemote = true;
    # ADR 0032: local is not reachable from inside the Box at all.
    inBoxUnreachableTracker = true;
    relayCapable = true;
    hostPostingCapable = true;
    trackerAxisRead = "LOCAL";
    # Explicitly empty, not omitted: local has no write step, so
    # gates_tracker.go's ISSUE_TRACKER_*_READWRITE/READONLY gates match neither
    # the "GITHUB" nor the "FORGEJO" arm.
    trackerAxisWrite = "";
  }
  {
    name = "jira";
    goVar = "Jira";
    validAsTracker = true;
    tokenEnvVar = "JIRA_TOKEN";
    doctorTokenHint = "JIRA_TOKEN";
    doctorSlugHint = "JIRA_BASE_URL / JIRA_PROJECT_KEY";
  }
  {
    name = "forgejo";
    goVar = "Forgejo";
    validAsTracker = true;
    validAsCodeForge = true;
    tokenEnvVar = "FORGEJO_TOKEN";
    doctorTokenHint = "FORGEJO_TOKEN";
    doctorSlugHint = "FORGEJO_BASE_URL";
    outboxRelayCapable = true;
    relayCapable = true;
    hostPostingCapable = true;
    trackerAxisRead = "FORGEJO";
    trackerAxisWrite = "FORGEJO";
    trackerAxisFiler = "FORGEJO";
    forgeBackend = "FORGEJO";
  }
]
