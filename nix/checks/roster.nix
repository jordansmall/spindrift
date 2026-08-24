# Eval-level checks for lib/roster.nix's normalizeRoster (issue #2152 slice
# A): pins name-validation, duplicate-name rejection, and the promptFile
# default-injection contract before any Driver ever consumes a roster.
{
  pkgs,
  ...
}:
let
  rosterLib = import ../../lib/roster.nix { inherit (pkgs) lib; };
  defaultModelFixture = import ../../lib/default-model-fixture.nix;
  inherit (pkgs.lib) assertMsg mapAttrs hasInfix;
  # Shared by the roster-default-roster-by-name-* checks below (issue #2560,
  # non-blocking review finding): pulls a named entry out of a roster, same
  # shape as equivalence.nix's modelOf but returning the whole entry since
  # callers here read both .model and .effort off it.
  entryFor = name: roster: builtins.head (builtins.filter (e: e.name == name) roster);
in
{
  # Issue #2571 review fix (Finding B): normalizeRosterResult never throws --
  # it returns a structured { ok; value; violation; entryName; message; }
  # result, so this asserts directly on .ok/.violation/.entryName (which
  # entry, which problem) rather than only "did eval abort" via tryEval
  # (builtins.tryEval can't recover the thrown message text).
  roster-normalize-rejects-invalid-name =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "Bad_Name";
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
        }
      ];
    in
    assert assertMsg (
      result.ok == false
    ) "normalizeRosterResult must not accept a name that isn't lowercase-alnum-dash";
    assert assertMsg (result.violation == "invalid-name")
      "normalizeRosterResult must report violation == \"invalid-name\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "Bad_Name")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    # Issue #2571 blocking review finding: pin message content, not just
    # .ok/.violation/.entryName -- normalizeRoster (the throwing wrapper)
    # throws this exact .message unmodified, so this is the only way to pin
    # that the production throw path actually names the entry and the
    # problem (AC1), since builtins.tryEval can't recover a thrown message.
    assert assertMsg (hasInfix "Bad_Name" result.message)
      "normalizeRosterResult's message must name the offending entry \"Bad_Name\", got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "invalid name" result.message)
      "normalizeRosterResult's message must describe the problem (\"invalid name\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-invalid-name" { } "touch $out";

  # Issue #2571 non-blocking review finding (AC1 gap): the checks above only
  # prove normalizeRosterResult's structured output, never that the throwing
  # wrapper production code (lib/mkHarness.nix) actually calls --
  # normalizeRoster -- really throws for this violation class.
  roster-normalize-throws-on-invalid-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "Bad_Name";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success)
      "normalizeRoster must throw on a name that isn't lowercase-alnum-dash";
    pkgs.runCommand "roster-normalize-throws-on-invalid-name" { } "touch $out";

  # Issue #2571 round-3 review finding: builtins.match throws its own opaque
  # type error if e.name isn't a string at all (e.g. a bare number) -- must
  # fail with a clean, named "invalid-name" violation (same tag as the
  # format check, just a different reason) instead of aborting eval
  # mid-match with a message that names neither the entry nor the problem.
  roster-normalize-rejects-non-string-name =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = 5;
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
        }
      ];
    in
    assert assertMsg (
      result.ok == false
    ) "normalizeRosterResult must not accept a name that isn't a string";
    assert assertMsg (result.violation == "invalid-name")
      "normalizeRosterResult must report violation == \"invalid-name\", got: ${builtins.toJSON result.violation}";
    # Issue #2571 blocking review finding: see
    # roster-normalize-rejects-invalid-name above for rationale.
    assert assertMsg (hasInfix "invalid name" result.message)
      "normalizeRosterResult's message must describe the problem (\"invalid name\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-non-string-name" { } "touch $out";

  # Issue #2571 non-blocking review finding (AC1 gap): see
  # roster-normalize-throws-on-invalid-name above for rationale.
  roster-normalize-throws-on-non-string-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = 5;
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success) "normalizeRoster must throw on a name that isn't a string";
    pkgs.runCommand "roster-normalize-throws-on-non-string-name" { } "touch $out";

  roster-normalize-rejects-missing-name =
    let
      result = rosterLib.normalizeRosterResult [
        {
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
        }
      ];
    in
    assert assertMsg (
      result.ok == false
    ) "normalizeRosterResult must not accept an entry that omits name";
    assert assertMsg (result.violation == "missing-name")
      "normalizeRosterResult must report violation == \"missing-name\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == null)
      "normalizeRosterResult must report entryName == null when the name itself is missing, got: ${builtins.toJSON result.entryName}";
    # Issue #2571 blocking review finding: see
    # roster-normalize-rejects-invalid-name above for rationale.
    assert assertMsg (hasInfix "entry 0" result.message)
      "normalizeRosterResult's message must identify the offending entry by index (\"entry 0\"), got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "missing a name" result.message)
      "normalizeRosterResult's message must describe the problem (\"missing a name\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-missing-name" { } "touch $out";

  # Issue #2571 non-blocking review finding: normalizeRosterResult's
  # documented failure shape is exactly { ok; value; violation; entryName;
  # message; } (its doc comment above the function) -- the failure branch
  # must construct this explicitly, not return the internal fold
  # accumulator (which also carries seen/out) verbatim.
  roster-normalize-result-failure-shape-has-no-internal-keys =
    let
      result = rosterLib.normalizeRosterResult [
        {
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
        }
      ];
    in
    assert assertMsg (
      result.ok == false
    ) "normalizeRosterResult must not accept an entry that omits name";
    assert assertMsg (
      builtins.attrNames result == [
        "entryName"
        "message"
        "ok"
        "value"
        "violation"
      ]
    ) "normalizeRosterResult's failure branch must return exactly the documented { ok; value; violation; entryName; message; } shape, got attrNames: ${builtins.toJSON (builtins.attrNames result)}";
    pkgs.runCommand "roster-normalize-result-failure-shape-has-no-internal-keys" { } "touch $out";

  # Issue #2571 non-blocking review finding (AC1 gap): see
  # roster-normalize-throws-on-invalid-name above for rationale.
  roster-normalize-throws-on-missing-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success) "normalizeRoster must throw on an entry that omits name";
    pkgs.runCommand "roster-normalize-throws-on-missing-name" { } "touch $out";

  roster-normalize-accepts-valid-name =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "scout-2";
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
          prompt = "some prompt text";
        }
      ];
    in
    assert assertMsg (
      result.ok == true
    ) "normalizeRosterResult must accept a valid lowercase-alnum-dash name";
    pkgs.runCommand "roster-normalize-accepts-valid-name" { } "touch $out";

  roster-normalize-rejects-duplicate-name =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "scout";
          model = "m1";
          mode = "subagent";
          description = "d1";
          tools = [ ];
        }
        {
          name = "scout";
          model = "m2";
          mode = "subagent";
          description = "d2";
          tools = [ ];
        }
      ];
    in
    assert assertMsg (
      result.ok == false
    ) "normalizeRosterResult must not accept two entries that share a name";
    assert assertMsg (result.violation == "duplicate-name")
      "normalizeRosterResult must report violation == \"duplicate-name\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "scout")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    # Issue #2571 blocking review finding: see
    # roster-normalize-rejects-invalid-name above for rationale.
    assert assertMsg (hasInfix "scout" result.message)
      "normalizeRosterResult's message must name the offending entry \"scout\", got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "duplicate name" result.message)
      "normalizeRosterResult's message must describe the problem (\"duplicate name\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-duplicate-name" { } "touch $out";

  # Issue #2571 non-blocking review finding (AC1 gap): see
  # roster-normalize-throws-on-invalid-name above for rationale.
  roster-normalize-throws-on-duplicate-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "m1";
              mode = "subagent";
              description = "d1";
              tools = [ ];
            }
            {
              name = "scout";
              model = "m2";
              mode = "subagent";
              description = "d2";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success)
      "normalizeRoster must throw when two entries share a name";
    pkgs.runCommand "roster-normalize-throws-on-duplicate-name" { } "touch $out";

  # Issue #2571 slice 1: an entry's keys must be a subset of the documented
  # roster entry shape (docs/reference.md's "Subagent roster" section) --
  # any stray/unknown key (e.g. a typo) must throw rather than silently
  # passing through to the Drivers.
  roster-normalize-rejects-unknown-key =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "scout";
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
          nonesuch = "x";
        }
      ];
    in
    assert assertMsg (
      result.ok == false
    ) "normalizeRosterResult must not accept an entry with an unknown key";
    assert assertMsg (result.violation == "unknown-key")
      "normalizeRosterResult must report violation == \"unknown-key\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "scout")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    # Issue #2571 blocking review finding: see
    # roster-normalize-rejects-invalid-name above for rationale.
    assert assertMsg (hasInfix "scout" result.message)
      "normalizeRosterResult's message must name the offending entry \"scout\", got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "nonesuch" result.message)
      "normalizeRosterResult's message must name the unknown key \"nonesuch\", got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-unknown-key" { } "touch $out";

  # Issue #2571 non-blocking review finding (AC1 gap): see
  # roster-normalize-throws-on-invalid-name above for rationale.
  roster-normalize-throws-on-unknown-key =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
              nonesuch = "x";
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success)
      "normalizeRoster must throw on an entry with an unknown key";
    pkgs.runCommand "roster-normalize-throws-on-unknown-key" { } "touch $out";

  # Issue #2571 slice 1: every entry must literally carry a `model` key, even
  # when its value is the empty-string opt-out sentinel (#392) -- omitting
  # the key entirely (typo/oversight) must throw.
  roster-normalize-rejects-missing-model =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "scout";
          mode = "subagent";
          description = "d";
          tools = [ ];
        }
      ];
    in
    assert assertMsg (
      result.ok == false
    ) "normalizeRosterResult must not accept an entry that omits model";
    assert assertMsg (result.violation == "missing-model")
      "normalizeRosterResult must report violation == \"missing-model\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "scout")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    # Issue #2571 blocking review finding: see
    # roster-normalize-rejects-invalid-name above for rationale.
    assert assertMsg (hasInfix "scout" result.message)
      "normalizeRosterResult's message must name the offending entry \"scout\", got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "missing model" result.message)
      "normalizeRosterResult's message must describe the problem (\"missing model\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-missing-model" { } "touch $out";

  # Issue #2571 non-blocking review finding (AC1 gap): see
  # roster-normalize-throws-on-invalid-name above for rationale.
  roster-normalize-throws-on-missing-model =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success) "normalizeRoster must throw on an entry that omits model";
    pkgs.runCommand "roster-normalize-throws-on-missing-model" { } "touch $out";

  # Issue #2571 round-3 review finding: model = null (or any other non-string
  # value) currently passes normalizeRosterResult straight through and gets
  # baked into the --agents JSON as "model": null. Reuses the "missing-model"
  # tag (both mean "there's no usable model value") rather than adding a
  # distinct tag.
  roster-normalize-rejects-non-string-model =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "scout";
          model = null;
          mode = "subagent";
          description = "d";
          tools = [ ];
        }
      ];
    in
    assert assertMsg (
      result.ok == false
    ) "normalizeRosterResult must not accept an entry whose model isn't a string";
    assert assertMsg (result.violation == "missing-model")
      "normalizeRosterResult must report violation == \"missing-model\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "scout")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    assert assertMsg (hasInfix "scout" result.message)
      "normalizeRosterResult's message must name the offending entry \"scout\", got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "missing model" result.message)
      "normalizeRosterResult's message must describe the problem (\"missing model\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-non-string-model" { } "touch $out";

  # Issue #2571 non-blocking review finding (AC1 gap): see
  # roster-normalize-throws-on-invalid-name above for rationale.
  roster-normalize-throws-on-non-string-model =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = null;
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success) "normalizeRoster must throw on an entry whose model isn't a string";
    pkgs.runCommand "roster-normalize-throws-on-non-string-model" { } "touch $out";

  # Issue #2571 slice 1: model = "" is a well-established, permanent explicit
  # opt-out sentinel (#392) -- normalizeRoster must accept it (not throw).
  # Issue #2571 review fix (Finding A): normalizeRoster no longer drops such
  # an entry from the returned list -- model = "" is an ordinary, valid
  # value that passes through completely unfiltered (the #392
  # opt-out-from-the-built-image behavior moves to an explicit step in
  # lib/mkHarness.nix in a later slice, not silently inside this funnel).
  roster-normalize-accepts-explicit-empty-model =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      result.success
    ) "normalizeRoster must not throw on an entry with an explicit empty model";
    assert assertMsg (builtins.length result.value == 1)
      "normalizeRoster must retain an entry with an explicit empty model in the returned list, got: ${builtins.toJSON result.value}";
    assert assertMsg ((builtins.elemAt result.value 0).model == "")
      "normalizeRoster must preserve model == \"\" on the retained entry, got: ${builtins.toJSON (builtins.elemAt result.value 0).model}";
    pkgs.runCommand "roster-normalize-accepts-explicit-empty-model" { } "touch $out";

  # Issue #2571 review fix (Finding A): a duplicate name must still be
  # rejected even when the first occurrence has model == "" -- duplicate
  # detection runs before model is even inspected, so this is unaffected by
  # the removal of the empty-model drop (there's no drop left to reference).
  roster-normalize-duplicate-name-detected-with-empty-model =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "scout";
          model = "";
          mode = "subagent";
          description = "d1";
          tools = [ ];
        }
        {
          name = "scout";
          model = "m";
          mode = "subagent";
          description = "d2";
          tools = [ ];
        }
      ];
    in
    assert assertMsg (result.ok == false)
      "normalizeRosterResult must still detect a duplicate name when the first occurrence has an empty model";
    assert assertMsg (result.violation == "duplicate-name")
      "normalizeRosterResult must report violation == \"duplicate-name\", got: ${builtins.toJSON result.violation}";
    pkgs.runCommand "roster-normalize-duplicate-name-detected-with-empty-model" { } "touch $out";

  # Issue #2571 slice 2: an entry whose effective promptFile (explicit or
  # injected default) doesn't exist on disk under templates/default/prompts
  # and carries no inline prompt must throw.
  roster-normalize-rejects-nonexistent-promptfile =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "nonesuch";
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
        }
      ];
    in
    assert assertMsg (result.ok == false)
      "normalizeRosterResult must not accept an entry whose effective promptFile doesn't exist on disk and carries no inline prompt";
    assert assertMsg (result.violation == "missing-promptfile")
      "normalizeRosterResult must report violation == \"missing-promptfile\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "nonesuch")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    # Issue #2571 blocking review finding: pin message content for the
    # "missing-promptfile" violation class too -- see
    # roster-normalize-rejects-invalid-name above for rationale.
    assert assertMsg (hasInfix "nonesuch" result.message)
      "normalizeRosterResult's message must name the offending entry \"nonesuch\", got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "does not exist" result.message)
      "normalizeRosterResult's message must describe the problem (\"does not exist\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-nonexistent-promptfile" { } "touch $out";

  # Issue #2571 round-3 review finding (flagged non-blocking in review rounds
  # 1, 2, and 3, never fixed until now): builtins.pathExists alone blesses
  # non-files -- promptFile = ".", "..", "fragments" (a real subdirectory
  # under templates/default/prompts), and a path-traversal escape all
  # resolve to something that pathExists reports as existing even though
  # none of them is a usable prompt file. All four must be rejected under
  # the same "missing-promptfile" tag as a genuinely nonexistent file --
  # "the effective promptFile doesn't resolve to a usable prompt file"
  # covers path-traversal, directory, and missing uniformly.
  roster-normalize-rejects-unusable-promptfile =
    let
      rejects =
        promptFile:
        let
          result = rosterLib.normalizeRosterResult [
            {
              name = "nonesuch";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
              inherit promptFile;
            }
          ];
        in
        result.ok == false
        && result.violation == "missing-promptfile"
        # Issue #2571 blocking review finding: pin message content for the
        # "missing-promptfile" violation class too -- see
        # roster-normalize-rejects-invalid-name above for rationale.
        && hasInfix "nonesuch" result.message
        && hasInfix "does not exist" result.message;
      cases = [
        "."
        ".."
        "fragments"
        "../../../../../../etc/passwd"
      ];
      failures = builtins.filter (pf: !(rejects pf)) cases;
    in
    assert assertMsg (failures == [ ])
      "normalizeRosterResult must reject every unusable promptFile (directory, '.', '..', or a path-traversal escape) with violation == \"missing-promptfile\" and a message naming the entry and problem, but accepted or under-reported: ${builtins.toJSON failures}";
    pkgs.runCommand "roster-normalize-rejects-unusable-promptfile" { } "touch $out";

  # Issue #2571 blocking review finding: a promptFile that isn't a string at
  # all (e.g. null -- the same sentinel defaultRoster uses for `prompt`,
  # so a plausible Consumer spelling mistake confusing promptFile with
  # prompt) must fail with a clean, named violation rather than aborting
  # eval mid-interpolation with an opaque "cannot coerce null to a string".
  roster-normalize-rejects-non-string-promptfile =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "scout";
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
          promptFile = null;
        }
      ];
    in
    assert assertMsg (result.ok == false)
      "normalizeRosterResult must not accept an entry whose promptFile isn't a string";
    assert assertMsg (result.violation == "invalid-promptfile-type")
      "normalizeRosterResult must report violation == \"invalid-promptfile-type\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "scout")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    # Issue #2571 blocking review finding: see
    # roster-normalize-rejects-invalid-name above for rationale.
    assert assertMsg (hasInfix "scout" result.message)
      "normalizeRosterResult's message must name the offending entry \"scout\", got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "promptFile" result.message)
      "normalizeRosterResult's message must describe the problem (\"promptFile\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-non-string-promptfile" { } "touch $out";

  # Issue #2571 non-blocking review finding (AC1 gap): see
  # roster-normalize-throws-on-invalid-name above for rationale.
  roster-normalize-throws-on-invalid-promptfile-type =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
              promptFile = null;
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success)
      "normalizeRoster must throw when promptFile isn't a non-empty string";
    pkgs.runCommand "roster-normalize-throws-on-invalid-promptfile-type" { } "touch $out";

  # Issue #2571 tied non-blocking finding (issue #2555 user story 23):
  # promptFile = "" resolves to templates/default/prompts/ itself (the
  # directory exists), so builtins.pathExists alone would wrongly bless it as
  # "found". Must be rejected the same as any other non-usable promptFile
  # value, closing the sibling hole to prompt = "" (see hasInlinePrompt
  # below, which already requires non-empty).
  roster-normalize-rejects-empty-string-promptfile =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "scout";
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
          promptFile = "";
        }
      ];
    in
    assert assertMsg (result.ok == false)
      "normalizeRosterResult must not accept an entry with promptFile == \"\"";
    assert assertMsg (result.violation == "invalid-promptfile-type")
      "normalizeRosterResult must report violation == \"invalid-promptfile-type\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "scout")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    pkgs.runCommand "roster-normalize-rejects-empty-string-promptfile" { } "touch $out";

  # Issue #2571 round-3 review finding: prompt = 5 (or any non-null,
  # non-string value) currently passes normalizeRosterResult silently and
  # only fails later inside writeText deep in the Driver pipeline, far from
  # the entry that caused it -- must fail with a clean, named
  # "invalid-prompt-type" violation instead.
  roster-normalize-rejects-non-string-prompt =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "scout";
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
          prompt = 5;
        }
      ];
    in
    assert assertMsg (result.ok == false)
      "normalizeRosterResult must not accept an entry whose prompt isn't a string or null";
    assert assertMsg (result.violation == "invalid-prompt-type")
      "normalizeRosterResult must report violation == \"invalid-prompt-type\", got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "scout")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    assert assertMsg (hasInfix "scout" result.message)
      "normalizeRosterResult's message must name the offending entry \"scout\", got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "prompt" result.message)
      "normalizeRosterResult's message must describe the problem (\"prompt\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-non-string-prompt" { } "touch $out";

  # Issue #2571 non-blocking review finding (AC1 gap): see
  # roster-normalize-throws-on-invalid-name above for rationale.
  roster-normalize-throws-on-non-string-prompt =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
              prompt = 5;
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success)
      "normalizeRoster must throw on an entry whose prompt isn't a string or null";
    pkgs.runCommand "roster-normalize-throws-on-non-string-prompt" { } "touch $out";

  # Issue #2571 review fix (cheap non-blocking finding): an empty inline
  # `prompt = ""` must not satisfy the promptFile-existence escape hatch --
  # it's not a usable prompt, the same "agent runs with no usable prompt"
  # failure mode issue #2555 user story 23 is about. Must fall through to
  # requiring a real promptFile the same as omitting `prompt` entirely.
  roster-normalize-rejects-empty-inline-prompt =
    let
      result = rosterLib.normalizeRosterResult [
        {
          name = "nonesuch";
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
          prompt = "";
        }
      ];
    in
    assert assertMsg (result.ok == false)
      "normalizeRosterResult must not accept an entry with an empty inline prompt and no resolvable promptFile";
    assert assertMsg (result.violation == "missing-promptfile")
      "normalizeRosterResult must report violation == \"missing-promptfile\" for an empty inline prompt, got: ${builtins.toJSON result.violation}";
    assert assertMsg (result.entryName == "nonesuch")
      "normalizeRosterResult must name the offending entry, got: ${builtins.toJSON result.entryName}";
    # Issue #2571 blocking review finding: pin message content for the
    # "missing-promptfile" violation class too -- see
    # roster-normalize-rejects-invalid-name above for rationale.
    assert assertMsg (hasInfix "nonesuch" result.message)
      "normalizeRosterResult's message must name the offending entry \"nonesuch\", got: ${builtins.toJSON result.message}";
    assert assertMsg (hasInfix "does not exist" result.message)
      "normalizeRosterResult's message must describe the problem (\"does not exist\"), got: ${builtins.toJSON result.message}";
    pkgs.runCommand "roster-normalize-rejects-empty-inline-prompt" { } "touch $out";

  # Issue #2571 review fix (Finding C): "reviewer" is the one canonical
  # agent name whose injected promptFile default deliberately doesn't
  # follow the "<name>-prompt.md" convention -- its on-disk template is
  # templates/default/prompts/review-prompt.md. An entry named "reviewer"
  # omitting both promptFile and prompt must not throw, and must resolve to
  # review-prompt.md specifically (not the naive reviewer-prompt.md, which
  # doesn't exist on disk).
  roster-normalize-injects-reviewer-promptfile-override =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "reviewer";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success)
      "normalizeRoster must not throw on a reviewer entry omitting promptFile, got failure evaluating: ${builtins.toJSON result}";
    assert assertMsg ((builtins.elemAt result.value 0).promptFile == "review-prompt.md")
      "normalizeRoster must inject promptFile == \"review-prompt.md\" for the reviewer name (not reviewer-prompt.md), got: ${builtins.toJSON (builtins.elemAt result.value 0).promptFile}";
    pkgs.runCommand "roster-normalize-injects-reviewer-promptfile-override" { } "touch $out";

  # Issue #2571 slice 2: the other half of the same contract -- an entry
  # with a nonexistent promptFile but a non-null inline `prompt` must not
  # throw.
  roster-normalize-accepts-inline-prompt-without-promptfile-file =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "nonesuch";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
              prompt = "some inline prompt text";
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success)
      "normalizeRoster must not throw on an entry with a nonexistent promptFile when an inline prompt is supplied";
    pkgs.runCommand "roster-normalize-accepts-inline-prompt-without-promptfile-file" { } "touch $out";

  roster-normalize-injects-promptfile-default =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success) "normalizeRoster must not throw on an entry omitting promptFile";
    assert assertMsg ((builtins.elemAt result.value 0).promptFile == "scout-prompt.md")
      "normalizeRoster must inject promptFile as <name>-prompt.md when omitted, got: ${builtins.toJSON result.value}";
    pkgs.runCommand "roster-normalize-injects-promptfile-default" { } "touch $out";

  roster-normalize-preserves-promptfile =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [
            {
              name = "scout";
              model = "m";
              mode = "subagent";
              description = "d";
              tools = [ ];
              promptFile = "custom-scout.md";
              prompt = "some prompt text";
            }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success
    ) "normalizeRoster must not throw on an entry with explicit promptFile";
    assert assertMsg (
      (builtins.elemAt result.value 0).promptFile == "custom-scout.md"
    ) "normalizeRoster must preserve an explicit promptFile, got: ${builtins.toJSON result.value}";
    pkgs.runCommand "roster-normalize-preserves-promptfile" { } "touch $out";

  roster-normalize-allows-empty =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.normalizeRoster [ ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success) "normalizeRoster must not throw on an empty roster";
    assert assertMsg (
      result.value == [ ]
    ) "normalizeRoster [] must return [], got: ${builtins.toJSON result.value}";
    pkgs.runCommand "roster-normalize-allows-empty" { } "touch $out";

  # Issue #2506: readSchemaDefaults' `strict` flag must actually discriminate
  # -- `strict = true` throws on an entry missing `.default` (the contract
  # the roster's four schemaDefaults callers depend on). Without this
  # fixture, a reader that ignored `strict` and always fell back to `or ""`
  # would still pass every other check in the repo.
  roster-schema-defaults-strict-throws-on-missing-default =
    let
      helper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
      result = builtins.tryEval (
        let
          r = helper.readSchemaDefaults { strict = true; } { missing = { }; };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      !result.success
    ) "readSchemaDefaults { strict = true; } must throw on an entry missing .default";
    pkgs.runCommand "roster-schema-defaults-strict-throws-on-missing-default" { } "touch $out";

  # Issue #2506: the other half of the same contract -- `strict = false`
  # must fall back to `""` on an entry missing `.default` instead of
  # throwing, the tolerance mkHarness's flakeOption sweep depends on since
  # most flakeOption-flagged schema entries carry no model concept at all.
  roster-schema-defaults-tolerant-falls-back-on-missing-default =
    let
      helper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
      result = builtins.tryEval (
        let
          r = helper.readSchemaDefaults { strict = false; } { missing = { }; };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (result.success
    ) "readSchemaDefaults { strict = false; } must not throw on an entry missing .default";
    assert assertMsg (result.value.missing == "")
      "readSchemaDefaults { strict = false; } must fall back to \"\" on an entry missing .default, got: ${builtins.toJSON result.value.missing}";
    pkgs.runCommand "roster-schema-defaults-tolerant-falls-back-on-missing-default" { } "touch $out";

  # Issue #2386: defaultRoster ships a fixed default `effort` per agent,
  # looked up per name from rosterDefaults (issue #2506) rather than a
  # literal on each entry.
  roster-default-roster-ships-effort-defaults =
    let
      roster = rosterLib.defaultRoster {
        scoutModel = "m";
        reviewModel = "m";
        filerModel = "m";
        workerModel = "m";
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
      expected =
        mapAttrs (_: v: v.effort)
          (import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; }).rosterDefaults;
      mismatches = builtins.filter (n: (byName n).effort != expected.${n}) [
        "scout"
        "reviewer"
        "filer"
        "worker"
      ];
    in
    assert assertMsg (mismatches == [ ])
      "defaultRoster must ship the fixed default effort per agent from rosterDefaults (expected: ${builtins.toJSON expected}), mismatched: ${builtins.toJSON mismatches}";
    pkgs.runCommand "roster-default-roster-ships-effort-defaults" { } "touch $out";

  # Issue #2426: defaultRoster's models attrset sets a named agent's model.
  # Issue #2434: every unmentioned agent instead inherits its
  # lib/env-schema.nix default (filer's own schema default stays empty, so
  # naming a different agent in `models` doesn't accidentally provision it).
  roster-default-roster-models-by-name =
    let
      roster = rosterLib.defaultRoster {
        models = {
          filer = defaultModelFixture.dogfoodPins.filer;
        };
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
      # Deliberate carve-out (issue #2506 AC5): reads the schema directly
      # rather than through readSchemaDefaults, so this pin can't go
      # vacuous by comparing the helper under test against itself.
      schema = import ../../lib/env-schema.nix;
    in
    assert assertMsg ((byName "filer").model == defaultModelFixture.dogfoodPins.filer)
      "defaultRoster models.filer must set the filer entry's model, got: ${builtins.toJSON (byName "filer").model}";
    assert assertMsg ((byName "scout").model == schema.scoutModel.default)
      "defaultRoster must inherit an unmentioned name's schema default, got: ${builtins.toJSON (byName "scout").model}";
    assert assertMsg ((byName "reviewer").model == schema.reviewModel.default)
      "defaultRoster must inherit an unmentioned name's schema default, got: ${builtins.toJSON (byName "reviewer").model}";
    assert assertMsg ((byName "worker").model == schema.workerModel.default)
      "defaultRoster must inherit an unmentioned name's schema default, got: ${builtins.toJSON (byName "worker").model}";
    pkgs.runCommand "roster-default-roster-models-by-name" { } "touch $out";

  roster-default-roster-rejects-unknown-model-name =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.defaultRoster {
            models = {
              typo-agent = "m";
            };
          };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      !result.success
    ) "defaultRoster must throw when models names an agent absent from the roster";
    pkgs.runCommand "roster-default-roster-rejects-unknown-model-name" { } "touch $out";

  # Issue #2426: when both the legacy per-agent knob and models name the same
  # agent, models wins -- the higher-precedence source, per lib/roster.nix's
  # modelFor.
  roster-default-roster-models-overrides-legacy =
    let
      roster = rosterLib.defaultRoster {
        filerModel = "legacy-model";
        models = {
          filer = "models-model";
        };
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
    in
    assert assertMsg ((byName "filer").model == "models-model")
      "defaultRoster models.filer must win over a same-named legacy filerModel, got: ${builtins.toJSON (byName "filer").model}";
    pkgs.runCommand "roster-default-roster-models-overrides-legacy" { } "touch $out";

  # Issue #2434: an explicitly supplied legacy positional argument still
  # wins over the schema default -- the sentinel-`null` default on
  # scoutModel/reviewModel/filerModel/workerModel only defers to the schema
  # when the caller truly supplied nothing, never when it supplied a value.
  roster-default-roster-legacy-wins-over-schema-default =
    let
      roster = rosterLib.defaultRoster { scoutModel = "explicit-legacy"; };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
    in
    assert assertMsg ((byName "scout").model == "explicit-legacy")
      "defaultRoster must let an explicitly supplied legacy scoutModel win over the schema default, got: ${builtins.toJSON (byName "scout").model}";
    pkgs.runCommand "roster-default-roster-legacy-wins-over-schema-default" { } "touch $out";

  # Issue #2434 (was #392): an explicit empty string on a legacy positional
  # knob is itself a supplied value, not "not supplied" -- it must keep
  # opting the entry out, the same rung mkHarness.nix's deprecated
  # settings.*Model resolution relies on, even though the name is now
  # eligible to inherit a non-empty schema default.
  roster-default-roster-legacy-explicit-empty-opts-out =
    let
      roster = rosterLib.defaultRoster { scoutModel = ""; };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
    in
    assert assertMsg ((byName "scout").model == "")
      "defaultRoster must let an explicit empty legacy scoutModel opt-out win over the schema default, got: ${builtins.toJSON (byName "scout").model}";
    pkgs.runCommand "roster-default-roster-legacy-explicit-empty-opts-out" { } "touch $out";

  # Issue #2434: models.<name> = "" is the explicit opt-out (#392) and must
  # keep dropping that entry's model even though the name is now eligible
  # to inherit a non-empty schema default.
  roster-default-roster-explicit-empty-opts-out =
    let
      roster = rosterLib.defaultRoster {
        models = {
          scout = "";
        };
      };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
    in
    assert assertMsg ((byName "scout").model == "")
      "defaultRoster must let an explicit models.scout = \"\" opt-out win over the schema default, got: ${builtins.toJSON (byName "scout").model}";
    pkgs.runCommand "roster-default-roster-explicit-empty-opts-out" { } "touch $out";

  # Issue #2434: an agent unmentioned in `models` and with no legacy
  # positional argument supplied inherits its model from
  # lib/env-schema.nix's default -- the same default mkHarness's no-roster
  # fallback resolves through `mergedDefaults`.
  roster-default-roster-inherits-schema-default =
    let
      roster = rosterLib.defaultRoster { };
      byName = name: builtins.head (builtins.filter (e: e.name == name) roster);
      # Deliberate carve-out (issue #2506 AC5), same rationale as
      # roster-default-roster-models-by-name above: reads the schema
      # directly instead of through readSchemaDefaults.
      schema = import ../../lib/env-schema.nix;
      expected = {
        scout = schema.scoutModel.default;
        reviewer = schema.reviewModel.default;
        filer = schema.filerModel.default;
        worker = schema.workerModel.default;
      };
      mismatches = builtins.filter (n: (byName n).model != expected.${n}) [
        "scout"
        "reviewer"
        "filer"
        "worker"
      ];
    in
    assert assertMsg (mismatches == [ ])
      "defaultRoster {} must inherit each unmentioned agent's model from lib/env-schema.nix's default, mismatched: ${builtins.toJSON mismatches}";
    pkgs.runCommand "roster-default-roster-inherits-schema-default" { } "touch $out";

  # Issue #2560: byName.<name>.model sets a named agent's model, the same as
  # `models.<name>` but nested under a per-agent map that can also carry
  # `effort` alongside `model`.
  roster-default-roster-by-name-sets-model =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          filer = {
            model = defaultModelFixture.dogfoodPins.filer;
          };
        };
      };
    in
    assert assertMsg (
      (entryFor "filer" roster).model == defaultModelFixture.dogfoodPins.filer
    ) "defaultRoster byName.filer.model must set the filer entry's model, got: ${builtins.toJSON (entryFor "filer" roster).model}";
    pkgs.runCommand "roster-default-roster-by-name-sets-model" { } "touch $out";

  # Issue #2560: byName.<name>.effort overrides that agent's default effort
  # (from rosterDefaults) without disturbing its model.
  roster-default-roster-by-name-sets-effort =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          reviewer = {
            effort = "high";
          };
        };
      };
      schema = import ../../lib/env-schema.nix;
    in
    assert assertMsg ((entryFor "reviewer" roster).effort == "high")
      "defaultRoster byName.reviewer.effort must override the reviewer entry's effort, got: ${builtins.toJSON (entryFor "reviewer" roster).effort}";
    assert assertMsg ((entryFor "reviewer" roster).model == schema.reviewModel.default)
      "defaultRoster byName.reviewer.effort must not disturb the reviewer entry's model, got: ${builtins.toJSON (entryFor "reviewer" roster).model}";
    pkgs.runCommand "roster-default-roster-by-name-sets-effort" { } "touch $out";

  # Issue #2560: models.<name> is the higher-precedence shorthand and must
  # win over byName.<name>.model when both name the same agent.
  roster-default-roster-models-overrides-by-name =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          filer = {
            model = "byname-model";
          };
        };
        models = {
          filer = "models-model";
        };
      };
    in
    assert assertMsg ((entryFor "filer" roster).model == "models-model")
      "defaultRoster models.filer must win over a same-named byName.filer.model, got: ${builtins.toJSON (entryFor "filer" roster).model}";
    pkgs.runCommand "roster-default-roster-models-overrides-by-name" { } "touch $out";

  # Issue #2560: byName.<name>.model wins over a same-named legacy positional
  # knob (e.g. filerModel) -- byName sits between models and the legacy
  # knobs in the precedence chain.
  roster-default-roster-by-name-overrides-legacy =
    let
      roster = rosterLib.defaultRoster {
        filerModel = "legacy-model";
        byName = {
          filer = {
            model = "byname-model";
          };
        };
      };
    in
    assert assertMsg ((entryFor "filer" roster).model == "byname-model")
      "defaultRoster byName.filer.model must win over a same-named legacy filerModel, got: ${builtins.toJSON (entryFor "filer" roster).model}";
    pkgs.runCommand "roster-default-roster-by-name-overrides-legacy" { } "touch $out";

  # Issue #2560: an unknown top-level key in byName must throw, mirroring
  # models' unknownNames guard.
  roster-default-roster-rejects-unknown-by-name-key =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.defaultRoster {
            byName = {
              typo-agent = {
                model = "m";
              };
            };
          };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      !result.success
    ) "defaultRoster must throw when byName names an agent absent from the roster";
    pkgs.runCommand "roster-default-roster-rejects-unknown-by-name-key" { } "touch $out";

  # Issue #2560: byName is a closed attrset -- only `model` and `effort` are
  # accepted per agent. mode/tools/prompt etc. must throw, since those stay
  # roster-only.
  roster-default-roster-rejects-unknown-by-name-field =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.defaultRoster {
            byName = {
              filer = {
                mode = "x";
              };
            };
          };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (
      !result.success
    ) "defaultRoster must throw when a byName.<name> value carries a field other than model/effort";
    pkgs.runCommand "roster-default-roster-rejects-unknown-by-name-field" { } "touch $out";

  # Issue #2560: byName.<name>.model = "" is the explicit opt-out (#392) and
  # must win over the schema default, mirroring
  # roster-default-roster-explicit-empty-opts-out for models.
  roster-default-roster-by-name-explicit-empty-opts-out =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          scout = {
            model = "";
          };
        };
      };
    in
    assert assertMsg ((entryFor "scout" roster).model == "")
      "defaultRoster must let an explicit byName.scout.model = \"\" opt-out win over the schema default, got: ${builtins.toJSON (entryFor "scout" roster).model}";
    pkgs.runCommand "roster-default-roster-by-name-explicit-empty-opts-out" { } "touch $out";

  # Issue #2560 (non-blocking review finding): unlike model = "", effort = ""
  # is not a documented opt-out -- it's accepted and silently overrides
  # defaultRoster's own per-agent default effort with an empty string
  # (docs/reference.md's "Subagent roster" section). Pins that it's accepted
  # (doesn't throw) and that it produces exactly "", not the schema default,
  # so a future change can't silently make it fall back to the default
  # instead.
  roster-default-roster-by-name-explicit-empty-effort-overrides-default =
    let
      roster = rosterLib.defaultRoster {
        byName = {
          reviewer = {
            effort = "";
          };
        };
      };
    in
    assert assertMsg ((entryFor "reviewer" roster).effort == "")
      "defaultRoster must let an explicit byName.reviewer.effort = \"\" override the schema default effort with an empty string (not fall back to it), got: ${builtins.toJSON (entryFor "reviewer" roster).effort}";
    pkgs.runCommand "roster-default-roster-by-name-explicit-empty-effort-overrides-default" { }
      "touch $out";

  # Issue #2560 (non-blocking review finding): defaultRoster's unknown-field
  # scan calls builtins.attrNames on each byName.<name> value. On the raw
  # rosterLib/mkHarness call path (unlike the flakeModule path, which is
  # type-guarded by a types.submodule option), nothing stops a caller from
  # passing a non-attrset there, e.g. byName.filer = "oops". Pins that this
  # throws (rather than propagating whatever error builtins.attrNames itself
  # produces) so a future refactor can't silently make this path stop
  # throwing entirely.
  roster-default-roster-by-name-non-attrset-value-throws =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.defaultRoster {
            byName.filer = "oops";
          };
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success)
      "defaultRoster must throw when byName.<name> is not an attribute set (e.g. byName.filer = \"oops\")";
    pkgs.runCommand "roster-default-roster-by-name-non-attrset-value-throws" { } "touch $out";

  # Issue #2571 review fix: rosterLib.dropOptedOut drops only the entry
  # whose model is the explicit "" opt-out sentinel (#392), leaving any
  # other entry (regardless of model value) untouched.
  roster-drop-opted-out-drops-only-empty-model =
    let
      normalized = rosterLib.normalizeRoster [
        {
          name = "a";
          model = "";
          mode = "subagent";
          description = "d";
          tools = [ ];
          prompt = "some prompt text";
        }
        {
          name = "b";
          model = "m";
          mode = "subagent";
          description = "d";
          tools = [ ];
          prompt = "some prompt text";
        }
      ];
      result = rosterLib.dropOptedOut normalized;
    in
    assert assertMsg (builtins.length result == 1)
      "dropOptedOut must drop only the entry with an explicit empty model, got: ${builtins.toJSON result}";
    assert assertMsg ((builtins.elemAt result 0).name == "b")
      "dropOptedOut must retain the entry with a non-empty model, got: ${builtins.toJSON result}";
    pkgs.runCommand "roster-drop-opted-out-drops-only-empty-model" { } "touch $out";

  # Issue #2571 review fix: the identity case -- dropOptedOut on a roster
  # with no opted-out entries returns it unchanged.
  roster-drop-opted-out-identity-when-none-opted-out =
    let
      normalized = rosterLib.normalizeRoster [
        {
          name = "a";
          model = "m1";
          mode = "subagent";
          description = "d";
          tools = [ ];
          prompt = "some prompt text";
        }
        {
          name = "b";
          model = "m2";
          mode = "subagent";
          description = "d";
          tools = [ ];
          prompt = "some prompt text";
        }
      ];
      result = rosterLib.dropOptedOut normalized;
    in
    assert assertMsg (result == normalized)
      "dropOptedOut must return the roster unchanged when no entry is opted out, got: ${builtins.toJSON result}";
    pkgs.runCommand "roster-drop-opted-out-identity-when-none-opted-out" { } "touch $out";

  # Issue #2571 review fix: dropOptedOut is exported directly on the
  # versioned rosterLib surface (flake.nix), so a Consumer can call it
  # standalone on a hand-built roster that skipped normalizeRoster. An
  # entry missing `model` must fail with a guard naming the entry (rather
  # than Nix's bare, unhelpful "attribute 'model' missing") -- but
  # builtins.tryEval can only prove *that* eval aborted, never recover the
  # thrown message text (same caveat as the normalizeRoster throw checks
  # above), so this only pins the throw itself.
  roster-drop-opted-out-rejects-missing-model =
    let
      result = builtins.tryEval (
        let
          r = rosterLib.dropOptedOut [
            { name = "x"; }
          ];
        in
        builtins.deepSeq r r
      );
    in
    assert assertMsg (!result.success)
      "dropOptedOut must throw a guard error on an entry missing model, not silently succeed";
    pkgs.runCommand "roster-drop-opted-out-rejects-missing-model" { } "touch $out";

  # Issue #2437: lib/roster-schema-defaults.nix is the single source of
  # truth for defaultRoster's roster-name -> schema-key model defaults.
  # Pin its schemaDefaults output directly against lib/env-schema.nix's
  # four current defaults so the two can never silently drift. `expected`
  # below mirrors roster-default-roster-inherits-schema-default's mapping
  # on purpose: that check pins defaultRoster's output, this one pins the
  # helper's output one level down.
  roster-schema-defaults-helper-matches-env-schema =
    let
      helper = import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; };
      # Same carve-out as above: a direct schema read, not through
      # readSchemaDefaults, so this pin can't go vacuous.
      schema = import ../../lib/env-schema.nix;
      expected = {
        scout = schema.scoutModel.default;
        reviewer = schema.reviewModel.default;
        filer = schema.filerModel.default;
        worker = schema.workerModel.default;
      };
      mismatches = builtins.filter (n: helper.schemaDefaults.${n} != expected.${n}) (
        builtins.attrNames helper.rosterModelKeys
      );
    in
    assert assertMsg (mismatches == [ ])
      "lib/roster-schema-defaults.nix schemaDefaults must match lib/env-schema.nix's four current defaults, mismatched: ${builtins.toJSON mismatches}";
    pkgs.runCommand "roster-schema-defaults-helper-matches-env-schema" { } "touch $out";
}
