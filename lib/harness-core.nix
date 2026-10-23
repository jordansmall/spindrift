# The Harness core: the Go binaries and their fileset sources that depend on
# nothing a mkHarness caller varies (no revision, no knobs), so one evaluation
# serves every harness over the same toolset. lib/nixpkgs-shared.nix caches it
# per system beside the nixpkgs instances.
{ pkgs, hostPkgs }:
let
  inherit (pkgs) lib;
  buildConstants = import ./build-constants.nix;

  # Version sourced from the release-please manifest so mkHarness always tracks
  # the bot-maintained source of truth (ADR-0010).
  spindriftVersion = (builtins.fromJSON (builtins.readFile ../.release-please-manifest.json)).".";

  # internal/forge's own Go files without its backend subpackages (github,
  # forgejo, ...): the top level carries the shared verdict vocabulary
  # (promptassembly, issue #4159). The backends only the Box links come in
  # through backendBundleFiles.
  forgeTopLevelFiles = lib.fileset.unions (
    lib.mapAttrsToList (n: _: ../cmd/launcher/internal/forge + "/${n}") (
      lib.filterAttrs (n: t: t == "regular" && lib.hasSuffix ".go" n && !lib.hasSuffix "_test.go" n) (
        builtins.readDir ../cmd/launcher/internal/forge
      )
    )
  );

  # internal/driver's non-test Go files, minus the conformance kit: it imports
  # internal/golden and internal/passmachine, which no in-box fileset carries,
  # and `go mod vendor` loads every package in the tree, not just subPackages.
  # Nothing in-box imports it (Driver packages call it from _test.go only).
  # bundle/runners.go registers the runner families, which no Box constructs;
  # bundle/backends.go registers the backends, which only the Box looks up (the
  # Box adds it back through backendBundleFiles); bundle/ecosystems.go registers
  # the ecosystems, which only the Box and driver-exec bind (both add it back
  # through ecosystemBundleFile; the orchestrator never does).
  driverTreeFiles =
    lib.fileset.difference
      (lib.fileset.fileFilter (
        f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
      ) ../cmd/launcher/internal/driver)
      (
        lib.fileset.unions [
          ../cmd/launcher/internal/driver/driverkit/conformance
          ../cmd/launcher/internal/driver/bundle/runners.go
          ../cmd/launcher/internal/driver/bundle/backends.go
          ../cmd/launcher/internal/driver/bundle/ecosystems.go
        ]
      );

  ecosystemBundleFile = ../cmd/launcher/internal/driver/bundle/ecosystems.go;

  # Only the Box links backend adapters: it calls backendkit.ByName at runtime
  # for the clone URL, CLI setup and the recovery PR query (ADR 0068 decision
  # 1). backendBundleFiles is bundle/backends.go, which driverTreeFiles drops,
  # plus these adapters and their internal import closure (`go mod vendor`
  # loads every package in the tree). orchestrator and driver-exec use no
  # backend, so a backend edit leaves their drvPaths unchanged (issue #5738).
  backendBundleDirs = [
    "backendkit"
    "flock"
    "forge/bundlerelay"
    "forge/forgejo"
    "forge/git"
    "forge/github"
    "forge/gitplumbing"
    "forge/jira"
    "forge/local"
    "forge/rest"
    "gitexec"
    "gitremote"
    "seambundle"
  ];
  backendBundleFiles = lib.fileset.unions (
    [ ../cmd/launcher/internal/driver/bundle/backends.go ]
    ++ map (
      d:
      lib.fileset.fileFilter (f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name) (
        ../cmd/launcher/internal + "/${d}"
      )
    ) backendBundleDirs
  );

  # In-box Driver runner (issue #626): runs one Driver invocation, direct or
  # inside the Project devShell, tees the stream to a log path, and filters
  # heartbeats in-process, so there is one in-box Go unit rather than two.
  # Built for Linux (pkgs, not hostPkgs), and it goes through the Driver seam
  # (ADR 0009, issue #620) rather than a heartbeat package directly.

  # INVARIANT: the agent image drvPath must not change when host-side launcher
  # code outside this binary's import closure changes, so the fileset below is
  # deliberately tight and excludes *_test.go. An import added outside it
  # fails the build loudly with a missing package, which is the intended
  # failure mode (issue #474).
  driverExecBin = pkgs.buildGoModule {
    pname = "driver-exec";
    version = spindriftVersion;
    src = lib.fileset.toSource {
      root = ../cmd/launcher;
      fileset = lib.fileset.unions [
        ../cmd/launcher/go.mod
        ../cmd/launcher/go.sum
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/driver-exec)
        driverTreeFiles
        ecosystemBundleFile
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/driver/claude)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/driver/opencode)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/usage)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/landdelta)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/hostop)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/logscan)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/outcome)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/dispatchkind)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/retry)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/promptassembly)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/promptfence)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/passmachine)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/runstate)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/seedblock)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/bindregistry)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/ecosystem)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/registrymanifest)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/signalwire)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/signalclient)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/registryvocab)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/registryprobe)
        forgeTopLevelFiles
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/backend)
      ];
    };
    # Same go.mod/go.sum as launcherBin, but not the same vendorHash: `go mod
    # vendor` prunes to the packages the present source tree imports, and
    # driver-exec's fileset is narrower than launcherBin's full cmd/launcher
    # tree, so the two vendor differently (issue #784).
    vendorHash = buildConstants.driverExecVendorHash;
    subPackages = [ "driver-exec" ];
    meta.license = lib.licenses.mit;
  };

  # In-box Driver run and settle sequence (issues #4292, #4293, ADR 0058):
  # the entrypoint.sh shim execs this, so clone, prompt assembly and settle run
  # inside it. Same tight-fileset invariant as driverExecBin: the fileset is
  # exactly box's transitive internal import closure (`go list -deps ./box`),
  # so host-side launcher churn leaves the image drvPath alone. internal/driver
  # is walked recursively and so covers driverkit, claude, opencode and pi.
  boxBin = pkgs.buildGoModule {
    pname = "box";
    version = spindriftVersion;
    src = lib.fileset.toSource {
      root = ../cmd/launcher;
      fileset = lib.fileset.unions [
        ../cmd/launcher/go.mod
        ../cmd/launcher/go.sum
        (lib.fileset.fileFilter (f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name) ../cmd/launcher/box)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/homelayout)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/toolchain)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/registryvocab)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/registrymanifest)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/ecosystem)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/bindregistry)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/readonlyguards)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/dispatchkind)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/logscan)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/outcome)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/seambundle)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/bundleout)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/branchrecovery)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/boxclone)
        driverTreeFiles
        ecosystemBundleFile
        backendBundleFiles
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/landdelta)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/hostop)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/passmanifest)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/usage)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/signalwire)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/markergate)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/retry)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/runstate)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/outcomebackstop)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/backend)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/passmachine)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/promptfence)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/conflictresolve)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/promptassembly)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/signalclient)
        forgeTopLevelFiles
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/forge/gitplumbing)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/forge/bundlerelay)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/gitexec)
      ];
    };
    # Its own hash field: box's fileset vendors independently of the other
    # in-box binaries off the same go.mod/go.sum (issue #784).
    vendorHash = buildConstants.boxVendorHash;
    subPackages = [ "box" ];
    meta.license = lib.licenses.mit;
  };

  # In-box orchestrator (issue #1996, ADR 0007): the Go binary the box program
  # (cmd/launcher/box) hands the implementor pass off to instead of calling
  # driver-exec directly. Its fileset carries the same
  # import closure driverExecBin needs, plus the packages its own multi-pass
  # loop reaches for (issue #1998).
  orchestratorBin = pkgs.buildGoModule {
    pname = "orchestrator";
    version = spindriftVersion;
    src = lib.fileset.toSource {
      root = ../cmd/launcher;
      fileset = lib.fileset.unions [
        ../cmd/launcher/go.mod
        ../cmd/launcher/go.sum
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/orchestrator)
        driverTreeFiles
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/driver/claude)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/driver/opencode)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/usage)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/outcome)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/dispatchkind)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/logscan)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/runstate)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/seedblock)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/passmachine)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/promptassembly)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/promptfence)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/agentpaths)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/passmanifest)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/landdelta)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/hostop)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/deltareview)
        forgeTopLevelFiles
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/backend)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/retry)
      ];
    };
    # Its own hash, not driverExecBin's: the orchestrator's fileset (above)
    # has no internal/ecosystem, so it vendors none of the third-party
    # parsing dependencies that package's rows reach for.
    vendorHash = buildConstants.orchestratorVendorHash;
    subPackages = [ "orchestrator" ];
    meta.license = lib.licenses.mit;
  };

  # The whole module is the src of both launcherBin and daemonBin, not a
  # narrowed fileset like launcherCurrencyFileset below: the launcher's
  # _test.go files pull test-only deps (github.com/charmbracelet/x/exp/golden,
  # .../teatest) into go.sum, so trimming them out would vendor differently
  # and force a second vendorHash. docs/ sits outside the root, so a
  # docs-only commit moves neither src nor daemonBin (which bakes no
  # revision).
  launcherModuleSrc = lib.fileset.toSource {
    root = ../cmd/launcher;
    fileset = ../cmd/launcher;
  };

  # daemonBin is one input to the wrapper whose own $0
  # (SPINDRIFT_DAEMON_PROGRAM) is what runSlot's inline Tip.SelfPath
  # comparison (cmd/launcher/internal/daemon/loop.go) compares at each
  # iteration boundary (issue #3543); a mismatch halts a running daemon with
  # exit 10. Its src is the docs-free module source, so a docs-only commit
  # cannot halt a running daemon (issue #3621).
  daemonBin = hostPkgs.buildGoModule {
    pname = "spindrift-daemon";
    version = spindriftVersion;
    src = launcherModuleSrc;
    # Same src and vendorHash as launcherBin; only subPackages differs, and
    # subPackages does not change the vendored module set.
    vendorHash = buildConstants.launcherVendorHash;
    subPackages = [ "daemon" ];
    doCheck = false;
    # No ldflags: the binary bakes in no version or revision. But `version`
    # above still reaches the derivation name, so the path moves on a release
    # version bump as well as when the code it is built from moves.
    meta.license = lib.licenses.mit;
  };

  # A revision-independent sibling of launcherBin (issue #2677, ADR 0043):
  # launcherBin bakes `-X main.revision`, so its store path moves on every
  # commit. Staleness detection (issue #1364) needs a hash stable across
  # revision-only changes, so this one drops that ldflag. It is never
  # invoked; only its store hash is read.

  # src is scoped with lib.fileset to the launcher's own sources, so a
  # docs-only commit cannot move this hash.

  # The fileset is a directory-level approximation of the launcher's import
  # graph, not the graph itself: it subtracts the driver-exec, orchestrator,
  # quickstart and daemon subtrees (each an independent `package main` the
  # launcher never imports). internal/daemon stays in: `spindrift butler`
  # imports it for the awake window's zone (issue #3879). A reviewer found 13
  # directories included here that are outside the real import graph
  # (internal/testutil, for one), so perturbing those still moves this outPath
  # (issue #2677 review fix).
  launcherCurrencyFileset =
    lib.fileset.difference
      (lib.fileset.unions [
        ../cmd/launcher/go.mod
        ../cmd/launcher/go.sum
        (lib.fileset.fileFilter (f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name) ../cmd/launcher)
      ])
      (
        lib.fileset.unions [
          ../cmd/launcher/driver-exec
          ../cmd/launcher/orchestrator
          ../cmd/launcher/quickstart
          ../cmd/launcher/daemon
        ]
      );

  launcherCurrencySrc = lib.fileset.toSource {
    root = ../cmd/launcher;
    fileset = launcherCurrencyFileset;
  };

  launcherCurrencyBin = hostPkgs.buildGoModule {
    pname = "spindrift-launcher-currency";
    version = spindriftVersion;
    src = launcherCurrencySrc;
    # The narrower fileset vendors differently off the identical
    # go.mod/go.sum (#784), hence its own launcherCurrencyVendorHash rather
    # than launcherVendorHash.
    vendorHash = buildConstants.launcherCurrencyVendorHash;
    subPackages = [ "." ];
    doCheck = false;
    ldflags = [
      "-X main.version=${spindriftVersion}"
      # main.revision is intentionally omitted; see the comment above.
    ];
    meta.license = lib.licenses.mit;
  };
in
{
  # mkHarness compares this to its own cmd/launcher before reusing a cached
  # core: a checkset re-evaluated from a copied source tree must not pick up
  # binaries built from the original tree.
  root = ../cmd/launcher;
  inherit
    spindriftVersion
    backendBundleDirs
    driverExecBin
    boxBin
    orchestratorBin
    daemonBin
    launcherModuleSrc
    launcherCurrencyFileset
    launcherCurrencyBin
    ;
}
