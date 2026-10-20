# Where a lib/fragments.nix row's file lives under prompts/fragments/ (issue
# #5646, ADR 0068). A plain row is one root-level file; an `axis` row has one
# file per folder its axis signal can pick, in `<folder>/<fragment>`, because
# the active backend's signal picks the file at render time; a `shared` axis
# row also has the root `<fragment>` every folder without its own file falls
# back to. mkHarness's scan sites iterate `scanPathsOf`; the shipped-tree
# checks (image bake, golden coverage) iterate `pathsOf`.
#
# The shipped tree's per-backend files are declared in `shipped` below,
# independent of disk, so the shipped-tree checks fail when a declared file
# goes missing (issue #5726) instead of shrinking their own expected set. A new
# axis row, or a new per-backend file, is declared there too.
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
  uniq = builtins.foldl' (acc: f: if builtins.elem f acc then acc else acc ++ [ f ]) [ ];
  axisFolders =
    axis:
    let
      a = axes.${axis} or (throw "lib/fragment-paths.nix: unknown axis ${axis}");
      folders = map (b: folderOf (a.signal b)) (builtins.filter (b: b.${a.valid} or false) backends);
    in
    uniq folders;

  # A row's files across the given axis folders; a shared row's root file is
  # the one every other folder reads, so it is always listed.
  pathsFor =
    folders: row:
    if (row.axis or "") == "" then
      [ row.fragment ]
    else
      map (f: "${f}/${row.fragment}") folders ++ (if row.shared or false then [ row.fragment ] else [ ]);

  # The tolerant listing mkHarness's scan sites use over whatever fragmentsDir
  # they get (shipped, fixture, or override): a backend folder may legitimately
  # lack a row's file (it renders empty), so only the files present are listed.
  pathsIn =
    fragmentsDir: row:
    pathsFor (builtins.filter (f: builtins.pathExists (fragmentsDir + "/${f}/${row.fragment}")) (
      axisFolders row.axis
    )) row;

  # Per axis-row fragment, the folders the shipped tree carries a file in. An
  # empty list is legitimate: a folder without a row's file renders empty
  # (ADR 0068), and a `shared` row's root file stands in for every folder.
  shipped = {
    "filer-direct-label.md" = [
      "github"
      "forgejo"
    ];
    "filer-dedup.md" = [
      "github"
      "forgejo"
      "local"
    ];
    "filer-direct-file.md" = [
      "github"
      "forgejo"
    ];
    "fix-ci-read.md" = [
      "github"
      "forgejo"
    ];
    "pr-body-issue-ref.md" = [ "local" ];
    "pr-body-no-issue-ref.md" = [ "local" ];
    "issue-read.md" = [ "local" ];
    "research-issue-read.md" = [ "local" ];
    "scout-issue-read.md" = [ ];
    "research-verdict.md" = [
      "github"
      "forgejo"
    ];
    "research-verdict-relay.md" = [ "local" ];
    "research-verdict-relay-socket.md" = [ "local" ];
    "research-outcome-landing.md" = [
      "github"
      "forgejo"
    ];
    "issue-blocked-comment.md" = [
      "github"
      "forgejo"
    ];
    "issue-blocked-comment-relay.md" = [ "local" ];
    "open-pr-create.md" = [
      "github"
      "forgejo"
    ];
    "if-blocked-pr.md" = [
      "github"
      "forgejo"
    ];
  };

  axisRows = builtins.filter (r: (r.axis or "") != "") (import ./fragments.nix);
  allFolders = uniq (builtins.concatMap axisFolders (builtins.attrNames axes));

  # Every way `fragmentsDir` disagrees with `shipped`, each naming the path;
  # empty when it matches. The reverse direction (a file under a backend folder
  # that nothing declares) keeps a stray or misnamed file from going unnoticed.
  problemsWith =
    shipped: fragmentsDir:
    let
      forRow =
        row:
        let
          declared = shipped.${row.fragment} or null;
          folders = axisFolders row.axis;
        in
        if declared == null then
          [ "${row.fragment}: axis row has no entry in lib/fragment-paths.nix's `shipped`" ]
        else
          builtins.concatMap (
            f:
            if !builtins.elem f folders then
              [ "${f}/${row.fragment}: declared, but ${f} is not a folder of axis ${row.axis}" ]
            else if !builtins.pathExists (fragmentsDir + "/${f}/${row.fragment}") then
              [ "${f}/${row.fragment}: declared in lib/fragment-paths.nix but missing under ${fragmentsDir}" ]
            else
              [ ]
          ) declared;
      declaredPaths = builtins.concatMap (
        r: map (f: "${f}/${r.fragment}") (shipped.${r.fragment} or [ ])
      ) axisRows;
      strays =
        folder:
        if builtins.pathExists (fragmentsDir + "/${folder}") then
          map
            (
              n: "${folder}/${n}: present under ${fragmentsDir} but declared by no row in lib/fragment-paths.nix"
            )
            (
              builtins.filter (n: !builtins.elem "${folder}/${n}" declaredPaths) (
                builtins.attrNames (builtins.readDir (fragmentsDir + "/${folder}"))
              )
            )
        else
          [ ];
    in
    builtins.concatMap forRow axisRows
    ++ builtins.concatMap strays allFolders
    ++ map (
      k: "${k}: entry in lib/fragment-paths.nix's `shipped` names no axis row in lib/fragments.nix"
    ) (builtins.filter (k: !builtins.any (r: r.fragment == k) axisRows) (builtins.attrNames shipped));
  shippedProblemsIn = problemsWith shipped;

  defaultFragmentsDir = ../templates/default/prompts/fragments;
  shippedProblems = shippedProblemsIn defaultFragmentsDir;

  # The shipped tree's expected files: what `shipped` declares, not what is on
  # disk, so a missing one fails the shipped-tree checks rather than shrinking
  # the set.
  pathsOf =
    row:
    if shippedProblems != [ ] then
      throw "lib/fragment-paths.nix: shipped per-backend fragment files disagree with the tree:\n  ${builtins.concatStringsSep "\n  " shippedProblems}"
    else
      pathsFor shipped.${row.fragment} row;
in
{
  # Over the shipped tree by default; pass a fragmentsDir to a consumer that
  # reads another one.
  inherit pathsOf pathsIn shippedProblemsIn;

  # For the check's negative cases: the same comparison over a doctored `shipped`.
  inherit problemsWith shipped;

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
