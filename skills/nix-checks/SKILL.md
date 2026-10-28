---
name: nix-checks
description: Run Nix checks the way a Nix flake repo expects — git-add new files before the first build, prefer the devShell toolchain, and use the scoped gate over a full flake check.
---
Nix flakes only evaluate git-tracked files — `git add` any new file (e.g.
`git add -A`) before the first `nix build`/`nix flake check` that touches it,
or the build aborts with "is not tracked by Git" and burns a checks cycle.

If the repo has a `flake.nix` devShell, prefer its pinned toolchain:

  nix develop -c <check-command>   # run any check inside the devShell

Use the scoped check target, the gate (`nix run .#gate`), if the flake exposes
one, and do not run a full `nix flake check` in-box unless the diff changes
what gets baked into the box's own image — concretely, unless it touches
`nix/checks/image.nix` or `lib/image.nix`, the definitions that build and
inspect that image and are heavy and unreliable to re-run from inside the box
itself. This is a firm rule, and it **overrides** any acceptance criteria in
the issue that ask for `nix flake check` more loosely. Fall back to `nix
flake check` only if no scoped target exists.

That scoped gate also skips `docs-edit-isolation`, which proves a docs-only
edit leaves the non-docs checks alone by re-evaluating the whole checkset
twice — a large share of a cold inbox evaluation — and which guards
CI's docs-lane membership, something a Box rarely changes; CI's full `nix flake
check` still runs it.

Run the gate and redirect to a file:

  nix run .#gate >"$TMPDIR/checks.log" 2>&1; echo "exit=$?"
  grep -nE '^gate:|error|FAIL' "$TMPDIR/checks.log" || tail -n 40 "$TMPDIR/checks.log"

The gate evaluates the same set `nix build .#checks-inbox` builds, but in
parallel `nix-eval-jobs` workers (their count is clamped to the Box's memory
limit), and builds each check as it evaluates. The `gate:` lines name each
failing attr (or `nix-eval-jobs` / `nix build (batch)` when a failure cannot be
pinned to one) and end in a one-line summary, and the exit code is non-zero on
any failure. `checks-inbox` stays for CI and humans; its evaluation is
single-threaded, so a Box should use the gate.

The gate builds with `-L` (`--print-build-logs`) already. Keep `-L` on every
single-attr `nix build`: without it a failure prints only the failing
derivation's store path and a `[build failed]` line, so the compile or test
error costs a second turn running `nix log`.

That invocation is complete as written — pass the gate no `-j`, `--max-jobs`
or `--cores` flags. The Box's baked `nix.conf` pins `cores = 4` and
`max-jobs = 2` for its builds and the gate sizes its own eval workers, so
hand-tuning either is guesswork. The one exception, an `EXIT:137` kill, is
below.

Checks run in two tiers. While iterating, run the narrowest check that
covers the change — `go test` on one package, one bats shard, or a single
check attr with `nix build .#checks.<system>.<name> -L` (`<system>` is
e.g. `x86_64-linux`). List the attrs with `nix eval .#checks.<system>
--apply builtins.attrNames`. Run the full gate (`nix run .#gate`) once per
pass, just before the pass's final commit. After a rebase, re-run it only
if the rebase hit conflicts or brought in changes to files the branch
touches; otherwise CI's full `nix flake check` is the final gate. A Box's
worker subagents run targeted checks only — its coordinator agent owns the
full gate.

A Driver's Nix checks live in `nix/checks/drivers/<name>.nix`, named after its
row in `lib/drivers/default.nix`; `nix/checks/drivers.nix` unions the files
and runs the parity checks over every row. Its Go tests are the exception:
`nix/checks/go.nix` generates `drivers-<name>-go-test` from the same row.
Build one check with `nix build .#checks.<system>.<attr> -L`.

A check failure is deterministic: the same derivation hash fails the same way
however it is scheduled. Never re-run a failed check unchanged — not under
reduced parallelism (the `EXIT:137` carve-out below is outside this rule), not
after a sleep. Each retry is a multi-minute build with a foregone conclusion.
The only remedies are to fix the code or read the log `-L` already surfaced;
re-run only after a real edit. A build the kernel killed (`EXIT:137`, out of
memory) is the exception: that is not a check result at all, so a re-run is
legitimate there, unlike a real failure. Under the full `nix flake check`,
re-run the scoped gate instead — a smaller target, not a smaller `--cores`.
Under the gate itself there is no smaller target left, so fall back to the
single-threaded aggregate,
`nix build .#checks-inbox -L --max-jobs 1 --cores 1`, the one sanctioned
exception to the no-resource-flags rule above: it drops evaluation to one
thread, the concurrent derivations to one, and the concurrent compiles or test
binaries inside it to one as well. Reach for it once, only after an
`EXIT:137`, and never for a check that genuinely failed.

If `nix develop` is unavailable or fails, fall back to the baked toolchain and
log the fallback. Go module without a devShell:

- `test -z "$(gofmt -l .)"`
- `go vet ./...`
- `go test ./...`
