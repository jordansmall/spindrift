# Pins that the SDK fake host's graphQLQuery and matchDocument stay the same
# code as the launcher's (issue #7566): the fake host is what the conformance
# suite runs on, so a strictness fix made to one copy alone would go
# unnoticed. The function bodies and the key-echo bound must be textually
# identical, and both files must carry the documents call site's refusal
# strings, which no function body holds; their doc comments name each other as
# twins and may differ.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    drop
    findFirst
    hasInfix
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

  # From the `func <name>(` line through the first column-one `}`.
  # Throws on a missing header rather than yielding an empty span, which two
  # files lacking the function would otherwise share.
  spanOf =
    name:
    { file, lines, ... }:
    let
      start = findFirstIndex (hasPrefix "func ${name}(") null lines;
      rest = drop start lines;
      end = findFirstIndex (l: l == "}") null rest;
    in
    assert assertMsg (start != null) "${file}: no `func ${name}(` line";
    assert assertMsg (end != null) "${file}: `func ${name}(` has no closing column-one `}`";
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

  # The documents call site's refusal formats, as the Go literal fragments
  # that end each one; the call site is not a function of its own to span.
  refusalStrings = [
    ": query is not a document the route declares\""
    ": a route that declares documents admits no query string\""
  ];

  # Takes the loaded files as an argument so the -regression check can feed
  # doctored copies.
  assertParity =
    { host, sdk }@files:
    [
      (
        assert assertMsg (spanOf "graphQLQuery" host == spanOf "graphQLQuery" sdk) (
          differs files "`graphQLQuery`"
        );
        null
      )
      (
        assert assertMsg (spanOf "matchDocument" host == spanOf "matchDocument" sdk) (
          differs files "`matchDocument`"
        );
        null
      )
      (
        assert assertMsg (constLine host == constLine sdk) (differs files "`maxBodyKeyEcho`");
        null
      )
    ]
    ++ map (
      s:
      (
        assert assertMsg (hasInfix s host.text && hasInfix s sdk.text) (
          "the documents refusal string ${s} is missing from ${host.file} or ${sdk.file}; they are twins (see the \"Twin:\" comment), so change both"
        );
        null
      )
    ) refusalStrings;
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
        "a drifted matchDocument rule" = [
          "if docs[name] == query {"
          "if strings.HasPrefix(docs[name], query) {"
        ];
        "a drifted undeclared-document refusal" = [
          ": query is not a document the route declares\""
          ": query is not a declared document\""
        ];
        "a drifted query-string refusal" = [
          ": a route that declares documents admits no query string\""
          ": a documents route admits no query string\""
        ];
        "a renamed matchDocument (missing function)" = [
          "func matchDocument("
          "func matchDocumentRenamed("
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
