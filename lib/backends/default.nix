# The backend descriptor registry (issue #2521; ADR 0005/0007 "nix computes,
# generated code executes"): one row per ISSUE_TRACKER/CODE_FORGE backend --
# github, git, local, jira, forgejo -- carrying every axis-membership,
# token-knob, doctor-hint, and capability fact today hand-declared as Go
# struct literals in cmd/launcher/internal/backend/registry.go. Rendered into
# cmd/launcher/internal/backend/registry_gen.go by lib/renderers.nix's
# renderBackendRegistryGo, guarded against drift by
# nix/checks/schema-drift.nix's backend-registry-gen check, written by
# `nix run .#regen`.
#
# A plain list (no `{ lib }:` wrapper), mirroring lib/subcommands.nix, so it
# stays importable with zero arguments -- lib/env-schema.nix imports it
# directly (no `lib` in scope).
#
# Declaration order here is deliberate and load-bearing: github, git, local,
# jira, forgejo. lib/env-schema.nix derives issueTracker.choices and
# codeForge.choices as an order-preserving filter over this one list; this
# declaration order reproduces both axes' existing pinned choice orders --
# [github, git, local, forgejo] for codeForge, and [github, local, jira,
# forgejo] for issueTracker -- via nothing but a single filter each
# (validAsCodeForge / validAsTracker), with no separate per-axis ordering
# table.
#
# Fields:
#   name                     string  the ISSUE_TRACKER/CODE_FORGE knob value
#                                     this row registers, e.g. "github";
#                                     required on every row.
#   goVar                    string  the exact Go identifier the generated
#                                     file declares for this row's
#                                     Descriptor var (e.g. "GitHub" for
#                                     "github"). An explicit field rather
#                                     than a derived title-case transform,
#                                     since "github" -> "GitHub" isn't a
#                                     simple capitalize-first-letter rule
#                                     and a derived transform would be
#                                     fragile; required on every row.
#   validAsTracker           bool    this backend may be selected as
#                                     ISSUE_TRACKER; omitted (false) when it
#                                     may not.
#   validAsCodeForge         bool    this backend may be selected as
#                                     CODE_FORGE; omitted (false) when it may
#                                     not.
#   tokenEnvVar              string  this backend's bearer-token knob name;
#                                     omitted (empty) when the backend
#                                     carries no bearer token (git, local).
#   doctorTokenHint          string  the env var `doctor` points an operator
#                                     at when this backend is the active
#                                     ISSUE_TRACKER; omitted means "use the
#                                     github-shaped default".
#   doctorSlugHint           string  the env var `doctor` points an operator
#                                     at for this backend's repo/slug
#                                     config; omitted means "use the
#                                     github-shaped default".
#   hostMediatedRemote       bool    true only for a backend with no
#                                     writable remote to push to at all
#                                     (ADR 0033: "local"); omitted (false)
#                                     otherwise.
#   inBoxUnreachableTracker  bool    true only for a tracker with no in-box
#                                     reachability at all (ADR 0032:
#                                     "local"), gating the read-only /issues
#                                     mount; omitted (false) otherwise.
#   outboxRelayCapable       bool    true for a backend whose CODE_FORGE
#                                     selection gets the outbox mount/relay
#                                     treatment under read-only (issue
#                                     #1918: "github" only); omitted (false)
#                                     otherwise.
#   relayCapable             bool    true for a CODE_FORGE backend that,
#                                     under BOX_FORGE_AND_ISSUE_ACCESS=
#                                     read-only, has every real
#                                     host-mediation seam needed
#                                     (bundle-relay always; draft-PR-create +
#                                     commit-subjects too when the backend
#                                     has a PR concept) -- true for github,
#                                     forgejo, local; omitted (false) for
#                                     git. Distinct from outboxRelayCapable
#                                     (a narrower, github-only concern:
#                                     outbox mount treatment, #1918/#2267).
#   hostPostingCapable       bool    true for an ISSUE_TRACKER backend that,
#                                     under BOX_FORGE_AND_ISSUE_ACCESS=
#                                     read-only, can have its comments/
#                                     issue-filing host-mediated
#                                     (host-posted comments + issue-filing)
#                                     -- true for github, forgejo, local;
#                                     omitted (false) for jira.
#   trackerAxisRead          string  this tracker's ISSUE_TRACKER_GITHUB/
#                                     LOCAL/FORGEJO read-step axis value
#                                     ("GITHUB", "LOCAL", or "FORGEJO");
#                                     omitted means "GITHUB" (the default
#                                     arm, shared by github and jira).
#   trackerAxisWrite         string  this tracker's write-step axis value
#                                     ("GITHUB", "FORGEJO", or "" for a
#                                     tracker with no direct write-step path);
#                                     omitted means "GITHUB". "local" sets
#                                     this to the literal empty string
#                                     explicitly (not omitted) -- it
#                                     legitimately has no write axis, exactly
#                                     like local's write-step gates render
#                                     neither pair today (see
#                                     gates_tracker.go's
#                                     ISSUE_TRACKER_*_READWRITE/READONLY
#                                     gates, keyed off itWrite=="" never
#                                     matching either "GITHUB" or "FORGEJO"
#                                     arm -- itWrite is never "LOCAL"; only
#                                     trackerAxisRead ever takes that value).
#   trackerAxisFiler         string  this tracker's filer write-mechanism
#                                     axis value ("GH" or "FORGEJO"); omitted
#                                     means "GH".
#   forgeBackend             string  this code-forge's backend suffix ("GH"
#                                     or "FORGEJO"); omitted means "GH".
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
    hostMediatedRemote = true;
    inBoxUnreachableTracker = true;
    relayCapable = true;
    hostPostingCapable = true;
    trackerAxisRead = "LOCAL";
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
    relayCapable = true;
    hostPostingCapable = true;
    trackerAxisRead = "FORGEJO";
    trackerAxisWrite = "FORGEJO";
    trackerAxisFiler = "FORGEJO";
    forgeBackend = "FORGEJO";
  }
]
