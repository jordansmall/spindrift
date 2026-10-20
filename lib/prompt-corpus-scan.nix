# Per-file scan results for the default prompt corpus (templates/default/prompts:
# the shared and kind prompts, the fragments tree and the chores directory).
# Every harness instance reads the identical default files; this module takes
# no arguments, so Nix's per-file import cache hands every instance the same
# value with no key, and each file is read and scanned at most once per
# evaluation (the attrsets below are lazy per file). A source copied to a
# re-rooted store path re-evaluates it naturally. Builtins only, like
# lib/prompt-contract.nix, so it needs no nixpkgs.
#
# A consumer compares its input against `text` (a prompt arg) or against the
# exported default dir (a fragments or chores directory): equal means the cached
# result is the answer, otherwise it calls `scan` on the overridden text itself.
let
  builtinsCompat = import ./builtins-compat.nix;
  promptContract = import ./prompt-contract.nix;

  promptsDir = ../templates/default/prompts;
  fragmentsDir = promptsDir + "/fragments";
  choresDir = promptsDir + "/chores";

  # `forbidden` is the lib/prompt-contract.nix forbiddenMarkers substring rows
  # the text contains, in row order.
  scan = text: {
    inherit text;
    # Not lib.hasInfix: its `.*X.*` regex recurses per character under a
    # std::regex Nix (CI's) and overflows the evaluator's stack on a whole
    # prompt. Same split idiom as lib/prompt-contract.nix's
    # hasDirectPlaceholder, braces in a character class. One split per source
    # keeps eval cost flat in the registry's size. The name class excludes `$`,
    # `{` and `}` so `${${TDD_STEP}}` still captures TDD_STEP; a registry key
    # outside the class is never matched, so keys must fit it.
    vars = builtins.listToAttrs (
      map (m: {
        name = builtins.head m;
        value = true;
      }) (builtins.filter builtins.isList (builtins.split "\\$[{]([A-Za-z0-9_]+)[}]" text))
    );
    forbidden = promptContract.forbiddenMarkersIn text;
  };

  scanFile = file: scan (builtins.readFile file);

  isMarkdown = name: builtinsCompat.hasSuffix ".md" name;

  # The top-level non-directory *.md files of a directory, by file name.
  topLevelMarkdown =
    dir:
    let
      entries = builtins.readDir dir;
    in
    builtins.listToAttrs (
      map (name: {
        inherit name;
        value = scanFile (dir + "/${name}");
      }) (builtins.filter (n: entries.${n} != "directory" && isMarkdown n) (builtins.attrNames entries))
    );

  # Every regular *.md file under the fragments tree, keyed by its path
  # relative to the root ("auto-format.md", "github/open-pr-create.md"), the
  # spelling lib/fragment-paths.nix's scanPathsOf produces.
  fragmentFiles =
    let
      walk =
        rel:
        let
          entries = builtins.readDir (if rel == "" then fragmentsDir else fragmentsDir + "/${rel}");
        in
        builtins.concatMap (
          n:
          let
            path = if rel == "" then n else "${rel}/${n}";
          in
          if entries.${n} == "directory" then
            walk path
          else if entries.${n} == "regular" && isMarkdown n then
            [ path ]
          else
            [ ]
        ) (builtins.attrNames entries);
    in
    builtins.listToAttrs (
      map (path: {
        name = path;
        value = scanFile (fragmentsDir + "/${path}");
      }) (walk "")
    );
in
{
  inherit scan fragmentsDir choresDir;
  # An overridden chores directory scans through the same filter.
  scanDir = topLevelMarkdown;
  prompts = topLevelMarkdown promptsDir;
  chores = topLevelMarkdown choresDir;
  fragments = fragmentFiles;
}
