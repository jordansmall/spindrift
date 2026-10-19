# Where a lib/fragments.nix row's file lives under prompts/fragments/ (issue
# #5646, ADR 0068). A plain row is one root-level file; an `axis` row has one
# file per backend valid on that axis, in `<backend>/<fragment>`, because the
# active backend picks the file at render time. mkHarness's scan sites iterate
# `scanPathsOf`; the shipped-tree checks (image bake, golden coverage) iterate
# `pathsOf`.
let
  backends = import ./backends/default.nix;
  validField = {
    tracker = "validAsTracker";
    forge = "validAsCodeForge";
  };
  pathsOf =
    row:
    if (row.axis or "") == "" then
      [ row.fragment ]
    else
      map (b: "${b.name}/${row.fragment}") (
        builtins.filter (b: b.${validField.${row.axis}} or false) backends
      );
in
{
  inherit pathsOf;

  # Files a scan of `fragmentsDir` may read: pathsOf plus a deprecated row's
  # legacyFragment, but only when no primary file exists there, mirroring the
  # Go assembler (legacy read only when the primary is missing) so a stale
  # legacy file the assembler never renders cannot fail the build. Kept out of
  # pathsOf so the shipped-tree checks do not demand the legacy file exist.
  scanPathsOf =
    fragmentsDir: row:
    pathsOf row
    ++ (
      if
        row ? legacyFragment
        && builtins.all (p: !builtins.pathExists (fragmentsDir + "/${p}")) (pathsOf row)
      then
        [ row.legacyFragment ]
      else
        [ ]
    );

  # Whether an old-layout fragmentsDir handed to mkHarness (a test fixture;
  # fragmentsDir is not Consumer-tunable) may omit the row's files. The runtime
  # --prompt-dir fallback lives in the Go assembler (assemble.go).
  mayBeAbsent = row: (row.axis or "") != "" || row ? legacyFragment;
}
