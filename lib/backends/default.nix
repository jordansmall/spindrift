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

# Field semantics are documented on the Descriptor struct in
# cmd/launcher/internal/backend/registry.go; what follows is the Nix side.

# An omitted bool means false. An omitted trackerAxisRead or trackerAxisWrite
# means "GITHUB"; an omitted trackerAxisFiler or forgeBackend means "GH"; an
# omitted doctorTokenHint or doctorSlugHint means doctor's github-shaped
# default. Omission is the only way to ask for a default:
# renderBackendRegistryGo throws on an explicit "" for tokenEnvVar or any of
# those defaulted string fields except trackerAxisWrite, because the generated
# Go cannot tell "" from omitted, and lib/mkHarness.nix defaults on absence
# while cmd/launcher defaults on emptiness (issue #3487). trackerAxisWrite = ""
# is legal (see local) because cmd/launcher reads it as-is. trackerAxisRead
# and trackerAxisWrite must be set together or neither, and trackerAxisFiler
# requires trackerAxisRead, else renderBackendRegistryGo throws:
# backend.TrackerAxisSignals drops write and filer for a row that omits read
# (issue #4183), and reads an omitted write as "" where mkHarness.nix says
# "GITHUB" (issue #2673).

# displayName (required) is the prompt's ${FORGE_DISPLAY_NAME} (issue #5647),
# so a fragment shared by several backends need not fork per backend. It is not
# title-cased by rule: local is "local". goVar is explicit too, since no
# capitalize-first rule renders "github" as "GitHub".

# Nix cannot see the adapters, so cmd/launcher's
# TestRelayCapableMatchesForgeInterfaces checks relayCapable and
# outboxRelayCapable against the interfaces each row's forge implements
# (issue #2628). git is relayCapable without outboxRelayCapable.

# A row may also carry `knobs`: an attrset keyed by env-schema key, each value
# an env-schema entry. lib/row-knobs.nix folds them into lib/env-schema.nix, so
# the row owns its backend-specific knobs; the generated Descriptor ignores the
# field, and an env name or key a row shares with another declaration fails
# eval (ADR 0068).

# quickstart (optional) opts the backend into Quickstart's remote-host
# detection and wizard (ADR 0068 decision 8): `hosts` lists the git remote
# hosts that identify it, `axes` a non-empty subset of "tracker" and "forge"
# that must include "tracker" (Quickstart's answer is the issue tracker).
# "tracker" requires validAsTracker and "forge" validAsCodeForge, else
# renderBackendRegistryGo throws. Quickstart renders the values the Strategy
# derives from the remote for the flakeOption knobs in the row's `knobs`. An
# omitted block means hand-edit only.

# validateKnobs (optional) is a function from the merged defaults to true,
# throwing on an invalid value of a knob the row owns; lib/mkHarness.nix runs
# it for the selected tracker and forge rows only, so a knob stays dead config
# when its backend is not selected. Like `knobs`, the generated Descriptor
# ignores it.

# goTestRepoFiles (optional) lists repo-root-relative files the backend's own go
# tests read beside cmd/launcher; nix/checks/go.nix copies them into that
# backend's test source. goTestBuildsOn (optional) names the backend whose
# package this one uses as a library, so its go test source may hold that
# sibling's directory (issue #5637). core (optional bool) marks a backend the
# core build keeps (ADR 0068, decision 7). A core row gets no per-row go test
# derivation (launcher-go-test runs it, issue #7318), so it must not set
# goTestBuildsOn. Like `knobs`, the generated Descriptor ignores all three.

# trackerScopeKnob (optional string) names the schema key whose value is the
# Tracker scope (ADR 0075 decision 3), as the Jira row names jiraProjectKey. An
# omitted field scopes by the repo slug. lib/mkHarness.nix reads it, and the
# generated Descriptor ignores it.

# bareRefClosesIssues (optional bool) marks a tracker that is the repo's own
# issue list, so a bare `Closes #N` in a PR closes its issue #N. mkHarness
# then requires the Tracker scope to equal REPO_SLUG (ADR 0075 decision 3).
# The generated Descriptor ignores it.

# routeKinds (optional list of strings) names the Issue route kinds the tracker
# translates (ADR 0075 decision 1); each declared kind has both a filter op and
# a stamp op. An omitted field means none, and mkHarness rejects a route kind
# the tracker has not declared: a tracker that ignored the route would quietly
# list the whole Tracker scope. The generated Descriptor carries it as
# RouteKinds.

# connectorOnly (optional bool) marks a backend whose only implementation is
# its Connector: there is no native adapter for its Connector's `primary` to
# replace, so lib/connector-names.nix leaves it out of nativeBackendNames
# (jira, issue #6858; github, issue #6888; forgejo, issue #7122). The
# generated Descriptor carries it too: the Bundle's no-native-network-adapter
# check reads it (issue #6888).

# consumerStanza (required) declares what a Consumer of the backend
# configures (issue #7415): `connectorKnobs`, the names of its Connector's
# credential knobs (connectors/<name>/module.nix), each a secret stanza beside
# the row's own tokenEnvVar; `flakeInput`, the flake-input stanza; and
# `moduleImport`, the module import. A null flakeInput or moduleImport means
# the batteries flake (`flakeModules.default`) already carries the backend. The
# settings-block knob lines are the row's own `knobs` with flakeOption = true.
# nix/consumer-stanzas.nix composes them and fails eval when the Connector
# declares a secret knob `connectorKnobs` omits. The generated Descriptor
# ignores the field.

# labelRegistry marks a tracker whose labels are registered objects that
# ListLabels/CreateLabel manage; local and jira omit it (issue #4400).
# issuesWearLabels marks a tracker whose issue labels are reliable
# provenance, registry or not: jira sets it though it omits labelRegistry
# (issue #6347). local omits it: a body marker can stand in for its labels.
[
  (import ./github.nix)
  (import ./git.nix)
  (import ./local.nix)
  (import ./jira.nix)
  (import ./forgejo.nix)
]
