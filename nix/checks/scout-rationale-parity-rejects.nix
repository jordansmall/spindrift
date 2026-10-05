# Negative-path pins for nix/checks/scout-rationale-pointer-window.nix (issue
# #3613): scout-rationale-parity.nix's pointer checks only ever see the real
# comments, which all pass, so a matcher regressed to a joined-block match
# would still evaluate green. The acceptance cases guard the other direction --
# an always-false matcher would satisfy every rejection vacuously. Unlike
# mk-fragment-parity-rejects.nix, cases assert a boolean `expect` rather than
# tryEval a throw: the predicate returns a bool and never throws.
{ pkgs, ... }:
let
  inherit (pkgs.lib) assertMsg;
  hasPointerInWindow = import ./scout-rationale-pointer-window.nix { inherit (pkgs) lib; };
  needles = [
    "lib/mkHarness.nix"
    "scoutProvisioned"
  ];

  cases = [
    {
      slug = "non-adjacent-names";
      expect = false;
      message = "scout-rationale-parity-rejects: a block naming lib/mkHarness.nix and scoutProvisioned in unrelated sentences three lines apart was accepted.";
      lines = [
        "// See lib/mkHarness.nix for the build details."
        "// Unrelated prose that fills the gap between the two"
        "// sentences of this comment block."
        "// The scoutProvisioned flag is mentioned only here."
      ];
    }
    {
      # The width boundary: rejected by a two-line window, accepted by any
      # wider one, so widening the window fails here.
      slug = "one-line-gap";
      expect = false;
      message = "scout-rationale-parity-rejects: a block with a full line of prose between lib/mkHarness.nix and scoutProvisioned was accepted.";
      lines = [
        "// See lib/mkHarness.nix for the build details."
        "// A full line of unrelated prose sits between the two names."
        "// The scoutProvisioned flag is mentioned only here."
      ];
    }
    {
      slug = "empty-block";
      expect = false;
      message = "scout-rationale-parity-rejects: an empty comment block was accepted.";
      lines = [ ];
    }
    {
      slug = "name-missing";
      expect = false;
      message = "scout-rationale-parity-rejects: a block naming only scoutProvisioned was accepted.";
      lines = [
        "// scoutProvisioned is explained elsewhere."
        "// Nothing here names the canonical file."
      ];
    }
    {
      slug = "reflowed-across-two-lines";
      expect = true;
      message = "scout-rationale-parity-rejects: a pointer reflowed across two adjacent lines was rejected.";
      lines = [
        "// Some preamble about the opencode driver. For why, see lib/mkHarness.nix's"
        "// scoutProvisioned comment."
        "// Trailing unrelated prose."
      ];
    }
    {
      # The pointer sits in the block's final window, so a genList count that
      # drops the last window fails here.
      slug = "pointer-in-last-window";
      expect = true;
      message = "scout-rationale-parity-rejects: a pointer reflowed across a block's last two lines was rejected.";
      lines = [
        "// Leading unrelated prose about the opencode driver."
        "// More unrelated prose. For why, see lib/mkHarness.nix's"
        "// scoutProvisioned comment."
      ];
    }
    {
      slug = "single-line-block";
      expect = true;
      message = "scout-rationale-parity-rejects: a one-line block naming both lib/mkHarness.nix and scoutProvisioned was rejected.";
      lines = [ "# See lib/mkHarness.nix's scoutProvisioned comment." ];
    }
  ];

  mkCase =
    c:
    let
      name = "scout-rationale-parity-rejects-${c.slug}";
    in
    {
      inherit name;
      value =
        assert assertMsg (hasPointerInWindow needles c.lines == c.expect) c.message;
        pkgs.runCommand name { } "touch $out";
    };
in
builtins.listToAttrs (map mkCase cases)
