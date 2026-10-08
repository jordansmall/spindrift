# Launcher-recognised CLI flags that are not schema knobs (those live in
# lib/env-schema.nix). One row per flag: `flag` (name without the leading --),
# `doc`, `verb` (the parser accepts the flag only after this verb; null =
# anywhere) and `arg` (placeholder when the flag takes a value; null = boolean).
# Two optional fields, defaulted where read: `short` (single-letter short form
# without the dash, e.g. "h"; absent/null = none) and `intercepted` (bool,
# default false: mainRun acts on the flag before parseFlags runs, so parseFlags
# rejects it as unknown and the row only feeds completions, the man page, and
# `spindrift --help --all`).
#
# A short form is a `short` field on its long flag's row, never its own row
# and never only doc text.
#
# `nix run .#regen` renders this into cmd/launcher/cliflags_gen.go, which
# parseFlags consumes and `spindrift --help --all` renders, and the
# shell-completion renderers and the man page's COMMAND FLAGS section consume
# it too, so a new non-schema flag added here reaches the parser, terminal
# help, completions, and man page together.
#
# `--input` (the Launcher input document path, ADR 0020) is deliberately not a
# row: the Nix wrapper/daemon passes it (lib/mkHarness.nix), users never type it.
[
  {
    flag = "no-build";
    doc = "fail fast if the image is absent instead of building; pair with 'spindrift build' for split build/run flows";
    verb = null;
    arg = null;
  }
  {
    flag = "yes";
    doc = "skip confirmation prompt when dispatching unlabeled issues (alias: --force)";
    verb = null;
    arg = null;
  }
  {
    flag = "force";
    doc = "skip confirmation prompt when dispatching unlabeled issues (alias: --yes)";
    verb = null;
    arg = null;
  }
  {
    # The parser accepts it anywhere; main.go rejects it on non-research
    # verbs ("flag --self-contained is only valid for the research subcommand").
    flag = "self-contained";
    doc = "research only: review the issue from its body and comments alone, cloning no Target repo and needing no REPO_SLUG or GH_TOKEN";
    verb = null;
    arg = null;
  }
  {
    flag = "verbose";
    doc = "show the full doctor report, every check and gate row, not just failures";
    verb = "doctor";
    arg = null;
    short = "v";
  }
  {
    flag = "butler";
    doc = "make doctor also validate the butler config (exit 2 on failure) and require the butler labels (exit 4 if missing)";
    verb = "doctor";
    arg = null;
  }
  {
    flag = "research";
    doc = "make doctor also require the research labels (exit 4 if missing); the daemon preflight passes it when the research kind runs";
    verb = "doctor";
    arg = null;
  }
  {
    flag = "chore";
    doc = "sweep only the named butler Chore instead of the first due one";
    verb = "butler";
    arg = "name";
  }
  {
    flag = "json";
    doc = "emit one Dispatch Record per line as JSON instead of the summary table";
    verb = "stats";
    arg = null;
  }
  {
    flag = "reingest";
    doc = "re-parse every log still on disk, even unchanged ones, so a parser fix repairs the history that remains";
    verb = "stats";
    arg = null;
  }
  {
    flag = "root";
    doc = "read Records from this checkout's .spindrift store; repeat for several roots (default: the current directory)";
    verb = "stats";
    arg = "dir";
  }
  {
    flag = "since";
    doc = "keep only Records claimed at or after this time (RFC 3339 or YYYY-MM-DD, UTC)";
    verb = "stats";
    arg = "time";
  }
  {
    flag = "kind";
    doc = "keep only Records of this dispatch kind, or unknown for logs the backfill could not classify";
    verb = "stats";
    arg = "kind";
  }
  {
    flag = "include-inferred";
    doc = "include Records whose attribution is inferred from logs (default true; --include-inferred=false drops them)";
    verb = "stats";
    arg = null;
  }
  {
    flag = "help";
    doc = "show usage and exit";
    verb = null;
    arg = null;
    short = "h";
    intercepted = true;
  }
  {
    flag = "all";
    doc = "with --help, show the full reference: every subcommand, flag, and knob";
    verb = null;
    arg = null;
    intercepted = true;
  }
  {
    flag = "version";
    doc = "show version and exit";
    verb = null;
    arg = null;
    intercepted = true;
  }
  {
    flag = "secret-cmd";
    doc = "templated fetch command for any secret with none of its own set; {name} substitutes the secret's kebab-case env name (sibling SECRET_CMD env var; lowest precedence)";
    verb = null;
    arg = "command";
  }
]
