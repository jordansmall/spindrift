# Pins that the code the opencode guard plugin and the pi hook bridge repeat
# verbatim stays identical (issue #6455): the two timeout constants, requireJq
# and runHook. Their "Twin:" comments name the pieces; the HOOKS-loop decision
# handling and the summary call differ by host API, so they stay on the comment
# alone.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    drop
    findFirst
    hasPrefix
    replaceStrings
    splitString
    sublist
    ;
  inherit (pkgs.lib.lists) findFirstIndex;

  load = file: text: {
    inherit file text;
    lines = splitString "\n" text;
  };
  read = file: load file (builtins.readFile (../.. + "/${file}"));
  real = {
    opencode = read "agent/opencode-guards.js";
    pi = read "agent/pi-hook-bridge.js";
  };

  # Pinned by name, not value, so a lockstep bump in both files passes.
  consts = [
    "GUARD_TIMEOUT_MS"
    "SUMMARY_TIMEOUT_MS"
  ];
  functions = [
    "requireJq"
    "runHook"
  ];

  # From the `function <name>(` line through the first column-one `}`. Throws
  # on a missing header rather than yielding an empty span, which two files
  # lacking the function would otherwise share.
  spanOf =
    { file, lines, ... }:
    name:
    let
      start = findFirstIndex (hasPrefix "function ${name}(") null lines;
      rest = drop start lines;
      end = findFirstIndex (l: l == "}") null rest;
    in
    assert assertMsg (start != null) "${file}: no `function ${name}(` line";
    assert assertMsg (end != null) "${file}: `function ${name}(` has no closing column-one `}`";
    concatStringsSep "\n" (sublist 0 (end + 1) rest);

  constLine =
    { file, lines, ... }:
    name:
    let
      line = findFirst (hasPrefix "const ${name} =") null lines;
    in
    assert assertMsg (line != null) "${file}: no `const ${name} =` line";
    line;

  differs =
    { opencode, pi }:
    name:
    "`${name}` differs between ${opencode.file} and ${pi.file}; they are twins (see the \"Twin:\" comment), so change both";

  # Takes the loaded files as an argument so the -regression check can feed
  # doctored copies.
  assertParity =
    { opencode, pi }@files:
    [
      (map (
        name:
        assert assertMsg (constLine opencode name == constLine pi name) (differs files name);
        name
      ) consts)
      (map (
        name:
        assert assertMsg (spanOf opencode name == spanOf pi name) (differs files name);
        name
      ) functions)
    ];
in
{
  guard-runner-twin-parity = builtins.seq (builtins.deepSeq (assertParity real) null) (
    pkgs.runCommand "guard-runner-twin-parity" { } "touch $out"
  );

  guard-runner-twin-parity-regression =
    let
      # Doctors the pi twin only; each fixture must change the text, or it
      # would pass vacuously.
      fixtures = {
        "a drifted runHook body" = [
          "encoding: \"utf8\""
          "encoding: \"latin1\""
        ];
        "a drifted const value in one file" = [
          "const GUARD_TIMEOUT_MS ="
          "const GUARD_TIMEOUT_MS = 1 +"
        ];
        "a renamed requireJq (missing function)" = [
          "function requireJq("
          "function requireJqRenamed("
        ];
        "a removed const line" = [
          (constLine real.pi "SUMMARY_TIMEOUT_MS")
          ""
        ];
      };
      check =
        name:
        let
          from = builtins.elemAt fixtures.${name} 0;
          to = builtins.elemAt fixtures.${name} 1;
          doctored = load real.pi.file (replaceStrings [ from ] [ to ] real.pi.text);
        in
        assert assertMsg (
          doctored.text != real.pi.text
        ) "guard-runner-twin-parity-regression: the fixture for ${name} left ${real.pi.file} unchanged";
        assert assertMsg
          (
            !(builtins.tryEval (
              builtins.deepSeq (assertParity {
                inherit (real) opencode;
                pi = doctored;
              }) null
            )).success
          )
          "guard-runner-twin-parity-regression: expected assertParity to reject ${name}, but it evaluated successfully";
        true;
    in
    assert builtins.all check (builtins.attrNames fixtures);
    pkgs.runCommand "guard-runner-twin-parity-regression" { } "touch $out";
}
