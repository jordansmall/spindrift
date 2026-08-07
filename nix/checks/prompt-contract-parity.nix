# Build-time/runtime parity check (issue #2320, parent #2244; widened to
# every row, including severity=="warn" ones, by issue #2356): pins the
# semantic fold connecting lib/prompt-contract.nix's pure buildTimeRejectVerdicts
# (Nix "ok"/"reject"/"advise") to the runtime validator's (agent/
# entrypoint.sh's old _validate_prompt_contract, now promptassembly.Validate
# per issue #2356) block/don't-block behavior (no "advise" state at
# runtime) -- Nix verdict == "reject" must correspond to blocking; "ok"/
# "advise" must both correspond to not blocking. This slice pins the fold
# and the fixture coverage on the pure-Nix side only; tests/prompt-contract-
# parity.bats drives the real runtime validator itself against these
# fixtures (rendered to JSON) from bats.
{ pkgs, ... }:
let
  promptContract = import ../../lib/prompt-contract.nix;
  inherit (pkgs.lib) assertMsg concatStringsSep;
  inherit (promptContract) parityFixtures parityFold validateMarkers;

  # Looks up a fixture's row severity by id -- parityFixtures itself doesn't
  # carry severity (it's a fold-input/output pair, not a full row copy), so
  # checks that need to scope themselves to reject-only or warn-only
  # fixtures resolve it back through validateMarkers.
  severityById = id: (builtins.head (builtins.filter (r: r.id == id) validateMarkers)).severity;
in
{
  # Scoped to severity=="reject" fixtures only: a severity=="warn" row's
  # fixture with gate=true/markerPresent=false has verdict=="advise" by
  # construction (see parityFixtures' doc comment), never "reject", so this
  # check would wrongly fail on warn fixtures if left unscoped.
  prompt-contract-parity-rejects-when-gate-true-and-marker-absent =
    let
      rejectFixtures = builtins.filter (f: severityById f.id == "reject") parityFixtures;
      matching = builtins.filter (f: f.gate && !f.markerPresent) rejectFixtures;
      bad = builtins.filter (f: f.verdict != "reject") matching;
      badIds = map (f: f.id) bad;
    in
    assert assertMsg (matching != [ ])
      "prompt-contract-parity: expected at least one severity==\"reject\" fixture with gate=true and markerPresent=false";
    assert assertMsg (bad == [ ])
      "prompt-contract-parity: every severity==\"reject\" fixture with gate=true and markerPresent=false must have verdict==\"reject\"; offending ids: [${concatStringsSep ", " badIds}]";
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

  prompt-contract-parity-fixtures-cover-every-row =
    let
      expectedIds = map (r: r.id) validateMarkers;
      outIds = pkgs.lib.unique (map (f: f.id) parityFixtures);
      missing = builtins.filter (id: !(builtins.elem id outIds)) expectedIds;
      extra = builtins.filter (id: !(builtins.elem id expectedIds)) outIds;
    in
    assert assertMsg (missing == [ ])
      "prompt-contract-parity: parityFixtures is missing coverage for validateMarkers row ids: [${concatStringsSep ", " missing}]";
    assert assertMsg (extra == [ ])
      "prompt-contract-parity: parityFixtures covers ids that are not rows in validateMarkers: [${concatStringsSep ", " extra}]";
    pkgs.runCommand "prompt-contract-parity-fixtures-cover-every-row" { } "touch $out";

  # Pins the severity=="warn" invariant explicitly (issue #2356): documents
  # intent rather than relying on the reject-row check above's scoping to
  # silently cover it by omission. A warn row's runtime validator never
  # blocks, so none of its fixtures may ever resolve to verdict=="reject".
  prompt-contract-parity-warn-rows-never-reject =
    let
      warnFixtures = builtins.filter (f: severityById f.id == "warn") parityFixtures;
      bad = builtins.filter (f: f.verdict == "reject") warnFixtures;
      badIds = map (f: f.id) bad;
    in
    assert assertMsg (warnFixtures != [ ])
      "prompt-contract-parity: expected at least one severity==\"warn\" row fixture";
    assert assertMsg (bad == [ ])
      "prompt-contract-parity: no severity==\"warn\" row fixture may have verdict==\"reject\" (a warn row's runtime validator never blocks); offending ids: [${concatStringsSep ", " badIds}]";
    pkgs.runCommand "prompt-contract-parity-warn-rows-never-reject" { } "touch $out";

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
