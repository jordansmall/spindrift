# AUTO-FORMAT

Before committing, auto-format the files you changed:

1. Detect the project's formatter, in order of preference:
   - A `format` or `fmt` script/target in `package.json`, `Makefile`, or `justfile`.
   - The standard formatter for the language (e.g. `gofmt -w`, `cargo fmt`, `black`).
   - Never `nix fmt`, even if the flake defines a formatter: evaluating the
     flake copies the dirty work tree into `/nix/store`, which the in-box
     agent user cannot write to, so the command always dies with a store-lock
     permission error.
2. Run it only on the files you changed (from `git diff --name-only` vs the
   base branch), where the formatter accepts explicit paths. Fall back to a
   project-wide run when the formatter does not support per-file invocation.
3. Skip silently when no formatter is found — this must never fail the run.

