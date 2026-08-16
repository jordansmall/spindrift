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
# stays importable with zero arguments -- a follow-up slice imports it
# directly (no `lib` in scope) from lib/env-schema.nix.
#
# Declaration order here is deliberate and load-bearing: github, git, local,
# jira, forgejo. A follow-up slice derives env-schema.nix's
# issueTracker.choices and codeForge.choices as an order-preserving filter
# over this one list; this declaration order reproduces both axes' existing
# pinned choice orders -- [github, git, local, forgejo] for codeForge, and
# [github, local, jira, forgejo] for issueTracker -- via nothing but a single
# filter each (validAsCodeForge / validAsTracker), with no separate
# per-axis ordering table.
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
[
  {
    name = "github";
    goVar = "GitHub";
    validAsTracker = true;
    validAsCodeForge = true;
    tokenEnvVar = "GH_TOKEN";
    outboxRelayCapable = true;
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
  }
]
