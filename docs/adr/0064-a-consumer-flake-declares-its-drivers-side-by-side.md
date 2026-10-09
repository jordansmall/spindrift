# A Consumer flake declares its Drivers side by side

Status: proposed

## Context

ADR 0009 made the agent CLI a build-time Driver seam: one Driver per image,
selected by the `driver` flake option. It deferred running more than one
Driver ("per-issue routing ... is a later launcher feature, not a
repackaging"), and issue #3490 recorded that the per-Driver
`agent-image-<driver>` output family it anticipated never materialized: there
is one `agent-image` (or `agent-closure` under bwrap), with the Driver riding
only in the OCI image name.

In practice a Consumer flake gets exactly one Harness, so exactly one Driver.
Trying a second one today means:

- **Hand-building a second Harness.** The flake module wraps one `mkHarness`
  call. The dogfood's bwrap twin shows the workaround: a direct `harness`
  call over a copy of the module's arguments, exported under its own app
  names.
- **Rewriting every model setting.** The top-level model, effort and roster
  are written in one Driver's vocabulary: bare Anthropic ids for `claude`,
  `provider/model` ids for `opencode`. A Driver switch rewrites all of them.
- **Fighting fixed output names.** The freshness probe compares against a
  fixed `agent-image`/`agent-closure` attribute, so a second Harness's image
  would be checked against the wrong build.
- **Sharing one `harness.env`.** Runtime env overrides baked defaults, and
  `harness.env` is read by every app. A `MODEL` written for `claude` would
  reach any other Driver's app.
- **A latent mismatch.** The launcher reads `DRIVER` from its baked input
  document, but an ambient `DRIVER` env var wins, so exporting one changes
  host-side classification, parsing and doctor checks while the image still
  runs the baked Driver's CLI.

spindrift's own dogfood has the same limits, so it cannot compare Drivers on
its own work, and the `pi` Driver (ADR 0009 amendment, issue #4945) would have
no easy way to run real Dispatches.

## Decision

A Consumer flake declares **several Drivers side by side, keyed by name**,
and gets one Harness per declared Driver (issue #4943).

1. **The Driver is the key.** A new `agents.drivers.<name>` map replaces the
   single `agents.driver` value. There is no Driver field inside an entry, so
   the Driver is named in exactly one place.
2. **Only Driver-valued settings nest under a Driver.** A setting goes under
   `agents.drivers.<name>` only if its valid values depend on that Driver:
   the top-level model and effort, the roster (each role's model and effort),
   the review, filer and worker model and effort overrides, and Driver-only
   knobs such as the Bash timeout. Everything else (prompt, skills, runtime,
   image packages, Nix settings, Daemon, merge and forge settings) stays
   shared at the top level, set once.
3. **One default Driver.** `agents.defaultDriver` names the Driver that
   `nix run .` and `nix run .#daemon` use. It is inferred when exactly one
   Driver is declared, required otherwise, and must name a declared Driver.
4. **One Harness per declared Driver.** The module builds each from the
   shared settings merged with that Driver's entry. Every declared Driver
   gets `<driver>` and `<driver>-daemon` apps and Driver-suffixed image and
   closure attributes (`agent-image-<driver>`, `agent-closure-<driver>`). The
   default Driver also keeps today's names: the default app, `daemon`, the
   unsuffixed image and closure attributes, and its current OCI image name.
5. **Each Harness points at its own image and app.** The baked image
   attribute the freshness probe compares against is the running Driver's
   own. Each Driver's Daemon app bakes its own app reference as the child
   Dispatch target and self reference, so one Daemon runs one Driver.
6. **Runtime env layers per Driver.** Each Driver's app reads `harness.env`,
   then `harness.<driver>.env` if present, with the Driver file winning.
   Precedence against exported env and dispatch-time flags is unchanged.
   With more than one Driver declared, a Driver-valued knob in the shared
   `harness.env` produces a warning.
7. **The baked Driver wins.** An ambient `DRIVER` that differs from the
   app's baked Driver produces a warning and is ignored, becoming an error
   once the deprecation window below closes.
8. **`spindrift stats --by driver`.** Dispatch Records already store the
   Driver (ADR 0061). `stats` gains a `driver` grouping key, composable with
   the existing keys.

**Backward compatibility.** Today's `agents.driver` and top-level model,
effort and roster settings stay valid as shorthand for one Driver:
`agents.driver = d` plus those settings is translated to
`agents.drivers.d = { ... }` with `defaultDriver = d`, and must build an
identical Harness. The shorthand emits a `lib.warn` deprecation message,
the same mechanism as the `settings.<section>` shim, and MIGRATING.md maps
each old option to its new place. Removal is a later minor release, not part
of this decision. Two cases are evaluation errors: setting both the shorthand
and the Driver map, and, with more than one Driver declared, setting a
Driver-valued knob at the top level. Under ADR 0010 the new apps are an
additive change to the Consumer surface. A single-Driver Consumer sees no new
behaviour beyond the deprecation warning.

The dogfood adopts the Driver-map form first, declaring `claude` (default,
its current models and roster unchanged) and `opencode`, so switching is
proven before a third Driver exists. CI dispatch keeps running the default
Driver; switching CI is editing `defaultDriver` and its secrets.

## Considered Options

- **A base config plus `variants.<name>` overrides.** Each variant would
  override any part of the top-level config, including the Driver. Rejected:
  the Driver would be set at the top level and again per variant, so reading
  a variant means working out which value wins.
- **Any setting overridable per Driver.** Rejected for the same reason:
  prompt, skills or closures set in two places reintroduce precedence
  questions. Only settings whose values depend on the Driver vary.
- **Dogfood-only wiring.** A private profile table in spindrift's own repo,
  like the bwrap twin. Rejected: the dogfood is meant to run as an ordinary
  Consumer, and this would give it machinery no Consumer has.
- **A single runtime-switchable image.** ADR 0009 already rejected the fat
  image carrying every Driver; nothing here changes that.
- **Driver-prefixed knob names** (`PI_MODEL`, `CLAUDE_MODEL`, ...). Rejected:
  it multiplies the env schema per Driver. A per-Driver env file reuses the
  existing names.
- **No runtime model override with several Drivers.** Rejected: it loses the
  zero-rebuild model switch ADR 0009 keeps `MODEL` a runtime knob for.
- **A Daemon that spreads its slots across Drivers.** Deferred, not rejected:
  useful for comparing Drivers on live work, but it adds a scheduling
  dimension. One Driver per Daemon plus `stats --by driver` covers
  comparison across periods first.
- **Per-label Driver routing in CI.** Deferred: CI runs the default Driver,
  and a Consumer can wire its own workflows as it likes.

## Consequences

- ADR 0009's "one Driver per image" stands. It now means several images per
  Consumer flake, and the `agent-image-<driver>` family #3490 found missing
  returns alongside the unsuffixed default outputs.
- The flake options reference, `docs/reference.md` and MIGRATING.md document
  the Driver map, the default, the env layering and the deprecation.
- Building every declared Driver costs a closure per Driver; a Consumer
  declares only the Drivers it runs.
- The structural-option and schema-drift checks gain the Driver map, and the
  equivalence check pins shorthand and Driver-map forms to an identical
  Harness.
- An ambient `DRIVER` stops silently changing host-side behaviour; after the
  deprecation window it is rejected outright.
- Deferred, each a later decision: removing the shorthand options, a
  mixed-Driver Daemon, and per-label Driver routing.
