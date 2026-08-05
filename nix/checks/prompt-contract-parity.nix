# Build-time/runtime parity check (issue #2320, parent #2244): pins the
# semantic fold connecting lib/prompt-contract.nix's pure buildTimeRejectVerdicts
# (Nix "ok"/"reject"/"advise") to agent/entrypoint.sh's runtime
# _validate_prompt_contract (bash block/don't-block, no "advise" state) --
# Nix verdict == "reject" must correspond to bash blocking; "ok"/"advise"
# must both correspond to bash not blocking. This slice pins the fold and
# the fixture coverage on the pure-Nix side only; a following slice drives
# agent/entrypoint.sh itself against these fixtures (rendered to JSON) from
# bats.
{ pkgs, ... }:
let
  promptContract = import ../../lib/prompt-contract.nix;
  inherit (pkgs.lib) assertMsg concatStringsSep;
  inherit (promptContract) parityFixtures parityFold;
in
{
  prompt-contract-parity-rejects-when-gate-true-and-marker-absent =
    let
      matching = builtins.filter (f: f.gate && !f.markerPresent) parityFixtures;
      bad = builtins.filter (f: f.verdict != "reject") matching;
      badIds = map (f: f.id) bad;
    in
    assert assertMsg (matching != [ ])
      "prompt-contract-parity: expected at least one fixture with gate=true and markerPresent=false";
    assert assertMsg (bad == [ ])
      "prompt-contract-parity: every fixture with gate=true and markerPresent=false must have verdict==\"reject\"; offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-parity-rejects-when-gate-true-and-marker-absent" { } "touch $out";

  prompt-contract-parity-never-rejects-when-marker-present =
    let
      matching = builtins.filter (f: f.markerPresent) parityFixtures;
      bad = builtins.filter (f: f.verdict == "reject") matching;
      badIds = map (f: f.id) bad;
    in
    assert assertMsg (matching != [ ])
      "prompt-contract-parity: expected at least one fixture with markerPresent=true";
    assert assertMsg (bad == [ ])
      "prompt-contract-parity: no fixture with markerPresent=true may have verdict==\"reject\"; offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-parity-never-rejects-when-marker-present" { } "touch $out";

  prompt-contract-parity-never-rejects-when-gate-false =
    let
      matching = builtins.filter (f: !f.gate) parityFixtures;
      bad = builtins.filter (f: f.verdict == "reject") matching;
      badIds = map (f: f.id) bad;
    in
    assert assertMsg (matching != [ ])
      "prompt-contract-parity: expected at least one fixture with gate=false";
    assert assertMsg (bad == [ ])
      "prompt-contract-parity: no fixture with gate=false may have verdict==\"reject\"; offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-parity-never-rejects-when-gate-false" { } "touch $out";

  prompt-contract-parity-fixtures-cover-every-reject-row =
    let
      expectedIds = map (r: r.id) (
        builtins.filter (r: r.severity == "reject") promptContract.validateMarkers
      );
      outIds = pkgs.lib.unique (map (f: f.id) parityFixtures);
      missing = builtins.filter (id: !(builtins.elem id outIds)) expectedIds;
      extra = builtins.filter (id: !(builtins.elem id expectedIds)) outIds;
    in
    assert assertMsg (missing == [ ])
      "prompt-contract-parity: parityFixtures is missing coverage for severity==\"reject\" row ids: [${concatStringsSep ", " missing}]";
    assert assertMsg (extra == [ ])
      "prompt-contract-parity: parityFixtures covers ids that are not severity==\"reject\" rows in validateMarkers: [${concatStringsSep ", " extra}]";
    pkgs.runCommand "prompt-contract-parity-fixtures-cover-every-reject-row" { } "touch $out";

  # Pins the fold helper directly (exported so a future bash-side slice can
  # cite one source of truth for the mapping instead of reimplementing it).
  prompt-contract-parity-fold-matches-verdict =
    let
      bad = builtins.filter (f: parityFold f.verdict != (f.verdict != "reject")) parityFixtures;
    in
    assert assertMsg (parityFold "ok")
      "prompt-contract-parity: parityFold \"ok\" must be true (must not block at runtime)";
    assert assertMsg (parityFold "advise")
      "prompt-contract-parity: parityFold \"advise\" must be true (must not block at runtime)";
    assert assertMsg (!(parityFold "reject"))
      "prompt-contract-parity: parityFold \"reject\" must be false (must block at runtime)";
    assert assertMsg (bad == [ ])
      "prompt-contract-parity: parityFold must agree with (verdict != \"reject\") for every fixture, ${toString (builtins.length bad)} disagreed";
    pkgs.runCommand "prompt-contract-parity-fold-matches-verdict" { } "touch $out";
}
