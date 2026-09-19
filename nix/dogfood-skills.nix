# The skills baked into the dogfood image, each under its own `<name>/SKILL.md`
# directory (lib/image.nix) so the in-box Claude Code registers it and offers it
# as `/<name>`. Rename a skill by changing `name` here, never by vendoring a
# copy of the upstream body into this repo.

# Declared as { name; src; } content (issue #597) rather than pre-built
# derivations: the image re-realizes each with its own Linux pkgs, so no
# consumer host `pkgs` may appear here. That host tag is what made the
# agent-image drvPath diverge across hosts.

# The upstreams are pinned via flake.lock, none floating to a branch head.

# Defined once so flake.nix's `spindrift` module config and fixtures.nix's
# direct mkHarness bake byte-identical skills (issue #459).
{
  caveman,
  matt-skills,
  jordan-skills,
}:
[
  {
    # Caveman compresses the agent's output tokens (issue #486).
    name = "caveman";
    src = builtins.readFile "${caveman}/skills/caveman/SKILL.md";
  }
  {
    name = "tdd";
    src = builtins.readFile "${matt-skills}/skills/engineering/tdd/SKILL.md";
  }
  {
    # Upstream SKILL.md references `/setup-matt-pocock-skills`, which this repo
    # does not bake. #787 kept it verbatim; #816 confirmed the dangling
    # reference caused no incident.
    name = "to-tickets";
    src = builtins.readFile "${matt-skills}/skills/engineering/to-tickets/SKILL.md";
  }
  {
    name = "commit";
    src = builtins.readFile "${jordan-skills}/commit/SKILL.md";
  }
  {
    # Upstream SKILL.md references `/setup-matt-pocock-skills` and
    # `docs/agents/issue-tracker.md`, neither baked here. #787 kept it
    # verbatim; #816 confirmed the same dangling reference in to-tickets
    # caused no incident.
    name = "code-review";
    src = builtins.readFile "${matt-skills}/skills/engineering/code-review/SKILL.md";
  }
  # Three engineering principles, adapted in `jordan-skills` from the MIT
  # `pstack` plugin. The adaptation drops upstream's
  # `disable-model-invocation: true`, which restricts a skill to human `/name`
  # invocation and keeps it out of subagents -- inert in a Box, which has no
  # human and reviews in a subagent.
  {
    name = "principle-fix-root-causes";
    src = builtins.readFile "${jordan-skills}/principle-fix-root-causes/SKILL.md";
  }
  {
    name = "principle-laziness-protocol";
    src = builtins.readFile "${jordan-skills}/principle-laziness-protocol/SKILL.md";
  }
  {
    name = "principle-redesign-from-first-principles";
    src = builtins.readFile "${jordan-skills}/principle-redesign-from-first-principles/SKILL.md";
  }
  {
    # Authored in this repo (skills/nix-checks), not a pinned upstream flake
    # input like the rows above, because there is no upstream yet. Issue #3223:
    # a later extraction to its own flake input swaps only this `src`.
    name = "nix-checks";
    src = builtins.readFile ../skills/nix-checks/SKILL.md;
  }
]
