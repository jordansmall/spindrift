# AUTO-LINT

Before committing, lint the files you changed and resolve what you find:

1. Detect the project's linter, in order of preference:
   - A `lint` target in the project's build config (`package.json` script,
     `Makefile`, `justfile`), or a checker the flake/devShell exposes.
   - The standard linter for the language (e.g. `eslint`, `ruff`/`flake8`,
     `golangci-lint`/`go vet`, `clippy`, `statix`).
2. Run it only on the files you changed (from `git diff --name-only` vs the
   base branch), where the linter accepts explicit paths.
3. Apply the linter's safe auto-fix mode where available, then manually
   resolve the remaining findings in the changed files before committing.
4. Skip silently when no linter is found — this must never fail the run.

