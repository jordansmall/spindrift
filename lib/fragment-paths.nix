# Where a lib/fragments.nix row's file lives under prompts/fragments/ (issue
# #5646, ADR 0068). A plain row is one root-level file; an `axis` row has one
# file per folder its axis signal can pick, in `<folder>/<fragment>`, because
# the active backend's signal picks the file at render time; a `shared` axis
# row also has the root `<fragment>` every folder without its own file falls
# back to. mkHarness's scan sites iterate `scanPathsOf`; the shipped-tree
# checks (image bake, golden coverage) iterate `pathsOf`.
let
  backends = import ./backends/default.nix;

  # An axis picks its folder by signal, not backend name (mirroring the Go
  # assembler's axisFolder and the gates), so jira, which rides github's read
  # arm, has no folder of its own. The registry defaults for an omitted signal
  # are documented in lib/backends/default.nix.
  axes = {
    tracker = {
      valid = "validAsTracker";
      signal = b: b.trackerAxisRead or "GITHUB";
    };
    filer = {
      valid = "validAsTracker";
      signal = b: b.trackerAxisFiler or "GH";
    };
    forge = {
      valid = "validAsCodeForge";
      signal = b: b.forgeBackend or "GH";
    };
  };
  # Mirrors the Go side: the signal lowercased, with "GH" the github arm's
  # forge/filer spelling.
  upper = builtins.genList (i: builtins.substring i 1 "ABCDEFGHIJKLMNOPQRSTUVWXYZ") 26;
  lower = builtins.genList (i: builtins.substring i 1 "abcdefghijklmnopqrstuvwxyz") 26;
  folderOf = signal: if signal == "GH" then "github" else builtins.replaceStrings upper lower signal;
  axisFolders =
    axis:
    let
      a = axes.${axis} or (throw "lib/fragment-paths.nix: unknown axis ${axis}");
      folders = map (b: folderOf (a.signal b)) (builtins.filter (b: b.${a.valid} or false) backends);
    in
    builtins.foldl' (acc: f: if builtins.elem f acc then acc else acc ++ [ f ]) [ ] folders;

  # A backend folder may legitimately lack a row's file (it renders empty), so
  # only the files present under fragmentsDir are listed; a shared row's root
  # file is the one every other folder reads, so it is always listed.
  pathsIn =
    fragmentsDir: row:
    if (row.axis or "") == "" then
      [ row.fragment ]
    else
      builtins.filter (p: builtins.pathExists (fragmentsDir + "/${p}")) (
        map (f: "${f}/${row.fragment}") (axisFolders row.axis)
      )
      ++ (if row.shared or false then [ row.fragment ] else [ ]);

  defaultFragmentsDir = ../templates/default/prompts/fragments;
  pathsOf = pathsIn defaultFragmentsDir;
in
{
  # Over the shipped tree by default; pass a fragmentsDir to a consumer that
  # reads another one.
  inherit pathsOf pathsIn;

  # The fragments/ folder one backend row reads from on `axis`, the same folder
  # pathsIn lists for that row.
  folderOf = axis: backend: folderOf (axes.${axis}.signal backend);

  # Files a scan of `fragmentsDir` may read: pathsIn plus a deprecated row's
  # legacyFragment, but only when no primary file exists there, mirroring the
  # Go assembler (legacy read only when the primary is missing) so a stale
  # legacy file the assembler never renders cannot fail the build. Kept out of
  # pathsOf so the shipped-tree checks do not demand the legacy file exist.
  scanPathsOf =
    fragmentsDir: row:
    pathsIn fragmentsDir row
    ++ (
      if
        row ? legacyFragment
        && builtins.all (p: !builtins.pathExists (fragmentsDir + "/${p}")) (pathsIn fragmentsDir row)
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
