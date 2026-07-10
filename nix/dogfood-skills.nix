# The pinned upstream caveman skill (juliusbrussee/caveman, issue #486),
# baked into the dogfood image under its .md basename so the in-box skill
# preamble advertises it as /caveman (mkHarness copies each skill under
# `basename f`/`f.name` — see lib/mkHarness.nix). Upstream ships the skill
# at skills/caveman/SKILL.md; renaming happens here, at the writeText call,
# rather than vendoring a copy of the content into this repo.
#
# Defined once so flake.nix's `spindrift` module config and fixtures.nix's
# direct mkHarness mirror bake byte-identical skills (the same single-source
# pattern as nix/dogfood-defaults.nix, issue #459).
{ pkgs, caveman }:
[
  (pkgs.writeText "caveman.md" (builtins.readFile "${caveman}/skills/caveman/SKILL.md"))
]
