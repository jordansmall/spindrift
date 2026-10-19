# The backend descriptor registry (issue #2521; ADR 0005/0007, "nix computes,
# generated code executes"): one row per ISSUE_TRACKER/CODE_FORGE backend,
# each in its own sibling file and listed in order below.
# lib/renderers.nix's renderBackendRegistryGo writes it into
# cmd/launcher/internal/backend/registry_gen.go on `nix run .#regen`, and
# nix/checks/schema-drift.nix's backend-registry-gen check guards it.

# Adding a field is one entry in renderBackendRegistryGo's rowFields table plus
# the matching field on the hand-written Descriptor struct in
# cmd/launcher/internal/backend/registry.go; TestDescriptorFieldsMatchNixRows
# fails if the two disagree. Document an optional field's default in the
# "An omitted ..." paragraph below, then run `nix run .#regen`.

# A plain list with no `{ lib }:` wrapper, because lib/env-schema.nix imports
# it with no `lib` in scope.

# Declaration order is load-bearing. lib/env-schema.nix derives
# issueTracker.choices and codeForge.choices as an order-preserving filter over
# this list, and this order reproduces both axes' pinned choice orders without
# a separate per-axis ordering table. Keep it an explicit list: builtins.readDir
# would sort the files by name and silently reorder both axes.

# An omitted bool means false. An omitted trackerAxisRead means "GITHUB"; an
# omitted trackerAxisWrite means "GITHUB" too (only reachable when
# trackerAxisRead is omitted as well); an omitted trackerAxisFiler or
# forgeBackend means "GH"; an omitted doctorTokenHint or doctorSlugHint means
# doctor falls back to its github-shaped default. Omission
# is the only way to ask for a default: renderBackendRegistryGo throws on an
# explicit "" for tokenEnvVar or any of those defaulted string fields except
# trackerAxisWrite, because the generated Go cannot tell "" from omitted, and
# lib/mkHarness.nix defaults on absence while cmd/launcher defaults on
# emptiness (issue #3487). trackerAxisWrite = "" is legal (see local) because
# cmd/launcher reads it as-is. trackerAxisRead and trackerAxisWrite must be set
# together or neither, and a row that sets trackerAxisFiler must set
# trackerAxisRead, else renderBackendRegistryGo throws: cmd/launcher's
# backend.TrackerAxisSignals drops write and filer for a row that omits read
# (issue #4183), and reads an omitted write as "" where mkHarness.nix says
# "GITHUB" (issue #2673).

# displayName (required) is the backend's human-facing name, the value of the
# prompt's ${FORGE_DISPLAY_NAME} substitution (issue #5647, ADR 0068): a
# fragment shared by several backends names the active one through it instead
# of forking a copy per backend. It is not title-cased by rule: local is "local".

# goVar is an explicit field rather than a derived title-case transform,
# because "github" must render as "GitHub" and no capitalize-first rule
# produces that.

# boxTokenEnvVar is the ADR 0016 Box-side override of tokenEnvVar: the
# operator's value under this name reaches the Box under tokenEnvVar's name,
# while the launcher keeps its own token for merges and labels. Optional;
# omitted means no override (jira, local, git). Forgejo's row derives the
# field from its boxForgejoToken knob so the two cannot drift.

# relayCapable covers every host-mediation seam a CODE_FORGE needs under
# BOX_FORGE_AND_ISSUE_ACCESS=read-only: bundle relay always, plus draft-PR
# create and commit subjects when the backend has a PR concept. Nix cannot
# see the adapters, so cmd/launcher's
# TestRelayCapableMatchesReadOnlyForgeInterfaces checks each row's bit against
# the interfaces its read-only forge implements (issue #2628).
# outboxRelayCapable is narrower: it covers the outbox mount and relay
# treatment (issues #1918, #2267, #2927), and also stands for "this forge has
# pull requests" in the prompt's IF BLOCKED PR gates, read-write Boxes included
# (issue #5276). The same Go test enforces it against DraftPRCreator.

# inBoxGHCredentialHelper marks a CODE_FORGE whose Box clone runs
# `gh auth setup-git`; github and git (its remote URL may be github.com), not
# forgejo (token URL, ADR 0038) or local (filesystem mount, ADR 0033; issue #4452).

# A row may also carry `knobs`: an attrset keyed by env-schema key, each value
# an env-schema entry. lib/row-knobs.nix folds them into lib/env-schema.nix, so
# the row owns its backend-specific knobs; the generated Descriptor ignores the
# field, and an env name or key a row shares with another declaration fails
# eval (ADR 0068).

# quickstart (optional) opts the backend into `spindrift quickstart`'s
# remote-host detection and wizard (ADR 0068 decision 8): `hosts` lists the
# git remote hosts that identify it, `axes` the axes it sets, a non-empty
# subset of "tracker" and "forge" that must include "tracker" (quickstart's
# answer is the issue tracker). "tracker" requires validAsTracker and "forge"
# requires validAsCodeForge; renderBackendRegistryGo throws otherwise. Quickstart
# renders the values the Strategy derives from the remote for the flakeOption
# knobs in the row's `knobs`. An omitted block
# means hand-edit only: quickstart never offers the backend.

# cliPackage is the nixpkgs attribute name of a CLI only this backend needs
# inside the Box (forgejo: forgejo-cli, issue #1963). lib/mkHarness.nix unions
# it across the selected tracker and forge rows and lib/image.nix bakes the
# result; omit it for a backend that needs none. gh is not declared here: it
# is Harness plumbing, baked unconditionally. Like `knobs`, the generated
# Descriptor ignores it.

# validateKnobs (optional) is a function from the merged defaults to true,
# throwing on an invalid value of a knob the row owns; lib/mkHarness.nix runs
# it for the selected tracker and forge rows only, so a knob stays dead config
# when its backend is not selected. Like `knobs`, the generated Descriptor
# ignores it.

# goTestRepoFiles (optional) lists repo-root-relative files the backend's own go
# tests read beside cmd/launcher; nix/checks/go.nix copies them into that
# backend's test source. goTestBuildsOn (optional) names the backend whose
# package this one uses as a library, so its go test source may hold that
# sibling's directory (issue #5637). Like `knobs`, the generated Descriptor
# ignores both.

# labelRegistry marks a tracker whose labels are registered objects that
# ListLabels/CreateLabel manage; local and jira omit it (issue #4400).
[
  (import ./github.nix)
  (import ./git.nix)
  (import ./local.nix)
  (import ./jira.nix)
  (import ./forgejo.nix)
]
