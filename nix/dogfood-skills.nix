# The pinned upstream caveman skill (juliusbrussee/caveman, issue #486),
# baked into the dogfood image under its .md basename so the in-box skill
# preamble advertises it as /caveman. Declared as { name; src; } content
# (issue #597) rather than a pre-built `pkgs.writeText` derivation: the image
# re-realizes it with its own Linux pkgs (lib/image.nix), so no consumer host
# `pkgs` may appear here — that host tag is what made the agent-image
# drvPath diverge across hosts. Upstream ships the skill at
# skills/caveman/SKILL.md; renaming happens here, at the `name` field, rather
# than vendoring a copy of the content into this repo.
#
# Defined once so flake.nix's `spindrift` module config and fixtures.nix's
# direct mkHarness mirror bake byte-identical skills (the same single-source
# pattern as nix/dogfood-defaults.nix, issue #459).
{ caveman }:
[
  {
    name = "caveman.md";
    src = builtins.readFile "${caveman}/skills/caveman/SKILL.md";
  }
]
