# The single source of truth for spindrift's subcommands (issue #1575).
# lib/renderers.nix renders it into cmd/launcher/subcommands_gen.go and
# nix/checks/schema-drift.nix guards that against drift. Each name must
# match a verbHandlers key (cmd/launcher/main.go); the hidden
# __complete-issues verb is undocumented and absent from both tables.
[
  # List order is display order. Console comes first per ADR 0023.
  {
    name = "console";
    usage = "";
    doc = "browse the open backlog interactively (read-only)";
  }
  {
    name = "dispatch";
    usage = "[--no-build] [--yes] [--continuous] [issue...]";
    doc = "dispatch agents in waves; an issue list dispatches exactly those (bypasses label/barrier gates)";
    # Issue #556 scopes positional issue-number completion to dispatch,
    # preview, and recover. lib/renderers.nix derives the renderer and
    # schema-drift gate list from this field (issue #1603).
    dynamicIssueCompletion = true;
  }
  {
    # research shows an issue list in its usage but has no
    # dynamicIssueCompletion: it drains agent-research, a different queue
    # (issue #556).
    name = "research";
    usage = "[--no-build] [--yes] [--continuous] [issue...]";
    doc = "advise-only research dispatch: drains agent-research (or an issue list) and posts a verdict comment; never merges, never promotes";
  }
  {
    name = "preview";
    usage = "[issue...]";
    doc = "dry-run: show what dispatch would pick up, in order";
    dynamicIssueCompletion = true;
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
    dynamicIssueCompletion = true;
  }
  {
    name = "doctor";
    usage = "";
    doc = "check configuration validity, forge credentials, repository connectivity, and label presence; distinct exit code per failure class (see docs/reference.md)";
  }
  {
    name = "reconcile";
    usage = "";
    doc = "local-tracker bookkeeping sweep: close issues whose recorded landing PR merged (no-op on github/jira)";
  }
  {
    name = "registry";
    usage = "discover <repo-dir> <routes-file> [--force]";
    doc = "discover registry routes from a Target repo checkout and write the routes file (ADR 0045)";
  }
]
