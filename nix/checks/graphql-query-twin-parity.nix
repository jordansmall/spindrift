# Pins that the SDK fake host's graphQLQuery stays the same code as the
# launcher's (issue #7566): the fake host is what the conformance suite runs
# on, so a strictness fix made to one copy alone would go unnoticed. The
# function body and the key-echo bound must be textually identical; their doc
# comments name each other as twins and may differ.
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
    host = read "cmd/launcher/internal/connectorhost/network.go";
    sdk = read "sdk/connector/fakehost.go";
  };

  # From the `func graphQLQuery(` line through the first column-one `}`.
  # Throws on a missing header rather than yielding an empty span, which two
  # files lacking the function would otherwise share.
  spanOf =
    { file, lines, ... }:
    let
      start = findFirstIndex (hasPrefix "func graphQLQuery(") null lines;
      rest = drop start lines;
      end = findFirstIndex (l: l == "}") null rest;
    in
    assert assertMsg (start != null) "${file}: no `func graphQLQuery(` line";
    assert assertMsg (end != null) "${file}: `func graphQLQuery(` has no closing column-one `}`";
    concatStringsSep "\n" (sublist 0 (end + 1) rest);

  constLine =
    { file, lines, ... }:
    let
      line = findFirst (hasPrefix "const maxBodyKeyEcho =") null lines;
    in
    assert assertMsg (line != null) "${file}: no `const maxBodyKeyEcho =` line";
    line;

  differs =
    { host, sdk }:
    what:
    "${what} differs between ${host.file} and ${sdk.file}; they are twins (see the \"Twin:\" comment), so change both";

  # Takes the loaded files as an argument so the -regression check can feed
  # doctored copies.
  assertParity =
    { host, sdk }@files:
    [
      (assert assertMsg (spanOf host == spanOf sdk) (differs files "`graphQLQuery`"); null)
      (assert assertMsg (constLine host == constLine sdk) (differs files "`maxBodyKeyEcho`"); null)
    ];
in
{
  graphql-query-twin-parity = builtins.seq (builtins.deepSeq (assertParity real) null) (
    pkgs.runCommand "graphql-query-twin-parity" { } "touch $out"
  );

  graphql-query-twin-parity-regression =
    let
      # Doctors the SDK twin only; each fixture must change the text, or it
      # would pass vacuously.
      fixtures = {
        "a drifted strictness rule" = [
          "is duplicated"
          "is repeated"
        ];
        "a drifted echo bound" = [
          "const maxBodyKeyEcho = 256"
          "const maxBodyKeyEcho = 255"
        ];
        "a renamed graphQLQuery (missing function)" = [
          "func graphQLQuery("
          "func graphQLQueryRenamed("
        ];
      };
      check =
        name:
        let
          from = builtins.elemAt fixtures.${name} 0;
          to = builtins.elemAt fixtures.${name} 1;
          doctored = load real.sdk.file (replaceStrings [ from ] [ to ] real.sdk.text);
        in
        assert assertMsg (
          doctored.text != real.sdk.text
        ) "graphql-query-twin-parity-regression: the fixture for ${name} left ${real.sdk.file} unchanged";
        assert assertMsg
          (
            !(builtins.tryEval (
              builtins.deepSeq (assertParity {
                inherit (real) host;
                sdk = doctored;
              }) null
            )).success
          )
          "graphql-query-twin-parity-regression: expected assertParity to reject ${name}, but it evaluated successfully";
        true;
    in
    assert builtins.all check (builtins.attrNames fixtures);
    pkgs.runCommand "graphql-query-twin-parity-regression" { } "touch $out";
}
