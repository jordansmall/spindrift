# Knob env overrides are deprecated; nix hands the launcher one input document

Runtime environment variables stop being a configuration surface for knobs.
The end-state precedence is:

```
schema default  <  flake settings.<section>.<knob>  <  explicit CLI flag
```

Environment variables remain for exactly two things: **secrets** (schema
entries with `secret = true` — tokens never belong in the store or in argv)
and **internal plumbing** (launcher→Box delivery via `BOX_ENV_VARS` and the
Box entrypoint's baked defaults preamble, which are not operator-facing).

Every other nix-computed value reaches the launcher through a single
**Launcher input** document: a nix-rendered JSON file with a `settings`
section (resolved knob values, the Consumer flake's voice) and an `artifacts`
section (image archive, agent files, driver name, and the rest of what
`renderGoRunPreamble`/`renderGoBuildPreamble` export today). The generated
wrapper passes its store path via one flag:

```
exec launcher --input /nix/store/…-launcher-input.json "$@"
```

The Go struct mirroring the document is nix-generated from the schema, under
the same golden-diff guard as `flagtable_gen.go`, so the two sides cannot
drift.

## Motivation

The run preamble renders `VAR="${VAR:-<baked>}"` per knob: anything already
in the environment silently beats the flake settings. This is an operator
trap, hit repeatedly in practice — a value set in the Consumer flake was
overridden by a forgotten ambient variable (`harness.env` sourcing, shell
exports, CI env), and diagnosing it means bisecting three indistinguishable
voices that all arrive through one channel. With the document, each voice has
its own channel — file = flake, flags = operator, env = secrets — so the
launcher can tell them apart, and an ambient knob variable becomes
*detectable* rather than silently authoritative.

A second motivation is the seam itself: the nix→Go interface today is ~17
plumbing env vars enumerated three times (produced in `lib/preambles.nix`,
consumed as `os.Getenv` literals in `main.go`, and listed a third time as
`nixBakedEnvVars` purely so the schema coverage check can exclude them). The
document collapses that to one generated table; the exclusion list is
deleted, not maintained.

## Staging

- **Release N (transition):** the launcher warns, with provenance, on any
  knob env var found in its environment — `MAX_JOBS set in environment —
  knob env overrides are deprecated; use --max-jobs or
  settings.concurrency.maxJobs` — and still honors the value.
- **Release N+1:** the same condition is an error.

Secrets are exempt at both stages. The dev-iteration overrides
(`SPINDRIFT_PROMPT_DIR`, `SPINDRIFT_SKILLS_DIR`, ADR 0015) migrate to flags
on the same schedule — they are operator-facing knobs like any other.

## Considered options

- **Unconditional env from the preamble** (drop the `:-` fallback so baked
  values stomp ambient env) — a one-line renderer change, but it mirrors the
  confusion: ambient env silently *loses* instead of silently winning, and
  warning about it requires the wrapper to inspect env in generated bash
  before clobbering — more shell logic, against ADR 0005. Rejected.
- **Wrapper passes every knob as CLI flags** — visible in `ps`, but "a flag
  is explicit operator intent" stops being true when the wrapper generates
  forty of them, conflict resolution becomes last-flag-wins subtleties, and
  the plumbing values still need env or a file anyway. Rejected.
- **Keep env, add provenance reporting** (`doctor` shows each knob's source)
  — fixes the diagnosis pain but leaves the silent-clobber hazard in place.
  Rejected as the end state; the transition release's warning *is* this
  option, temporarily.

## Consequences

- Breaking change to the operator surface (ADR 0010): pre-1.0, a MINOR bump
  per stage; `MIGRATING.md` documents the flag/settings equivalents.
- `harness.env` shrinks to secrets only. `dogfood.sh` forwards its arguments
  to the launcher as flags; `KNOB=x ./dogfood.sh` stops being a supported
  override idiom.
- `renderDefaultsPreamble { export = true; }` (the launcher-side defaults
  preamble) and the `renderGoRunPreamble`/`renderGoBuildPreamble` env exports
  retire; the Box-side `entrypointDefaultsPreamble` is unchanged (Box env is
  plumbing, not an operator surface).
- `nixBakedEnvVars` in `lib/renderers.nix` and its coverage-check exclusion
  are deleted.
- The schema stays the single registry; `flakeOption`, `secret`, and `boxEnv`
  flags keep their meanings. A knob's runtime default still comes from the
  schema (via the generated defaults map) when neither document nor flag
  supplies it.
