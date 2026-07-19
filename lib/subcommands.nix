# The subcommand registry (issue #1575): the single source of truth for
# "what subcommands spindrift has" and their one-line summaries. Rendered
# into cmd/launcher/subcommands_gen.go by lib/renderers.nix's
# renderSubcommandsGo (nix/regen.nix writes it, nix/checks/schema-drift.nix
# guards it against drift), which printSubcommands (cmd/launcher/flags.go)
# consumes instead of a hand-written literal. A Go test
# (TestSubcommandRegistry_MatchesVerbHandlers, cmd/launcher/main_test.go)
# asserts these names are exactly the verbHandlers keys (cmd/launcher/main.go)
# — the hidden __complete-issues shell-completion verb is deliberately not a
# documented subcommand and so is absent from both tables.
#
# Order here is display order (console first, per ADR 0023 — bare
# `spindrift` now points operators at the interactive console).
#
# Fields:
#   name   string  verb name; must match a verbHandlers key exactly
#   usage  string  bracketed argument synopsis shown after name; "" when the
#                  verb takes none
#   doc    string  one-line summary rendered into --help and (eventually) the
#                  completions/man page
[
  {
    name = "console";
    usage = "";
    doc = "browse the open backlog interactively (read-only)";
  }
  {
    name = "dispatch";
    usage = "[--no-build] [--yes] [issue...]";
    doc = "dispatch agents in waves; an issue list dispatches exactly those (bypasses label/barrier gates)";
  }
  {
    name = "research";
    usage = "[--no-build] [--yes] [issue...]";
    doc = "advise-only research dispatch: drains agent-research (or an issue list) and posts a verdict comment; never merges, never promotes";
  }
  {
    name = "preview";
    usage = "[issue...]";
    doc = "dry-run: show what dispatch would pick up, in order";
  }
  {
    name = "build";
    usage = "";
    doc = "realize the agent image without running any agent";
  }
  {
    name = "recover";
    usage = "<issue>";
    doc = "run the merge gate for a single issue";
  }
  {
    name = "doctor";
    usage = "";
    doc = "check forge credentials, repository connectivity, and label presence (triage fatal, research advisory)";
  }
]
