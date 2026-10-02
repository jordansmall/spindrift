# Negative-path pins for nix/checks/mk-fragment-parity.nix's five asserts and
# its dogfood-row lookup (issue #3240 review finding), plus positive pins for
# its comparator fold and per-side needles (issue #3486): the helper itself only
# has positive callers (commit/tdd/code-review-fragment-parity.nix), so an
# inverted `hasInfix` or a dropped assert there would still evaluate green.
# Mirrors nix/checks/fragment-pairs.nix's rejectionCase/rejectionCases shape.
#
# Every case below imports the helper fresh against a synthetic one-row
# `dogfoodSkills` fixture, never the `mkFragmentParity` default.nix builds
# against the real skills -- that keeps this file's outcome independent of
# upstream SKILL.md wording, the same reason the helper avoids verbatim diffs.
{ pkgs, ... }:
let
  inherit (pkgs.lib) assertMsg;

  # Hard-wrapped and differently-cased than fallbackText on purpose: this is
  # also the module's only exercise of the helper's `normalize` matching a
  # clause across a line break and a case change.
  clauseText = "state the shared clause plainly";
  goodSkillSrc = ''
    This SKILL demonstrates behavior for the fixture.  It must
    state
    the SHARED clause plainly, per the fixture's contract.
  '';
  goodSkillRow = {
    name = "reject-fixture-skill";
    src = goodSkillSrc;
  };
  goodFallbackText = "The fallback restates: state the shared clause plainly, matching the skill.";
  goodAnchorText = "See the /reject-fixture-skill skill.";

  baseArgs = {
    skillName = "reject-fixture-skill";
    sourceFile = "nix/checks/mk-fragment-parity-rejects.nix (synthetic fixture)";
    skillDesc = "the fixture skill";
    fallbackText = goodFallbackText;
    fallbackDesc = "the fixture fallback";
    anchorText = goodAnchorText;
    anchorDesc = "the fixture anchor";
    sharedClauses = [
      {
        name = "shared";
        clause = clauseText;
      }
    ];
    stepProseMarkers = [ "prose-marker" ];
    proseKind = "step prose";
    carriedKind = "step prose";
    discipline = "testing";
  };

  # Skill and fallback space the comparator differently on purpose (issue #3486).
  comparatorArgs = baseArgs // {
    fallbackText = "Keep it short: subject <=50 characters.";
    sharedClauses = [
      {
        name = "shared";
        clause = "subject ≤50";
      }
    ];
  };
  comparatorSkillRow = goodSkillRow // {
    src = "The subject line is subject ≤ 50 characters.";
  };

  # The two sides state one bound in wording that shares no literal.
  perSideSkillRow = goodSkillRow // {
    src = "Body text is wrapped at 72 columns.";
  };
  perSideArgs = baseArgs // {
    fallbackText = "The body ≤72 columns.";
    sharedClauses = [
      {
        name = "shared";
        skillClause = "wrapped at 72";
        fallbackClause = "body ≤72";
      }
    ];
  };

  clauseCheckKey = "reject-fixture-skill-fragment-parity-clause-shared";
  anchorCheckKey = "reject-fixture-skill-fragment-parity-baked-anchor-omits-step-prose";

  rejectionCases = [
    {
      # Pins clauseCheck's skill-side assert -- the skill no longer states a
      # clause the fallback still carries, which is exactly the drift the
      # check exists to catch.
      name = "mk-fragment-parity-clause-missing-from-skill-fails";
      skillRows = [
        (
          goodSkillRow
          // {
            src = "This skill covers something else entirely and never states the target phrase.";
          }
        )
      ];
      args = baseArgs;
      checkKey = clauseCheckKey;
      message = "the clause assert must fail when the skill text drops a clause the fallback still states";
    }
    {
      # Pins clauseCheck's fallback-side assert -- the mirror direction: the
      # fallback drifted away from a clause the skill still teaches.
      name = "mk-fragment-parity-clause-missing-from-fallback-fails";
      skillRows = [ goodSkillRow ];
      args = baseArgs // {
        fallbackText = "The fallback talks about unrelated things and omits the phrase.";
      };
      checkKey = clauseCheckKey;
      message = "the clause assert must fail when the fallback text drops a clause the skill still states";
    }
    {
      # Pins the baked anchor's leaked-marker assert -- a marker leaking into the
      # baked anchor means the anchor regrew the paragraph the unbaked/baked
      # split exists to remove.
      name = "mk-fragment-parity-leaked-marker-in-anchor-fails";
      skillRows = [ goodSkillRow ];
      args = baseArgs // {
        anchorText = "See the /reject-fixture-skill skill for prose-marker guidance.";
      };
      checkKey = anchorCheckKey;
      message = "the anchor assert must fail when a stepProseMarkers entry leaks into anchorText";
    }
    {
      # Pins the baked anchor's one-line assert -- the baked arm is a line, not
      # a paragraph; a second line is the same regression as case 3 in a
      # different shape.
      name = "mk-fragment-parity-multiline-anchor-fails";
      skillRows = [ goodSkillRow ];
      args = baseArgs // {
        anchorText = "See the /reject-fixture-skill skill.\nAdditional detail on a second line.";
      };
      checkKey = anchorCheckKey;
      message = "the anchor assert must fail when anchorText is more than one non-empty line";
    }
    {
      # Pins the baked anchor's skill-name assert -- an anchor that lost the
      # `/<skillName>` mention says nothing about the skill it points at.
      name = "mk-fragment-parity-anchor-missing-skill-name-fails";
      skillRows = [ goodSkillRow ];
      args = baseArgs // {
        anchorText = "See the documentation for details.";
      };
      checkKey = anchorCheckKey;
      message = "the anchor assert must fail when anchorText no longer names the /<skillName> skill";
    }
    {
      # Pins skillRowByName's throw -- a `skillName` that isn't a row in
      # `fixtures.dogfoodSkills` (e.g. after a nix/dogfood-skills.nix rename)
      # must throw, not silently skip the parity check.
      name = "mk-fragment-parity-missing-dogfood-row-throws";
      skillRows = [ (goodSkillRow // { name = "totally-different-skill"; }) ];
      args = baseArgs;
      checkKey = clauseCheckKey;
      message = "skillRowByName must throw when skillName names no row in fixtures.dogfoodSkills";
    }
    {
      # Pins that a bound change on one side alone stays red even though
      # comparator spacing is folded -- the number must still be compared.
      name = "mk-fragment-parity-fallback-bound-change-fails";
      skillRows = [ comparatorSkillRow ];
      args = comparatorArgs // {
        fallbackText = "Keep it short: subject ≤60 characters.";
      };
      checkKey = clauseCheckKey;
      message = "the clause assert must fail when only the fallback's bound changes";
    }
    {
      # Mirror of the case above for the skill side.
      name = "mk-fragment-parity-skill-bound-change-fails";
      skillRows = [ (comparatorSkillRow // { src = "The subject line is subject ≤ 60 characters."; }) ];
      args = comparatorArgs;
      checkKey = clauseCheckKey;
      message = "the clause assert must fail when only the skill's bound changes";
    }
    {
      # Pins per-side needles -- the fallback's own bound drifting must fail
      # even though the skill needle shares no literal with it.
      name = "mk-fragment-parity-per-side-fallback-bound-change-fails";
      skillRows = [ perSideSkillRow ];
      args = perSideArgs // {
        fallbackText = "The body ≤80 columns.";
      };
      checkKey = clauseCheckKey;
      message = "the clause assert must fail when the fallback-side needle's bound changes";
    }
    {
      # Pins per-side needles -- the skill's own bound drifting must fail.
      name = "mk-fragment-parity-per-side-skill-bound-change-fails";
      skillRows = [ (perSideSkillRow // { src = "Body text is wrapped at 80 columns."; }) ];
      args = perSideArgs;
      checkKey = clauseCheckKey;
      message = "the clause assert must fail when the skill-side needle's bound changes";
    }
  ];

  mkFor =
    skillRows:
    import ./mk-fragment-parity.nix {
      inherit pkgs;
      fixtures.dogfoodSkills = skillRows;
    };

  rejectionCase = c: {
    name = c.name;
    value =
      let
        mk = mkFor c.skillRows;
        result = builtins.tryEval (mk c.args).checks.${c.checkKey};
      in
      assert assertMsg (!result.success) c.message;
      pkgs.runCommand c.name { } "touch $out";
  };

  validationCases = [
    {
      # Pins `normalize`'s comparator fold: "≤ 50" and "<=50" are one bound.
      name = "mk-fragment-parity-comparator-spacing-validates";
      skillRows = [ comparatorSkillRow ];
      args = comparatorArgs;
    }
    {
      # Pins the spaced ASCII spelling "<= 50" folding to "≤50".
      name = "mk-fragment-parity-spaced-ascii-comparator-validates";
      skillRows = [ (comparatorSkillRow // { src = "The subject line is subject <= 50 characters."; }) ];
      args = comparatorArgs // {
        fallbackText = "Keep it short: subject ≤50 characters.";
      };
    }
    {
      # Pins the "≤ " fold here, and the " ≤" fold through the needle.
      name = "mk-fragment-parity-comparator-leading-space-validates";
      skillRows = [ comparatorSkillRow ];
      args = comparatorArgs // {
        fallbackText = "Keep it short: subject≤ 50 characters.";
      };
    }
    {
      # Pins per-side needles: neither side contains the other's needle.
      name = "mk-fragment-parity-per-side-clauses-validate";
      skillRows = [ perSideSkillRow ];
      args = perSideArgs;
    }
  ];

  # Forces the check itself: a failing assert aborts inside the seq with the
  # helper's own drift message, so no assertMsg wrapper is needed.
  validationCase = c: {
    name = c.name;
    value = builtins.seq (mkFor c.skillRows c.args).checks.${clauseCheckKey} (
      pkgs.runCommand c.name { } "touch $out"
    );
  };
in
builtins.listToAttrs (map rejectionCase rejectionCases)
// builtins.listToAttrs (map validationCase validationCases)
// {
  # Without this, a `mk-fragment-parity.nix` whose asserts always failed (or
  # whose skillRowByName always threw) would satisfy every rejectionCases
  # entry above vacuously.
  mk-fragment-parity-well-formed-input-validates =
    let
      mk = mkFor [ goodSkillRow ];
      parity = mk baseArgs;
      checks = parity.checks;
      names = builtins.attrNames checks;
      # Forces each check's assert chain, not just the attrset's shape.
      allForce = builtins.all (n: builtins.seq checks.${n} true) names;
    in
    assert assertMsg allForce
      "every check in a well-formed input's `checks` attrset must evaluate without asserting";
    assert assertMsg
      (
        names == [
          anchorCheckKey
          clauseCheckKey
        ]
      )
      "a well-formed input's `checks` attrset must carry exactly the -clause-<name> and -baked-anchor-omits-step-prose entries, got: ${builtins.concatStringsSep ", " names}";
    # `anchorLines` is the builder's other output, consumed by
    # code-review-fragment-parity.nix for its rostered-agent-type check. No
    # rejection case above forces it, so an inverted `anchorLines` filter in
    # the helper would otherwise stay green across this whole file.
    assert assertMsg (parity.anchorLines == [ goodAnchorText ])
      "a well-formed input's `anchorLines` must be the anchor's one non-empty line, got: ${builtins.toJSON parity.anchorLines}";
    pkgs.runCommand "mk-fragment-parity-well-formed-input-validates" { } "touch $out";
}
