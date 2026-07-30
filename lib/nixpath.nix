# Derive a flakeOption knob's full domain-tree path (its place in the flake
# surface's `perSystem.spindrift.*` tree) from the knob's own identity.
# ADR 0037 Pass 2 (issue #2188): the domain segment is the knob's `group`
# (re-cut to the six domains in #2187); the intra-domain remainder is the knob's
# own schema key, unless the knob overrides it with `nixSubPath` — the nesting or
# renamed leaf a flat `group` can't express (e.g. `label` -> `issues.labels.dispatch`,
# `mergeMode` -> `git.merge.policy`). Collapses the former Nix-only full `nixPath`
# parallel taxonomy (issue #2188).
name: entry: "${entry.group}.${entry.nixSubPath or name}"
