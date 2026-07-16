# The release-please changelog contract: .release-please-config.json must
# declare an explicit changelog-sections map, and every rendered heading must
# be documented in VERSIONING.md.
{ pkgs, ... }:
{
  # The changelog contract: .release-please-config.json must declare an
  # explicit changelog-sections map (never rely on release-please's
  # implicit defaults, which hide `security` and every non-feat/fix type
  # spindrift uses), and every rendered heading must be documented in
  # VERSIONING.md. Pure eval — reads both files, no builder needed.
  release-please-changelog =
    let
      inherit (pkgs.lib)
        assertMsg
        any
        concatMapStringsSep
        hasInfix
        splitString
        toLower
        trim
        ;
      # Source of truth for the section map. Order here is the order the
      # headings render in CHANGELOG.md. Nothing is hidden (see VERSIONING.md).
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
      # CHANGELOG.md is checked line-by-line rather than with hasInfix's
      # `.*pattern.*` regex match (lib/strings.nix): std::regex backtracking
      # over a 100+KB file segfaults, whereas VERSIONING.md is small enough
      # for the regex path to stay safe. This is a perf/size limit, not a
      # metacharacter-escaping problem: hasInfix already escapes its pattern
      # via escapeRegex before building the match.
      changelogLines = splitString "\n" (builtins.readFile ../../CHANGELOG.md);
      missingFromDoc = builtins.filter (s: !hasInfix s.section versioningDoc) sections;
      isUnreleasedHeading = line: toLower (trim line) == "## [unreleased]";
    in
    assert assertMsg (cfg ? "changelog-sections")
      ".release-please-config.json must declare changelog-sections (canonical map in nix/checks/changelog.nix)";
    assert assertMsg (cfg."changelog-sections" == sections)
      "changelog-sections in .release-please-config.json drifted from the canonical map in nix/checks/changelog.nix";
    assert assertMsg (missingFromDoc == [ ])
      "VERSIONING.md is missing changelog headings: ${
        concatMapStringsSep ", " (s: s.section) missingFromDoc
      }";
    # Self-test (issue #666): pins isUnreleasedHeading's normalization before
    # trusting it against the real file below. Deliberately excludes
    # section-level (###) headings: per CHANGELOG.md's convention (see
    # VERSIONING.md#what-lands-in-the-changelog), ## is always a release
    # heading and ### is always a section heading, never a release.
    assert assertMsg (isUnreleasedHeading "## [Unreleased] ")
      "isUnreleasedHeading must match a heading with trailing whitespace";
    assert assertMsg (isUnreleasedHeading "## [unreleased]")
      "isUnreleasedHeading must match case-insensitively";
    assert assertMsg (isUnreleasedHeading "## [Unreleased]")
      "isUnreleasedHeading must match the canonical heading";
    assert assertMsg (!isUnreleasedHeading "### [Unreleased]")
      "isUnreleasedHeading must not match a section-level heading (### is always a section, not a release, per CHANGELOG.md's convention)";
    # release-please never emits an `[Unreleased]` heading; one appearing in
    # CHANGELOG.md is always a stale, hand-inserted duplicate (issue #614).
    assert assertMsg (!any isUnreleasedHeading changelogLines)
      "CHANGELOG.md contains a stale ## [Unreleased] heading; release-please never emits one, remove the hand-inserted block";
    pkgs.runCommand "release-please-changelog" { } "touch $out";
}
