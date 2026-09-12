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

Always pass `-L` (`--print-build-logs`) and redirect to a file. Without it a
failure prints only the failing derivation's store path and a
`[build failed]` line, so the compile or test error costs a second turn
running `nix log`:

  nix build .#checks-inbox -L >"$TMPDIR/checks.log" 2>&1; echo "exit=$?"
  grep -nE 'error|FAIL' "$TMPDIR/checks.log" || tail -n 40 "$TMPDIR/checks.log"

That invocation is complete as written — pass no `-j`, `--max-jobs` or
`--cores` flags. The Box's baked `nix.conf` pins `cores = 4`, and Nix's
own default already builds one derivation at a time, so hand-tuning
either is guesswork.

A check failure is deterministic: the same derivation hash fails the same way
however it is scheduled. Never re-run a failed check unchanged — not under
reduced parallelism, not after a sleep. Each retry is a multi-minute build
with a foregone conclusion. The only remedies are to fix the code or read the
log `-L` already surfaced; re-run only after a real edit. A build the kernel
killed (`EXIT:137`, out of memory) is the exception: that is not a check
result at all, so a re-run is legitimate there, unlike a real failure. If
the kill happened under the full `nix flake check`, re-run the scoped
`checks-inbox` target instead — lowering parallelism is never the answer.

If `nix develop` is unavailable or fails, fall back to the baked toolchain and
log the fallback. Go module without a devShell:

- `test -z "$(gofmt -l .)"`
- `go vet ./...`
- `go test ./...`
