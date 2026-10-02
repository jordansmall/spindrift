# Launcher-recognised CLI flags that are not schema knobs (those live in
# lib/env-schema.nix). One row per flag: `flag` (name without the leading --),
# `doc`, `verb` (the parser accepts the flag only after this verb; null =
# anywhere) and `arg` (placeholder when the flag takes a value; null = boolean).
#
# `nix run .#regen` renders this into cmd/launcher/cliflags_gen.go, which
# parseFlags consumes, and the shell-completion renderers consume it too, so a
# new non-schema flag added here reaches the parser and completions together.
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
    doc = "show the full doctor report, every check and gate row, not just failures (short form: -v)";
    verb = "doctor";
    arg = null;
  }
  {
    flag = "butler";
    doc = "make doctor also validate the butler config (exit 2 on failure)";
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
    flag = "help";
    doc = "show usage and exit";
    verb = null;
    arg = null;
  }
  {
    flag = "version";
    doc = "show version and exit";
    verb = null;
    arg = null;
  }
  {
    flag = "secret-cmd";
    doc = "templated fetch command for any secret with none of its own set; {name} substitutes the secret's kebab-case env name (sibling SECRET_CMD env var; lowest precedence)";
    verb = null;
    arg = "command";
  }
]
