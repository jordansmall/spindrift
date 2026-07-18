# The skills baked into the dogfood image, each under its own `<name>/SKILL.md`
# directory (lib/image.nix) so the in-box Claude Code registers it and the
# skill preamble advertises it as `/<name>`. Declared as { name; src; } content
# (issue #597) rather than pre-built `pkgs.writeText` derivations: the image
# re-realizes each with its own Linux pkgs (lib/image.nix), so no consumer host
# `pkgs` may appear here — that host tag is what made the agent-image drvPath
# diverge across hosts. `name` is the skill (directory) name; `src` is the
# SKILL.md body, read straight from the pinned upstream tree — renaming happens
# here at the `name` field, not by vendoring a copy of the content into this
# repo.
#
# The upstreams are pinned via flake.lock (flake.nix inputs), none floating to
# a branch head:
#   - caveman (juliusbrussee/caveman, issue #486): output-token compression.
#   - matt-skills (mattpocock/skills, tag v1.1.0): tdd + to-tickets + code-review.
#   - jordan-skills (jordansmall/skills): commit.
#
# Defined once so flake.nix's `spindrift` module config and fixtures.nix's
# direct mkHarness mirror bake byte-identical skills (the same single-source
# pattern as nix/dogfood-defaults.nix, issue #459).
{
  caveman,
  matt-skills,
  jordan-skills,
}:
[
  {
    name = "caveman";
    src = builtins.readFile "${caveman}/skills/caveman/SKILL.md";
  }
  {
    name = "tdd";
    src = builtins.readFile "${matt-skills}/skills/engineering/tdd/SKILL.md";
  }
  {
    # Upstream SKILL.md references `/setup-matt-pocock-skills`, a skill this
    # repo does not bake — dangling as-is (issue #816), same accepted
    # tradeoff as the code-review entry.
    name = "to-tickets";
    src = builtins.readFile "${matt-skills}/skills/engineering/to-tickets/SKILL.md";
  }
  {
    name = "commit";
    src = builtins.readFile "${jordan-skills}/commit/SKILL.md";
  }
  {
    # Upstream SKILL.md references `/setup-matt-pocock-skills` and
    # `docs/agents/issue-tracker.md`, neither baked/shipped here. Left
    # dangling on purpose, matching the to-tickets precedent above: #787
    # punted the trim-vs-keep call to the implementer, who kept it verbatim;
    # no incident from to-tickets carrying the same dangling ref (#816).
    name = "code-review";
    src = builtins.readFile "${matt-skills}/skills/engineering/code-review/SKILL.md";
  }
]
