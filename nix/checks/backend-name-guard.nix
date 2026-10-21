# Fails when core code compares a backend-name literal (`== "github"`,
# `"jira" !=`, `builtins.elem x [ "github" ]`, ...), ADR 0068 / issue #5645:
# behavior varies through the row's facts, never a name test.
# The two Go modules are scanned by their AST tests
# (cmd/launcher/backend_name_guard_test.go, dashboard/backend_name_guard_test.go);
# the Go outside them (.github/scripts) is line-scanned here, with _test.go and
# generated files skipped.
# Pure eval regex, so it is a tripwire, not a proof: it misses a comparison
# split across lines, a named list of names, a literal needle
# (`elem "github" names`), a concatenated list (`elem t ([ "github" ] ++ xs)`),
# and a membership test whose subject nests parens (`elem (f (x)) [ "github" ]`).
# It errs closed on `if elem x xs then [ "github" ]`,
# a hit though no list holds the needle. Comment lines are skipped.
{ pkgs, ... }:
let
  inherit (pkgs) lib;
  root = toString ../..;
  names = map (row: row.name) (import ../../lib/backends/default.nix);
  alt = lib.concatStringsSep "|" names;
  comparison = ".*((==|!=) *\"(${alt})\"|\"(${alt})\" *(==|!=)).*";
  # `elem x [ "github" ... ]` (builtins., lib.): a name-literal membership test.
  # Matched across lines, since nixfmt puts each item of a longer list on its own.
  # The gap before `[` steps over an unnested paren group, so `elem (x or "github") [`
  # is a hit, but not a lone paren, so `optionals (elem x xs) [ ... ]` is no hit.
  membershipSpan = "(^|[^a-zA-Z0-9_])(elem[ \t\n]+(\\([^()]*\\)|[^[;()])*\\[[^]]*])";
  oneLine =
    s:
    lib.concatStringsSep " " (lib.filter (w: lib.isString w && w != "") (builtins.split "[ \t\n]+" s));
  # A line-leading `//` is Nix's attrset update, not a comment: only Go skips it.
  commentLine = f: if lib.hasSuffix ".go" f then "[ \t]*//.*" else "[ \t]*#.*";

  hitLines =
    f: text:
    let
      code = builtins.filter (l: builtins.isString l && builtins.match (commentLine f) l == null) (
        builtins.split "\n" text
      );
      spans = map (m: oneLine (builtins.elemAt m 1)) (
        builtins.filter builtins.isList (builtins.split membershipSpan (lib.concatStringsSep "\n" code))
      );
    in
    builtins.filter (l: builtins.match comparison l != null) code
    ++ builtins.filter (s: builtins.match ".*\"(${alt})\".*" s != null) spans;

  exempt = map toString [ ../../lib/backends ];
  self = toString ./backend-name-guard.nix;
  generatedGo =
    text:
    builtins.any (
      l: builtins.isString l && builtins.match "// Code generated .* DO NOT EDIT\\." l != null
    ) (builtins.split "\n" text);
  inScope =
    f:
    f != self
    && !builtins.any (e: lib.hasPrefix "${e}/" f) exempt
    && (lib.hasSuffix ".nix" f || (lib.hasSuffix ".go" f && !lib.hasSuffix "_test.go" f));
  # Each file is read once: `text` is lazy, so a path filtered out is never read.
  sources = builtins.filter (s: !(lib.hasSuffix ".go" s.f && generatedGo s.text)) (
    map
      (f: {
        inherit f;
        text = builtins.readFile f;
      })
      (
        builtins.filter inScope (
          map toString (
            lib.filesystem.listFilesRecursive ../../lib
            ++ lib.filesystem.listFilesRecursive ../../nix
            ++ lib.filesystem.listFilesRecursive ../../.github/scripts
            ++ [
              ../../flake.nix
              ../../templates/default/flake.nix
            ]
          )
        )
      )
  );
  files = map (s: s.f) sources;
  relative = f: lib.removePrefix "${root}/" f;
  hits = builtins.concatMap (
    s: map (l: "${relative s.f}: ${lib.strings.trim l}") (hitLines s.f s.text)
  ) sources;

  # Files that must be in the walk: if the tree moves, the guard goes blind
  # unless one of these notices.
  mustScan = [
    "lib/mkHarness.nix"
    "nix/checks/go.nix"
    ".github/scripts/ci-go-tokens.go"
  ];

  # Each case is scanned alone; `hit` is the verdict the scanner must reach.
  fixtureCases = {
    a = {
      hit = true;
      text = ''
        a = if tracker == "github" then 1 else 2;
      '';
    };
    b = {
      hit = true;
      text = ''
        b = "jira" != tracker;
      '';
    };
    c = {
      hit = true;
      text = ''
        c = if kind == "local" then 1 else 2;
      '';
    };
    d = {
      hit = false; # a comment line
      text = ''
        # d = tracker == "git"
      '';
    };
    e = {
      hit = false; # no name beside the ==
      text = ''
        e = if signal == "GH" then "github" else "x";
      '';
    };
    f = {
      hit = false; # an assignment, not a comparison
      text = ''
        f = default = "forgejo";
      '';
    };
    g = {
      hit = true; # a list split across lines
      text = ''
        g = builtins.elem name [
          "local"
          "forgejo"
        ];
      '';
    };
    h = {
      hit = false; # a list with no backend name
      text = ''
        h = lib.elem x [ "other" ];
      '';
    };
    i = {
      hit = false; # a literal needle, a known miss
      text = ''
        i = builtins.elem "github" names;
      '';
    };
    j = {
      hit = false; # a lone paren group is not the elem's subject
      text = ''
        j = lib.optionals (lib.elem x xs) [
          "github"
        ];
      '';
    };
    k = {
      hit = true; # a paren group in the subject
      text = ''
        k = builtins.elem (mergedDefaults.issueTracker or "github") [
          "github"
          "forgejo"
        ];
      '';
    };
    l = {
      hit = true; # a line-leading `//` is Nix's update, not a comment
      text = ''
        // lib.optionalAttrs (tracker == "github") { }
      '';
    };
  };
  fixtureHits = lib.attrNames (
    lib.filterAttrs (_: c: hitLines "fixture.nix" c.text != [ ]) fixtureCases
  );
  fixtureExpected = lib.attrNames (lib.filterAttrs (_: c: c.hit) fixtureCases);

  goFixture = ''
    // if tracker == "github" {
    if tracker == "jira" {
  '';

  # Failures are build failures, not eval throws, so a regression fails this
  # check alone instead of the whole `checks` evaluation.
  problems =
    lib.optional (fixtureHits != fixtureExpected)
      "fixture cases that hit: expected ${builtins.toJSON fixtureExpected}, got ${builtins.toJSON fixtureHits}"
    ++ lib.optional (
      builtins.length (hitLines "fixture.go" goFixture) != 1
    ) "Go scan is inert or over-broad: ${builtins.toJSON (hitLines "fixture.go" goFixture)}"
    ++ map (f: "${f} is not scanned: the walk is matching the wrong tree") (
      builtins.filter (f: !builtins.elem f (map relative files)) mustScan
    )
    ++ map (h: "core code compares a backend name: ${h}") hits;
in
{
  backend-name-guard = pkgs.runCommand "backend-name-guard" { } ''
    ${lib.optionalString (problems != [ ]) ''
      cat >&2 ${pkgs.writeText "backend-name-guard-problems" (lib.concatStringsSep "\n" problems)}
      exit 1
    ''}
    touch $out
  '';
}
