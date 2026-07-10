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
      inherit (pkgs.lib) assertMsg concatMapStringsSep hasInfix;
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
      missingFromDoc = builtins.filter (s: !hasInfix s.section versioningDoc) sections;
    in
    assert assertMsg (cfg ? "changelog-sections")
      ".release-please-config.json must declare changelog-sections (canonical map in nix/checks/changelog.nix)";
    assert assertMsg (cfg."changelog-sections" == sections)
      "changelog-sections in .release-please-config.json drifted from the canonical map in nix/checks/changelog.nix";
    assert assertMsg (missingFromDoc == [ ])
      "VERSIONING.md is missing changelog headings: ${
        concatMapStringsSep ", " (s: s.section) missingFromDoc
      }";
    pkgs.runCommand "release-please-changelog" { } "touch $out";
}
