# Merges the per-concern check modules into `checks` (everything, issue #454)
# and `checks-inbox` (the source-level subset, issue #581) so the Box gate can
# build the scoped target without re-baking the OCI image. The shared arg
# bundle is hoisted once so every module sees the same fixtures and pkgs.
{
  pkgs,
  config,
  fixtures,
  nixpkgs,
  system,
  flake-parts,
  checksAt,
  baselineChecks,
}:
let
  buildConstants = import ../../lib/build-constants.nix;

  # bats.nix generates its bats-shard-N checks from this same module
  # (issue #2648). Importing it once keeps the directory scan to a single run
  # and lets the name lists below derive from it instead of hand-duplicating.
  batsShards = import ./bats-shards.nix { inherit pkgs; };
  batsShardNames = batsShards.shardNames;

  # Shared machinery behind the fragment-parity checks (issue #3240), imported
  # once so commit/tdd/code-review-fragment-parity.nix each pick it up from
  # `common` instead of re-deriving it.
  mkFragmentParity = import ./mk-fragment-parity.nix { inherit pkgs fixtures; };

  # Every check module that reconstructs a launcher tree (go.nix,
  # baked-skills.nix) shares this one vendor tree instead of deriving its own
  # (issue #784); go.nix's per-Driver checks re-derive it over a source without
  # the Driver directories, under the same vendorHash.
  launcherGoModules =
    (pkgs.buildGoModule {
      pname = "spindrift-launcher-modules";
      version = "0";
      src = ../../cmd/launcher;
      vendorHash = buildConstants.launcherVendorHash;
    }).goModules;

  goCheckEnv = import ./go-check-env.nix;

  imageNixCores = import ./image-nix-setting.nix "cores";
  imageNixMaxJobs = import ./image-nix-setting.nix "max-jobs";

  common = {
    inherit
      pkgs
      config
      fixtures
      nixpkgs
      system
      flake-parts
      launcherGoModules
      batsShards
      mkFragmentParity
      goCheckEnv
      imageNixCores
      imageNixMaxJobs
      ;
  };
  sourceChecks =
    (import ./bats.nix common)
    // (import ./equivalence.nix common)
    // (import ./builtins-compat.nix common)
    // (import ./preambles.nix common)
    // (import ./drivers.nix common)
    // (import ./prompt-inject.nix common)
    // (import ./fragment-pairs.nix common)
    // (import ./tdd-fragment-parity.nix common)
    // (import ./commit-fragment-parity.nix common)
    // (import ./mk-fragment-parity-rejects.nix common)
    // (import ./nix-checks-lore-parity.nix common)
    // (import ./scout-rationale-parity.nix common)
    // (import ./scout-rationale-parity-rejects.nix common)
    // (import ./code-review-fragment-parity.nix common)
    // (import ./prompt-contract.nix common)
    // (import ./prompt-contract-parity.nix common)
    // (import ./research-verdicts.nix common)
    // (import ./jira-status-mapping.nix common)
    // (import ./awake-window.nix common)
    // (import ./chore-catalog.nix common)
    // (import ./read-only-capability.nix common)
    // (import ./network-mode.nix common)
    // (import ./nix-store-writable.nix common)
    // (import ./row-knobs.nix common)
    // (import ./runner-names-guard.nix common)
    // (import ./runners-unique-runtime.nix common)
    // (import ./merge-guard-paths.nix common)
    // (import ./prompts.nix common)
    // (import ./schema-drift.nix common)
    // (import ./quickstart-golden.nix common)
    // (import ./dispatch-labels.nix common)
    // (import ./gh-token-intervals.nix common)
    // (import ./dashboard-constant-parity.nix common)
    // (import ./dashboard-js.nix common)
    // (import ./opencode-guards.nix common)
    // (import ./opencode-formatter-lsp-pin.nix common)
    // (import ./opencode-session-schema.nix common)
    // (import ./doctor-report-routing.nix common)
    // (import ./agent-workflow-smoke.nix common)
    // (import ./nix-cache-key.nix common)
    // (import ./ci-scripts-from-base.nix common)
    // (import ./changelog.nix common)
    // (import ./versioning-prompt-template-carve-out.nix common)
    // (import ./go.nix common)
    // (import ./roster.nix common)
    // (import ./dogfood-pi-roster.nix common)
    // (import ./skills.nix common)
    // (import ./driver-map.nix common)
    // (import ./promptassembly.nix common)
    // (import ./prompt-assembly-golden-coverage.nix common)
    // (import ./fragment-paths.nix common)
    // (import ./baked-skills.nix common)
    // (import ./seccomp.nix common)
    // (import ./adr-numbers.nix common)
    // (import ./docs-edit-isolation.nix (common // { inherit checksAt baselineChecks; }))
    // (import ./seam-fixtures.nix common)
    // (import ./event-stream-parity.nix common)
    // (import ./bare-bang-guard.nix common)
    // (import ./backend-name-guard.nix common)
    // (import ./eval-probe.nix common);

  imageChecks = pkgs.lib.optionalAttrs pkgs.stdenv.isLinux (import ./image.nix common);

  # Checks that realize the OCI image, directly or by asserting facts about the
  # box's own baked toolchain. Re-running them inside the box built from that
  # image is redundant and heavy (issue #581), so `checks-inbox` excludes them
  # while `checks` keeps them for CI's pre-dispatch gate.
  imageOnlyCheckNames = [
    "nil-baked-in-dogfood"
    "nix-eval-jobs-baked-in-dogfood"
    "bats-baked-in-dogfood"
    "shellcheck-baked-in-dogfood"
    "caveman-baked-in-dogfood"
    # Realizes the real (unfree) claude-code package to grep its own binary
    # (issue #2011), a fact about the box's own baked toolchain.
    "drivers-claude-cli-knows-disable-background-tasks-env"
  ];

  # Checks too costly to evaluate in a Box (issue #5849); CI's
  # `nix flake check` still runs them.
  evalHeavyCheckNames = [ "docs-edit-isolation" ];

  # This starts from portableSourceChecks, not sourceChecks, so a darwin eval
  # also drops linuxOnlyCheckNames. Otherwise checks-inbox forces
  # mkharness-agent-closure-package's build on darwin, where
  # bwrapHarness.packages lacks agent-closure and its own assert throws.
  #
  # The bats shards are source checks, not image ones, but they push a cold
  # checks-inbox past Claude Code's stock 10-minute Bash cap (dogfood raises
  # it to 30 minutes, issue #4409, but whether the suite fits that is
  # unmeasured). CI's `nix flake check` still runs them; fold them back in
  # once the Box Go port (issue #4275) has shrunk the suite.
  checksInboxSet = removeAttrs portableSourceChecks (
    imageOnlyCheckNames ++ evalHeavyCheckNames ++ batsShardNames
  );

  # A narrower axis than imageOnlyCheckNames: source checks that depend on the
  # bats harness internals, e.g. the Linux-twin driverExecBin build
  # (issue #2648, issue #2354). mkHarness.nix always instantiates pkgs for the
  # Linux twin of the host system, so a darwin eval still demands a Linux
  # builder and fails with "Required system: aarch64-linux".
  linuxOnlyCheckNames = batsShardNames ++ [
    # Evaluates the bats shards, which are Linux-only (issue #5154). Also in
    # evalHeavyCheckNames, which only checks-inbox drops.
    "docs-edit-isolation"
    # flake.nix only exposes apps.regen-goldens under isLinux, because it pulls
    # in fixtures.seamFixtures, rendered off the Linux-only batsHarness. On darwin it is absent
    # from config.apps, so this check's own existence assert would throw.
    "regen-goldens-app-wiring"
    # lib/mkHarness.nix only exposes packages.agent-closure when isLinux, and
    # nix/fixtures.nix instantiates bwrapHarness at the current system, so on
    # darwin bwrapHarness.packages lacks it and this assert throws during eval.
    "mkharness-agent-closure-package"
    "mkharness-agent-closure-bundles-both"
    "mkharness-agent-closure-tracks-prefetch"
    # Builds a bwrap harness whose packages exist only when isLinux.
    "mkharness-image-attr-name"
    # Same isLinux gate one hop further out: asserts config.packages carries
    # agent-closure, which flake.nix only re-exports when
    # fixtures.dogfoodBwrapHarness.packages has it.
    "dogfood-bwrap-app-wiring"
    # Same gate for agent-closure-pi off fixtures.dogfoodBwrapPiHarness.
    "dogfood-bwrap-pi-app-wiring"
    # lib/seccomp.nix builds against pkgs.libseccomp, whose meta.platforms is
    # Linux-only, so evaluating it on darwin throws "Refusing to evaluate
    # package 'libseccomp'" before the check derivation itself ever runs.
    "seccomp-filter-is-regular-file-multiple-of-8-bytes"
  ];

  portableSourceChecks =
    if pkgs.stdenv.isLinux then sourceChecks else removeAttrs sourceChecks linuxOnlyCheckNames;

  # CI's docs lane (issue #5155) builds only these modules' checks when a PR
  # touches nothing outside the docs allowlist in .github/scripts/ci-changes.sh.
  # Whole modules, not single checks: the guard below can tell which module
  # reads a docs file, not which of its checks does, and a check the docs
  # change left alone substitutes from main's cache anyway.
  docsCheckModules = [
    "adr-numbers"
    # Reads no docs, but scans every *.nix under nix/checks/, which a
    # comment-only edit (issue #5158) can reach.
    "bare-bang-guard"
    # Reads no docs, but scans core .go/.nix as text; a comment-only edit can
    # add a trailing-comment comparison it flags.
    "backend-name-guard"
    "changelog"
    "driver-map"
    "event-stream-parity"
    "go"
    "nix-checks-lore-parity"
    "prompts"
    "roster"
    # Reads no docs, but scans every *.nix in the repo (trailing comments included),
    # which a comment-only edit can reach.
    "runner-names-guard"
    "schema-drift"
    "versioning-prompt-template-carve-out"
  ];

  # Modules the guard below skips. docs-edit-isolation names docs paths only to
  # filter them out of a perturbed copy and never reads them; its verdict is
  # docs-independent unless a pinned file vanishes (its own assert names the fix).
  docsGuardExemptModules = [ "docs-edit-isolation" ];

  docsCheckNames = pkgs.lib.concatMap (
    m: builtins.attrNames (import (./. + "/${m}.nix") common)
  ) docsCheckModules;

  checksDocsSet = builtins.intersectAttrs (pkgs.lib.genAttrs docsCheckNames (_: null)) (
    removeAttrs portableSourceChecks imageOnlyCheckNames
  );

  nixFilesIn =
    dir: pkgs.lib.filter (pkgs.lib.hasSuffix ".nix") (builtins.attrNames (builtins.readDir dir));
  # Built, not spelled out: a literal path would make this file a reader.
  driversPath = ./. + "/drivers";
  driverFiles = nixFilesIn driversPath;

  checksRel = "nix/checks";
  driversRel = "${checksRel}/drivers";

  # A driver check file is attributed to owner `drivers`: it enters the build
  # only through drivers.nix (it needs that module's entry/driverRegistry
  # args), so it cannot be named in docsCheckModules itself.
  driverModule = name: text: {
    owner = "drivers";
    key = "drivers/${name}";
    dir = driversRel;
    inherit text;
  };

  # Every nix/checks module the coverage guards scan.
  scannedModules =
    let
      mods =
        map (f: {
          owner = pkgs.lib.removeSuffix ".nix" f;
          dir = checksRel;
          text = builtins.readFile (./. + "/${f}");
        }) (nixFilesIn ./.)
        ++ map (
          f: driverModule (pkgs.lib.removeSuffix ".nix" f) (builtins.readFile (driversPath + "/${f}"))
        ) driverFiles;
    in
    # The controls below feed hand-made records past scannedModules, so this is
    # the only thing that notices the driver enumeration being dropped.
    assert pkgs.lib.assertMsg (
      driverFiles != [ ]
      &&
        builtins.length (pkgs.lib.filter (m: m.owner == "drivers" && m.dir == driversRel) mods)
        == builtins.length driverFiles
    ) "scannedModules must hold one `drivers` record per file in nix/checks/drivers";
    mods;

  # A `../` per component of `dir` reaches the repo root from a module in it,
  # as a regex fragment.
  repoRootPrefixRe = dir: pkgs.lib.concatMapStrings (_: ''\.\./'') (pkgs.lib.splitString "/" dir);

  # Built, not spelled out: a literal path would make this file a reader.
  dotDotPath = n: pkgs.lib.concatStrings (pkgs.lib.replicate n "../");
  dotPath = f: "." + "/${f}";

  # Owners (unique) of modules in `mods` that read a docs file but are not
  # in `listed`.
  docsReadersMissing =
    listed: mods:
    let
      inherit (pkgs.lib) filter unique;
      docsRef =
        dir:
        ''${repoRootPrefixRe dir}(docs([^a-zA-Z0-9_-]|$)|[^/ "}]+\.md|\.release-please-manifest\.json)'';
      readsDocs = m: builtins.length (builtins.split (docsRef m.dir) m.text) > 1;
    in
    unique (map (m: m.owner) (filter (m: readsDocs m && !(builtins.elem m.owner listed)) mods));

  # Coverage guard (issue #5155): a module reads a docs file when it names a
  # repo-root path on the ci-changes.sh allowlist: docs/, a root *.md, or the
  # release-please manifest, at the depth its directory implies. Blind spots:
  # a path built by string concatenation, a whole-repo source (the root named
  # as a bare `.` after the `../` hops, from a module or a Driver check file),
  # a path that stops at nix/checks/ or nix/ (a Driver check file's one or two
  # `../` match neither pattern), and a read made inside lib/ (mkHarness's
  # manifest version).
  checks-docs-covers-docs-readers =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep filter;
      moduleNames = map (m: m.owner) (filter (m: m.dir == checksRel) scannedModules);
      missing = docsReadersMissing (docsCheckModules ++ docsGuardExemptModules) scannedModules;
      stale = filter (m: !(builtins.elem m moduleNames)) (docsCheckModules ++ docsGuardExemptModules);
    in
    assert assertMsg (missing == [ ])
      "nix/checks module(s) read a docs file but are missing from docsCheckModules in nix/checks/default.nix: ${concatStringsSep ", " missing}";
    assert assertMsg (stale == [ ])
      "docsCheckModules/docsGuardExemptModules names a module absent from nix/checks: ${concatStringsSep ", " stale}";
    assert assertMsg (
      docsReadersMissing [ ] [ (driverModule "x" "${dotDotPath 3}docs/x.md") ] == [ "drivers" ]
    ) "negative control: a docs read from nix/checks/drivers/ must flag owner `drivers`";
    assert assertMsg (
      docsReadersMissing [ ] [ (driverModule "x" "${dotDotPath 2}docs/x.md") ] == [ ]
    ) "depth control: a two-level docs read from nix/checks/drivers/ is nix/docs, not the repo's";
    pkgs.runCommand "checks-docs-covers-docs-readers" { } "touch $out";

  # Coverage guard (issue #5158): a comment-only .go/.nix edit takes the docs
  # lane, so each such file a module outside docsCheckModules names (other
  # than to import it) must match .github/scripts/ci-comment-readers, and each
  # named source tree must be reviewed into commentTreeReaders (a Driver check
  # file's reads key under `drivers/<name>:`). Blind spots: as
  # checks-docs-covers-docs-readers.
  commentTreeReaders = {
    "bats:.github/scripts" = "the classifier suite builds ci-go-tokens.go";
    "baked-skills:cmd/launcher" = "spliced and built; its marker files are under promptassembly/";
    "dashboard-js:dashboard" = "a fileset root over the JS tree only";
    "default:." = "the coverage guards' path probes";
    "default:cmd/launcher" = "launcherGoModules' vendor source";
    "default:nix/checks" = "the coverage guards, both in checks-docs";
    "doctor-report-routing:cmd/launcher" = "greps internal/doctor/*.go and launchgates.go";
    "drivers:nix/checks/drivers" = "imports each Driver's check file by registry name";
    "equivalence:." = "harness flake fixtures' outPath";
    "equivalence:cmd/launcher" = "source-identity filesets";
    "equivalence:cmd/launcher/internal/driver/bundle" = "lists its *.go names, never their text";
    "promptassembly:cmd/launcher/internal/promptassembly" = "greps its *.go";
    "quickstart-golden:." = "a flake fixture's outPath";
    "skills:." = "a harness flake fixture's outPath";
    "skills:nix/checks" = "a path value the skills validator must accept";
  };

  # `uncovered` and `unreviewedTrees` (as { m, rel } reads) of the modules in
  # `mods` whose owner is not in `listed`. `./` resolves against the module's own
  # directory, and the repo root is `../` per component of it.
  commentReadersFindings =
    listed: mods:
    let
      inherit (pkgs.lib)
        any
        concatMap
        elem
        elemAt
        filter
        hasPrefix
        hasSuffix
        isList
        pathIsDirectory
        removeSuffix
        splitString
        unique
        ;
      inherit (pkgs.lib.filesystem) listFilesRecursive;
      globs = filter (l: l != "" && !(hasPrefix "#" l)) (
        splitString "\n" (builtins.readFile ../../.github/scripts/ci-comment-readers)
      );
      covered =
        p: any (g: builtins.match (builtins.replaceStrings [ "." "*" ] [ "\\." ".*" ] g) p != null) globs;
      refRe =
        dir:
        ''(^|[^./A-Za-z0-9_-])(import[[:space:]]+|callPackage[[:space:]]+|imports[[:space:]]*=[[:space:]]*\[[[:space:]]*)?(${repoRootPrefixRe dir}|\./)([A-Za-z0-9_.-][A-Za-z0-9_./-]*)'';
      relOf =
        dir: g:
        let
          base = if elemAt g 2 == "./" then dir else ".";
          p = removeSuffix "/" (elemAt g 3);
        in
        if p == "." then
          base
        else if base == "." then
          p
        else
          "${base}/${p}";
      reads =
        mod:
        map (g: {
          m = mod.key or mod.owner;
          rel = relOf mod.dir g;
        }) (filter (g: isList g && elemAt g 1 == null) (builtins.split (refRe mod.dir) mod.text));
      allReads = unique (concatMap reads (filter (mod: !(elem mod.owner listed)) mods));
      isSource = p: hasSuffix ".go" p || hasSuffix ".nix" p;
      holdsSource =
        rel:
        let
          path = ../../. + "/${rel}";
        in
        rel == "."
        || (
          builtins.pathExists path
          && pathIsDirectory path
          && any (f: isSource (toString f)) (listFilesRecursive path)
        );
    in
    {
      uncovered = filter (r: isSource r.rel && !(covered r.rel)) allReads;
      unreviewedTrees = filter (
        r: !(isSource r.rel) && holdsSource r.rel && !(commentTreeReaders ? "${r.m}:${r.rel}")
      ) allReads;
    };

  ci-comment-readers-cover-checks =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep;
      inherit (commentReadersFindings docsCheckModules scannedModules) uncovered unreviewedTrees;
      show = rs: concatStringsSep ", " (map (r: "${r.m}:${r.rel}") rs);
      driverFindings = text: commentReadersFindings [ ] [ (driverModule "x" text) ];
      driverUncovered = text: show (driverFindings text).uncovered;
    in
    assert assertMsg (uncovered == [ ])
      "nix/checks module(s) outside docsCheckModules read a .go/.nix file that .github/scripts/ci-comment-readers does not match, so a comment edit to it would skip the check: ${show uncovered}";
    assert assertMsg (unreviewedTrees == [ ])
      "nix/checks module(s) outside docsCheckModules name a .go/.nix tree absent from commentTreeReaders in nix/checks/default.nix; list any file in it the module reads as text in .github/scripts/ci-comment-readers, then add the tree: ${show unreviewedTrees}";
    assert assertMsg
      (
        driverUncovered "${dotDotPath 3}cmd/launcher/fixture-uncovered.go"
        == "drivers/x:cmd/launcher/fixture-uncovered.go"
      )
      "negative control: an unmatched .go read from nix/checks/drivers/ must be flagged under `drivers/<name>`";
    assert assertMsg (
      driverUncovered (dotPath "x.go") == "drivers/x:nix/checks/drivers/x.go"
    ) "depth control: a ./ read from nix/checks/drivers/ must resolve against that directory";
    assert assertMsg (show (driverFindings "${dotDotPath 3}lib").unreviewedTrees == "drivers/x:lib")
      "negative control: an unlisted source tree named from nix/checks/drivers/ must be flagged under `drivers/<name>`";
    pkgs.runCommand "ci-comment-readers-cover-checks" { } "touch $out";

  # Stale-name guard: every linuxOnlyCheckName must name a real source check,
  # or the darwin drop silently does nothing.
  linux-only-check-names-exist =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep filter;
      stale = filter (n: !(builtins.hasAttr n sourceChecks)) linuxOnlyCheckNames;
    in
    assert assertMsg (
      stale == [ ]
    ) "linuxOnlyCheckNames names a check absent from sourceChecks: ${concatStringsSep ", " stale}";
    pkgs.runCommand "linux-only-check-names-exist" { } "touch $out";

  # Regression guard (issues #581, #5849): each excluded name must name a check
  # that exists, or a renamed entry silently does nothing, and none of them may
  # leak into checksInboxSet, or the exclusion itself has regressed.
  mkInboxExclusionGuard =
    {
      drvName,
      listName,
      names,
      kind,
    }:
    let
      inherit (pkgs.lib) assertMsg concatStringsSep filter;
      stale = filter (n: !(builtins.hasAttr n sourceChecks)) names;
      leaked = filter (n: builtins.hasAttr n checksInboxSet) names;
    in
    assert assertMsg (
      stale == [ ]
    ) "${listName} names a check absent from sourceChecks: ${concatStringsSep ", " stale}";
    assert assertMsg (
      leaked == [ ]
    ) "checks-inbox must not contain ${kind} checks: ${concatStringsSep ", " leaked}";
    pkgs.runCommand drvName { } "touch $out";

  # Scoped in-box gate (issue #581): the source-level checks joined into one
  # derivation so it builds with a single `nix build .#checks-inbox` instead
  # of enumerating names.
  checks-inbox = pkgs.runCommand "checks-inbox" { } ''
    ${pkgs.lib.concatMapStringsSep "\n" (p: ": ${p}") (builtins.attrValues checksInboxSet)}
    touch $out
  '';

  # The set is also exposed as an attrset (issue #5862) so per-check builds can
  # enumerate it. The aggregate's build script holds the drvPath of every check
  # it depends on in its string context, so comparing that against the exposed
  # set catches drift instead of restating the value both are built from.
  checks-inbox-set = checksInboxSet;

  checks-inbox-set-parity =
    let
      inherit (pkgs.lib) assertMsg concatStringsSep subtractLists;
      aggregated = builtins.attrNames (builtins.getContext checks-inbox.drvAttrs.buildCommand);
      exposed = map (d: d.drvPath) (builtins.attrValues checks-inbox-set);
      extra = subtractLists exposed aggregated;
      missing = subtractLists aggregated exposed;
    in
    assert assertMsg (extra == [ ] && missing == [ ])
      "checks-inbox differs from checks-inbox-set: aggregate-only [${concatStringsSep ", " extra}], set-only [${concatStringsSep ", " missing}]";
    pkgs.runCommand "checks-inbox-set-parity" { } "touch $out";

  checks-inbox-excludes-image-checks = mkInboxExclusionGuard {
    drvName = "checks-inbox-excludes-image-checks";
    listName = "imageOnlyCheckNames";
    names = imageOnlyCheckNames;
    kind = "image-realizing";
  };

  checks-inbox-excludes-eval-heavy-checks = mkInboxExclusionGuard {
    drvName = "checks-inbox-excludes-eval-heavy-checks";
    listName = "evalHeavyCheckNames";
    names = evalHeavyCheckNames;
    kind = "eval-heavy";
  };
in
{
  checks =
    portableSourceChecks
    // imageChecks
    // {
      inherit
        checks-inbox-excludes-image-checks
        checks-inbox-excludes-eval-heavy-checks
        checks-inbox-set-parity
        linux-only-check-names-exist
        checks-docs-covers-docs-readers
        ci-comment-readers-cover-checks
        ;
    };

  inherit checks-inbox checks-inbox-set;

  # CI's docs-only lane (issue #5155): the checks whose modules read docs
  # files, plus the guards that keep that membership and the comment-only
  # reader list (issue #5158) complete.
  checks-docs = pkgs.runCommand "checks-docs" { } ''
    ${pkgs.lib.concatMapStringsSep "\n" (p: ": ${p}") (
      builtins.attrValues checksDocsSet
      ++ [
        checks-docs-covers-docs-readers
        ci-comment-readers-cover-checks
      ]
    )}
    touch $out
  '';
}
