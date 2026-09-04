---
name: nix-checks
description: Run Nix checks the way a Nix flake repo expects — git-add new files before the first build, prefer the devShell toolchain, and use a scoped check target over a full flake check.
---
Nix flakes only evaluate git-tracked files — `git add` any new file (e.g.
`git add -A`) before the first `nix build`/`nix flake check` that touches it,
or the build aborts with "is not tracked by Git" and burns a checks cycle.

If the repo has a `flake.nix` devShell, prefer its pinned toolchain:

  nix develop -c <check-command>   # run any check inside the devShell

Use a scoped check target (e.g. `checks-inbox`) if the flake exposes one, and
do not run a full `nix flake check` in-box unless the diff changes what gets
baked into the box's own image — concretely, unless it touches
`nix/checks/image.nix` or `lib/image.nix`, the definitions that build and
inspect that image and are heavy and unreliable to re-run from inside the box
itself. This is a firm rule, and it **overrides** any acceptance criteria in
the issue that ask for `nix flake check` more loosely. Fall back to `nix
flake check` only if no scoped target exists.

If `nix develop` is unavailable or fails, fall back to the baked toolchain and
log the fallback. Go module without a devShell:

- `test -z "$(gofmt -l .)"`
- `go vet ./...`
- `go test ./...`
