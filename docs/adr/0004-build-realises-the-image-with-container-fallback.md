# `build` realises the image, using the container runtime as a fallback Linux builder

The `build` command doesn't just load the baked image archive — it realises it
first, by `nix build`-ing the image's `.drv` path (baked into the script with
its string context discarded, the same trick `run` uses for the output path).
When the host can't realise a Linux derivation — the common case on a stock
mac — `build` falls back to running the build inside an ephemeral Nix container
on the runtime it already requires: the machine that can *run* the Box can
always *build* it. Pure evaluation guarantees the in-container build produces
the exact store path the launcher has baked in.

## Considered Options

- **Hard-require a Linux builder** — honest and simple, but breaks the
  two-command quick start on macOS, the primary platform, until the user does
  remote-builder setup; rejected.
- **Always build via the container** — needlessly ignores a real Linux builder
  where one exists (including Linux hosts themselves); rejected.
- **`build` = load only** (the previous behaviour) — contradicted the README's
  "nix build + load" promise and left no exposed way to build the image from
  darwin at all (`packages.<system>.spindrift` is Linux-only); rejected.

## Consequences

`run` and `nix flake check` keep the zero-build, discarded-context behaviour;
only `build` pays for realisation. The container fallback assumes `build` is
invoked from the Consumer flake's directory (the same `$PWD` convention
`harness.env` already uses) and keeps a named volume for `/nix` so rebuilds are
incremental.
