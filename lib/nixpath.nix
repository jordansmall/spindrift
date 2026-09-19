# Derive a flakeOption knob's path under `perSystem.spindrift.*` from its own
# identity (ADR 0037 Pass 2, issue #2188): the domain segment is the knob's
# `group` (re-cut to six domains in #2187) and the remainder its schema key,
# unless `nixSubPath` overrides it for a nested or renamed leaf that a flat
# `group` cannot express, such as `label` becoming `issues.labels.dispatch`.
name: entry: "${entry.group}.${entry.nixSubPath or name}"
