{ pkgs, ... }:
{
  # release-please's implicit defaults hide `security` and every non-feat/fix
  # type spindrift uses, so .release-please-config.json must declare an
  # explicit changelog-sections map and VERSIONING.md must document every
  # rendered heading.
  release-please-changelog =
    let
      inherit (pkgs.lib)
        assertMsg
        any
        concatMapStringsSep
        concatStringsSep
        hasInfix
        splitString
        toLower
        trim
        ;
      # Order here is the order the headings render in CHANGELOG.md.
      sections = [
        {
          type = "feat";
          section = "Features";
        }
        {
          type = "fix";
          section = "Bug Fixes";
        }
        {
          type = "perf";
          section = "Performance Improvements";
        }
        {
          type = "security";
          section = "Security";
        }
        {
          type = "revert";
          section = "Reverts";
        }
        {
          type = "docs";
          section = "Documentation";
        }
        {
          type = "refactor";
          section = "Code Refactoring";
        }
        {
          type = "test";
          section = "Tests";
        }
        {
          type = "build";
          section = "Build System";
        }
        {
          type = "ci";
          section = "Continuous Integration";
        }
        {
          type = "chore";
          section = "Miscellaneous Chores";
        }
        {
          type = "style";
          section = "Styles";
        }
        {
          type = "deps";
          section = "Dependencies";
        }
      ];
      cfg = builtins.fromJSON (builtins.readFile ../../.release-please-config.json);
      versioningDoc = builtins.readFile ../../VERSIONING.md;
      # CHANGELOG.md is checked line by line because hasInfix's `.*pattern.*`
      # regex backtracks over a 100+KB file and segfaults; VERSIONING.md is
      # small enough for the regex path. This is a size limit, not an escaping
      # problem, because hasInfix already escapes its pattern.
      changelogLines = splitString "\n" (builtins.readFile ../../CHANGELOG.md);
      missingFromDoc = builtins.filter (s: !hasInfix s.section versioningDoc) sections;
      # Collapses internal runs of spaces and tabs so "##  [Unreleased]" and
      # "##\t[Unreleased]" compare equal to the canonical heading; trim alone
      # only strips the ends.
      collapseWs =
        s: concatStringsSep " " (builtins.filter builtins.isString (builtins.split "[ \t]+" s));
      isUnreleasedHeading = line: toLower (collapseWs (trim line)) == "## [unreleased]";
    in
    assert assertMsg (cfg ? "changelog-sections")
      ".release-please-config.json must declare changelog-sections (canonical map in nix/checks/changelog.nix)";
    assert assertMsg (cfg."changelog-sections" == sections)
      "changelog-sections in .release-please-config.json drifted from the canonical map in nix/checks/changelog.nix";
    assert assertMsg (missingFromDoc == [ ])
      "VERSIONING.md is missing changelog headings: ${
        concatMapStringsSep ", " (s: s.section) missingFromDoc
      }";
    # Self-test (issue #666, extended #897) pins isUnreleasedHeading's
    # normalization before the real file below relies on it. ### never matches:
    # per CHANGELOG.md's convention (see VERSIONING.md#what-lands-in-the-changelog)
    # ## is always a release heading and ### is always a section heading.
    assert assertMsg (isUnreleasedHeading "## [Unreleased] ")
      "isUnreleasedHeading must match a heading with trailing whitespace";
    assert assertMsg (isUnreleasedHeading "## [unreleased]")
      "isUnreleasedHeading must match case-insensitively";
    assert assertMsg (isUnreleasedHeading "## [Unreleased]")
      "isUnreleasedHeading must match the canonical heading";
    assert assertMsg (!isUnreleasedHeading "### [Unreleased]")
      "isUnreleasedHeading must not match a section-level heading (### is always a section, not a release, per CHANGELOG.md's convention)";
    assert assertMsg (isUnreleasedHeading "##  [Unreleased]")
      "isUnreleasedHeading must match a heading with doubled internal whitespace";
    assert assertMsg (isUnreleasedHeading "##\t[Unreleased]")
      "isUnreleasedHeading must match a heading with a tab in place of a space";
    assert assertMsg (
      !isUnreleasedHeading "##  [1.0.0]"
    ) "isUnreleasedHeading must not match a versioned heading even with doubled internal whitespace";
    # release-please never emits an `[Unreleased]` heading; one appearing in
    # CHANGELOG.md is always a stale, hand-inserted duplicate (issue #614).
    assert assertMsg (!any isUnreleasedHeading changelogLines)
      "CHANGELOG.md contains a stale ## [Unreleased] heading; release-please never emits one, remove the hand-inserted block";
    pkgs.runCommand "release-please-changelog" { } "touch $out";
}
