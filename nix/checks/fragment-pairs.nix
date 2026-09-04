# Eval-level pins for lib/fragment-pairs.nix (issue #3219): one assertion
# per rejection rule its `validate` enforces, plus a same-shape positive case
# and a pin on what the real lib/fragments.nix registry declares.
#
# The registry check below pins `pairsOf` rather than re-running `validate`:
# lib/fragments.nix ends in `assert (import ./fragment-pairs.nix).validate
# rows`, so a registry that fails validation throws at import and never
# reaches an assertMsg here -- a `validate`-shaped check would be vacuous.
{ pkgs, ... }:
let
  fragmentPairs = import ../../lib/fragment-pairs.nix;
  inherit (pkgs.lib) assertMsg;

  row = gate: fragment: var: {
    inherit gate fragment var;
  };
  rowInverse = gate: fragment: var: inverseOf: {
    inherit
      gate
      fragment
      var
      inverseOf
      ;
  };

  # The shape the real registry declares, reduced to two rows: an on-member
  # and the off-member naming it. Shared by the two well-formed checks so
  # they cannot drift into pinning different shapes.
  scoutRows = [
    (row "SCOUT_PROVISIONED" "scout-delegate.md" "SCOUT_DELEGATE_STEP")
    (rowInverse "SCOUT_ABSENT" "scout-absent.md" "SCOUT_ABSENT_STEP" "SCOUT_PROVISIONED")
  ];

  # The four ways a declared `inverseOf` can be malformed, each reduced to
  # its own minimal `rows` and the message `validate` must throw. Shares one
  # `rejectionCase` builder below instead of re-spelling the same
  # let/tryEval/assertMsg/runCommand shape four times (same idiom as
  # `clauseCheck` + `sharedClauses` in nix/checks/tdd-fragment-parity.nix).
  rejectionCases = [
    {
      # A row naming itself is not a pair at all: one gate cannot be its own
      # inverse, so both "members" render together whenever that gate is on.
      name = "fragment-pairs-self-reference-throws";
      rows = [ (rowInverse "A" "a.md" "A_STEP" "A") ];
      message = "validate must throw when a row declares inverseOf itself";
    }
    {
      # A typo'd or since-renamed on-gate leaves the off-member alone in the
      # registry: nothing renders when the knob is on, so the prompt silently
      # loses a section instead of failing the build.
      name = "fragment-pairs-dangling-on-gate-throws";
      rows = [ (rowInverse "OFF" "off.md" "OFF_STEP" "MISSING") ];
      message = "validate must throw when the referenced on-gate is not the gate of any row";
    }
    {
      # Chaining inverses (OFF is the inverse of ON, which is itself the
      # inverse of X) means the pair's "on" side is no longer one boolean
      # knob two gates are computed from, which is the whole basis for
      # exactly-one-on.
      name = "fragment-pairs-inverse-of-inverse-chain-throws";
      rows = [
        (row "X" "x.md" "X_STEP")
        (rowInverse "ON" "on.md" "ON_STEP" "X")
        (rowInverse "OFF" "off.md" "OFF_STEP" "ON")
      ];
      message = "validate must throw when the referenced on-gate itself carries inverseOf";
    }
    {
      # One off-gate claimed by two different on-gates cannot be the inverse
      # of both: whichever on-gate is true, the off-member paired with the
      # *other* one renders next to it.
      name = "fragment-pairs-off-gate-claimed-by-two-on-gates-throws";
      rows = [
        (row "ON1" "on1.md" "ON1_STEP")
        (row "ON2" "on2.md" "ON2_STEP")
        (rowInverse "OFF" "off1.md" "OFF_STEP" "ON1")
        (rowInverse "OFF" "off2.md" "OFF_STEP2" "ON2")
      ];
      message = "validate must throw when an off-gate is declared the inverse of two different on-gates";
    }
  ];

  rejectionCase = c: {
    name = c.name;
    value =
      let
        result = builtins.tryEval (fragmentPairs.validate c.rows);
      in
      assert assertMsg (!result.success) c.message;
      pkgs.runCommand c.name { } "touch $out";
  };
in
{
  # The positive case: a correct declaration must not throw, or every
  # `rejectionCases` entry would pass for the wrong reason (a `validate` that
  # threw unconditionally would satisfy all of them).
  fragment-pairs-well-formed-pair-validates =
    let
      out = fragmentPairs.validate scoutRows;
    in
    assert assertMsg (out == true) "validate must return true for a well-formed exactly-one-on pair";
    pkgs.runCommand "fragment-pairs-well-formed-pair-validates" { } "touch $out";

  # `pairsOf` is the reusable half of the module -- it is what turns the
  # per-row `inverseOf` string into the `{ on; off; }` list downstream
  # consumers (and `validate`'s own grouping) work from, so its output shape
  # is pinned independently of whether validation passes.
  fragment-pairs-pairs-of-derives-declared-pair =
    let
      out = fragmentPairs.pairsOf scoutRows;
    in
    assert assertMsg (
      out == [
        {
          on = "SCOUT_PROVISIONED";
          off = "SCOUT_ABSENT";
        }
      ]
    ) "pairsOf must derive the declared { on; off; } pair from the inverseOf-carrying row";
    pkgs.runCommand "fragment-pairs-pairs-of-derives-declared-pair" { } "touch $out";
}
// builtins.listToAttrs (map rejectionCase rejectionCases)
// {
  # Pins what the real registry declares, so dropping an `inverseOf` from a
  # row (or adding one without meaning to) fails here rather than quietly
  # making the eval-time assert in lib/fragments.nix vacuous again.
  # `pairsOf` preserves registry order, so this list is in lib/fragments.nix's
  # own row order, not alphabetical.
  fragment-pairs-real-registry-declares-declared-pairs =
    let
      out = fragmentPairs.pairsOf (import ../../lib/fragments.nix);
    in
    assert assertMsg
      (
        out == [
          {
            on = "TDD_BAKED";
            off = "TDD_UNBAKED";
          }
          {
            on = "COMMIT_BAKED";
            off = "COMMIT_UNBAKED";
          }
          {
            on = "CODE_REVIEW_BAKED";
            off = "CODE_REVIEW_UNBAKED";
          }
          {
            on = "SCOUT_PROVISIONED";
            off = "SCOUT_ABSENT";
          }
        ]
      )
      "lib/fragments.nix must declare exactly the TDD_BAKED/TDD_UNBAKED, COMMIT_BAKED/COMMIT_UNBAKED, CODE_REVIEW_BAKED/CODE_REVIEW_UNBAKED, and SCOUT_PROVISIONED/SCOUT_ABSENT pairs";
    pkgs.runCommand "fragment-pairs-real-registry-declares-declared-pairs" { } "touch $out";
}
