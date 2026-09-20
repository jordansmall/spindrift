# Issue #3166 consolidated the scoutProvisioned rationale onto one canonical
# comment in lib/mkHarness.nix; the in-repo sites that point at it instead of
# restating it are listed in `pointerSites` below. docs/reference.md keeps a
# standalone prose copy deliberately -- a docs reader has no repo to follow a
# pointer into (#2762) -- so it is neither a pointer site nor guarded here.
# Nothing guarded the arrangement before this check: a trim of the canonical
# comment, or a reword at any one site, would silently orphan a pointer.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    hasInfix
    ;

  # Line-by-line rather than a whole-file hasInfix: lib.hasInfix's regex engine
  # (".*infix.*" via builtins.match) stack-overflows on cmd/launcher/main_test.go,
  # the one file among these sites large enough to trip it; every line on its
  # own is short.
  linesOf = path: builtins.filter builtins.isString (builtins.split "\n" (builtins.readFile path));

  # The anchor is the first code line below a site's comment block. Requiring
  # exactly one match keeps the block this check reads pinned to the site the
  # row names, rather than to whichever duplicate came first.
  anchorIdx =
    { file, anchor, ... }:
    lines:
    let
      hits = builtins.filter (i: hasInfix anchor (builtins.elemAt lines i)) (
        builtins.genList (i: i) (builtins.length lines)
      );
    in
    if hits == [ ] then
      throw "nix/checks/scout-rationale-parity.nix: no line matching \"${anchor}\" found in ${file} -- has the comment's anchor moved or been renamed?"
    else if builtins.length hits > 1 then
      throw "nix/checks/scout-rationale-parity.nix: found ${toString (builtins.length hits)} lines matching \"${anchor}\" in ${file} -- this check can no longer tell which one the comment sits above."
    else
      builtins.head hits;

  # Walks upward from just above the anchor, collecting the contiguous run of
  # comment lines and stopping at the first non-comment line (or the top of the
  # file).
  commentsAbove =
    prefix: lines: idx:
    let
      isCommentLine = l: builtins.match "[[:space:]]*${prefix}.*" l != null;
      go =
        i: acc:
        if i < 0 then
          acc
        else
          let
            l = builtins.elemAt lines i;
          in
          if isCommentLine l then go (i - 1) ([ l ] ++ acc) else acc;
    in
    go (idx - 1) [ ];

  canonicalSite = {
    file = "lib/mkHarness.nix";
    path = ../../lib/mkHarness.nix;
    prefix = "#";
    anchor = "scoutProvisioned = lib.any";
  };

  pointerSites = [
    {
      name = "main-go";
      label = "cmd/launcher/main.go's opencode driver branch";
      file = "cmd/launcher/main.go";
      path = ../../cmd/launcher/main.go;
      prefix = "//";
      anchor = "if driver == \"opencode\" {";
    }
    {
      name = "main-test-go";
      label = "cmd/launcher/main_test.go's opencode scout-model fallback test";
      file = "cmd/launcher/main_test.go";
      path = ../../cmd/launcher/main_test.go;
      prefix = "//";
      anchor = "func TestResolveAgentPresenceSignals_ScoutNoDocumentOpencodeDriverFallsBackToScoutModel(";
    }
    {
      name = "gates-go";
      label = "cmd/launcher/internal/promptassembly/gates.go's FILER_ENABLED gate";
      file = "cmd/launcher/internal/promptassembly/gates.go";
      path = ../../cmd/launcher/internal/promptassembly/gates.go;
      prefix = "//";
      anchor = "g[\"FILER_ENABLED\"] = e.FilerEnabled";
    }
    {
      name = "equivalence-opencode-driver";
      label = "nix/checks/equivalence.nix's mkharness-scout-provisioned-true-for-opencode-driver fixture";
      file = "nix/checks/equivalence.nix";
      path = ../../nix/checks/equivalence.nix;
      prefix = "#";
      anchor = "mkharness-scout-provisioned-true-for-opencode-driver =";
    }
    {
      name = "equivalence-opencode-opted-out-scout";
      label = "nix/checks/equivalence.nix's mkharness-scout-provisioned-false-for-opencode-opted-out-scout fixture";
      file = "nix/checks/equivalence.nix";
      path = ../../nix/checks/equivalence.nix;
      prefix = "#";
      anchor = "mkharness-scout-provisioned-false-for-opencode-opted-out-scout =";
    }
  ];

  # One line-list per distinct file, keyed on the path actually read so the key
  # cannot drift from it: equivalence.nix, the one file here holding two sites,
  # is read and split once instead of twice.
  linesByFile = builtins.listToAttrs (
    map (s: {
      name = toString s.path;
      value = linesOf s.path;
    }) ([ canonicalSite ] ++ pointerSites)
  );

  commentBlock =
    site:
    let
      lines = linesByFile.${toString site.path};
    in
    commentsAbove site.prefix lines (anchorIdx site lines);

  canonicalLines = commentBlock canonicalSite;
  canonicalText = concatStringsSep "\n" canonicalLines;

  # Derived from pointerSites so it cannot go stale, and by distinct file
  # rather than per-site label: the five labels spelled out ran to ~380
  # characters inside a single-line assert message, and the file list is what a
  # reader follows anyway.
  pointerSiteCount = toString (builtins.length pointerSites);
  dependentFiles = concatStringsSep ", " (
    builtins.attrNames (
      builtins.listToAttrs (
        map (s: {
          name = s.file;
          value = null;
        }) pointerSites
      )
    )
  );

  # One row per identifier the canonical comment must still name -- losing one
  # leaves a pointer site aimed at a rationale that no longer explains it.
  canonicalIdents = [
    {
      ident = "finalRoster";
      slug = "final-roster";
    }
    {
      ident = "driverAgentFiles";
      slug = "driver-agent-files";
    }
    {
      ident = "agentsJsonAttrs";
      slug = "agents-json-attrs";
    }
  ];

  canonicalCommentPresent = {
    name = "scout-rationale-parity-canonical-comment-present";
    value =
      assert assertMsg (canonicalLines != [ ])
        "scout-rationale-parity: no comment block found immediately above lib/mkHarness.nix's scoutProvisioned line -- this comment is the canonical rationale the ${pointerSiteCount} pointer sites in ${dependentFiles} all point at instead of restating.";
      pkgs.runCommand "scout-rationale-parity-canonical-comment-present" { } "touch $out";
  };

  canonicalCheck = row: {
    name = "scout-rationale-parity-canonical-${row.slug}";
    value =
      # Gated on the block existing, so a canonical comment detached from its
      # anchor fails scout-rationale-parity-canonical-comment-present alone
      # instead of reporting a missing identifier three more times.
      assert assertMsg (canonicalLines == [ ] || hasInfix row.ident canonicalText)
        "scout-rationale-parity: lib/mkHarness.nix's scoutProvisioned comment no longer mentions ${row.ident} -- this comment is the canonical rationale the ${pointerSiteCount} pointer sites in ${dependentFiles} all point at instead of restating.";
      pkgs.runCommand "scout-rationale-parity-canonical-${row.slug}" { } "touch $out";
  };

  pointerCheck = site: {
    name = "scout-rationale-parity-pointer-${site.name}";
    value =
      let
        blockText = concatStringsSep "\n" (commentBlock site);
      in
      assert assertMsg (hasInfix "lib/mkHarness.nix" blockText && hasInfix "scoutProvisioned" blockText)
        "scout-rationale-parity: the comment above ${site.label} no longer names both lib/mkHarness.nix and scoutProvisioned -- the pointer was dropped or reworded away from the canonical site in lib/mkHarness.nix.";
      pkgs.runCommand "scout-rationale-parity-pointer-${site.name}" { } "touch $out";
  };
in
builtins.listToAttrs (
  [ canonicalCommentPresent ] ++ map canonicalCheck canonicalIdents ++ map pointerCheck pointerSites
)
