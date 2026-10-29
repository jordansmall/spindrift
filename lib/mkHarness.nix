# mkHarness takes the locked nixpkgs input rather than a pre-built `pkgs` so it
# can map a darwin `system` to its Linux twin and re-instantiate for the OCI
# image, keeping the agent's toolchain and the Consumer's dev shell on one pin
# (ADR 0002). The image is target-agnostic: REPO_SLUG, auth, and commit
# identity stay runtime env, never Nix options (ADR 0001).
{
  nixpkgs,
  system,
  overlays ? [ ],
  config ? { },
  # The Consumer's own tools, baked into the image. A function of the (Linux)
  # pkgs so it stays correct on a darwin host.
  packages ? (_pkgs: [ ]),
  # Shell snippet box runs after cloning, to warm toolchain caches
  # (for example, fetching pinned deps). Baked into the image.
  prefetch ? "",
  # Baked into the image at /agent/prompts (see agentFiles), so changing it
  # requires an image rebuild. SPINDRIFT_PROMPT_DIR mounts an override
  # directory at runtime for zero-rebuild iteration; the mount itself lives in
  # cmd/launcher/internal/runner.
  prompt ? builtins.readFile ../templates/default/prompts/issue-prompt.md,
  scoutPrompt ? builtins.readFile ../templates/default/prompts/scout-prompt.md,
  reviewPrompt ? builtins.readFile ../templates/default/prompts/review-prompt.md,
  # One axis (Standards or Spec) of the /code-review skill's two-axis fan-out
  # spawns as this roster entry (issue #3447).
  reviewAxisPrompt ? builtins.readFile ../templates/default/prompts/review-axis-prompt.md,
  # Provisioned only when filerModel is non-empty (see agentsJsonTemplate).
  filerPrompt ? builtins.readFile ../templates/default/prompts/filer-prompt.md,
  # Provisioned by default (issue #2054: workerModel defaults to
  # claude-sonnet-5); empty only when workerModel is set to "".
  workerPrompt ? builtins.readFile ../templates/default/prompts/worker-prompt.md,
  # The N-agent roster (issue #264, lib/roster.nix), rendered by the selected
  # Driver into --agents JSON (claude) or on-disk agents/*.md (opencode).
  # `null` resolves to `rosterLib.defaultRoster` built from the four
  # deprecated model knobs, so a Consumer that has never heard of `roster`
  # keeps its old roster. An explicit `roster` ignores the legacy knobs.
  roster ? null,
  # Name-keyed model/effort shorthand (issue #2560), forwarded into
  # `rosterLib.defaultRoster`'s `byName` param. Takes effect only when
  # `roster` is null; an explicit `roster` always wins.
  byName ? { },
  conflictResolvePrompt ? builtins.readFile ../templates/default/prompts/conflict-resolve-prompt.md,
  # Used instead of `prompt` on a fix box (FIX_PASS>0): the branch is already
  # checked out, so this prompt skips scout/implement-from-scratch and goes
  # straight to check/fix/commit/push/watch-CI.
  fixPrompt ? builtins.readFile ../templates/default/prompts/fix-prompt.md,
  # Used instead of `prompt` when DISPATCH_KIND=research (ADR 0022,
  # issue #640): the researcher posts a verdict comment instead of
  # implementing the issue, so this prompt replaces the whole issue-prompt.md
  # flow rather than sharing its COMMS/CHECK blocks.
  researchPrompt ? builtins.readFile ../templates/default/prompts/research-prompt.md,
  # Used instead of `researchPrompt` in the self-contained research sub-mode
  # (ADR 0022, issue #2202): no repo and no clone, so this prompt skips the
  # EXPLORE step rather than sharing research-prompt.md's repo-exploration
  # prose.
  researchSelfContainedPrompt ? builtins.readFile ../templates/default/prompts/research-self-contained-prompt.md,
  # Used instead of `prompt` when DISPATCH_KIND=butler (ADR 0056, issue
  # #3875): a one-shot, read-only sweep of one Ledger Chore, never an issue,
  # so like researchPrompt this replaces the whole issue-prompt.md flow
  # rather than sharing its COMMS/CHECK/outcome blocks. Carries no harness-
  # owned contract injection of its own: its OUTCOME section is self-
  # contained in the template.
  butlerPrompt ? builtins.readFile ../templates/default/prompts/butler-prompt.md,
  # The butler's own in-Box reviewer prompt (ADR 0056, issue #3880): a butler
  # candidate's finding lives only in the delegation message, never a branch
  # diff, so its `reviewer` subagent renders from this file instead of
  # reviewPrompt above.
  butlerReviewPrompt ? builtins.readFile ../templates/default/prompts/butler-review-prompt.md,
  # The named-Chore prompt directory ${CHORE_PROMPT} resolves into (ADR
  # 0056): copied whole into the image, like fragmentsDir below, so a new
  # Chore needs no mkHarness plumbing, only a new file under it. Must stay
  # a plain path, not a derivation: choreCatalog below readDirs it, which
  # a derivation would turn into import-from-derivation.
  choresDir ? ../templates/default/prompts/chores,
  # The conditional fragment registry (issue #622): rows of (gate, fragment,
  # var) that box renders into the prompt from fragmentsRegistryJson. Not
  # Consumer-tunable; a new row needs no entrypoint edit.
  fragments ? import ./fragments.nix,
  # The directory the fragment registry's files live under, copied whole into
  # the image. Not Consumer-tunable; overridable here only so
  # nix/checks/prompts.nix (issue #2250) can point the
  # research-verdict-relay.md lookup at a broken fixture directory
  # without touching the real templates tree.
  fragmentsDir ? ../templates/default/prompts/fragments,
  # The dispatch kind rows (lib/dispatch-kinds), whose `prompts` axis lists
  # every kind prompt the image bakes (issue #5671). Not Consumer-tunable;
  # overridable here only so nix/checks/prompts.nix can add a stub row.
  dispatchKinds ? import ./dispatch-kinds,
  # Where a row prompt no Consumer parameter binds is read from; overridable
  # only so the same check can point a stub row at a fixture file.
  kindPromptsDir ? ../templates/default/prompts,
  # Skill files baked into the image at /agent/skills, alongside the
  # harness-owned skills. Each element is a path/derivation (copied under its
  # basename) or a { name; src; } content entry (issue #597) re-realized with
  # the image's own Linux pkgs, never a consumer host derivation, which would
  # tag the image's drvPath with the host system. The box program copies the
  # baked set into the Driver's runtime skills dir at startup, then copies
  # SPINDRIFT_SKILLS_DIR (staged at /operator-skills) over the top, so an
  # operator skill wins a name clash without erasing the baked set.
  skills ? [ ],
  # Non-secret run config baked into the `run` command as its built-in defaults;
  # a matching env var still wins at runtime, so one build can be re-pointed.
  defaults ? { },
  # Container runtime the launcher commands drive; the accepted values are the
  # runtimes the rows in lib/runners/default.nix list, and the default is the
  # first runtime of the first row.
  runtime ? builtins.head (import ./runners/index.nix { }).runtimes,
  # The flake output attribute the image (OCI) or agent closure (bwrap) is
  # exposed under, and the one the baked freshness probe realizes. A second
  # Harness in one flake needs its own name (ADR 0064, issue #5049).
  imageAttrName ? ((import ./runners/index.nix { }).forRuntime runtime).imageAttrName,
  # The agent CLI Driver (ADR 0009, issues #261/#262): selects one entry from
  # the lib/drivers/ registry, baked into the image (in-box half) and threaded
  # to the Go launcher as DRIVER (host-side half). "claude", "opencode" and "pi".
  driver ? "claude",
  # Set by lib/flakeModule.nix when more than one Driver is declared (ADR 0064,
  # issue #5053): the wrapper then layers harness.<driver>.env over harness.env.
  multiDriver ? false,
  # Fallback Linux builder for a host that cannot realize the Linux image
  # itself. Fully qualified so podman needs no default registry, and pinned by
  # manifest-list digest: this container runs with the consumer tree
  # bind-mounted read-write, so a silently-updated :latest could run someone
  # else's code against it. To bump, see docs/reference.md's "Bumping the pin".
  nixBuilderImage ? (import ./build-constants.nix).nixBuilderImage,
  # Bake a usable nix into the box (binary, a registered store DB, and a
  # single-user sandbox-off nix.conf) so `nix flake check` and `nix develop`
  # run inside the unprivileged throwaway container. Set false for a lean,
  # nix-free image.
  nixInBox ? true,
  # Self-test mode (ADR 0018, issue #469): makes the /nix/store directory (not
  # its root-owned contents) writable by the agent uid, so an in-Box `nix
  # flake check` can build new store paths instead of hitting EACCES. New
  # paths die with the Box. OCI bakes the chown into the image (lib/image.nix);
  # bwrap overlays an ephemeral tmpfs on the host's real store (ADR 0042).
  nixStoreWritable ? false,
  # Extra derivations whose closures are baked into the image and, when
  # nixInBox is on, registered in the store DB, so in-box nix sees them as
  # already present instead of cold-substituting them on every Box. A function
  # of the (Linux) pkgs, like `packages`, so Consumer-supplied derivations
  # stay correct on a darwin host (issue #469).
  extraClosures ? (_pkgs: [ ]),
  # Short git revision injected into the binary via ldflags for `spindrift --version`.
  # Callers pass self.shortRev or self.rev; defaults to "unknown" for impure builds.
  revision ? "unknown",
}:
let
  # Single source of truth for the vendorHash values and the nix-builder
  # digest otherwise duplicated across build sites (issue #784 / #2523).
  buildConstants = import ./build-constants.nix;

  nixpkgsShared = import ./nixpkgs-shared.nix;

  # OCI images are Linux-only, so the image always builds for the Linux twin.
  linuxSystem = nixpkgsShared.linuxTwin.${system};

  # The single param preambles.runArtifacts takes (issue #2770);
  # lib/preambles.nix explains the bundling.
  systems = {
    host = system;
    linux = linuxSystem;
  };

  mergedConfig = {
    allowUnfree = true;
  }
  // config;

  # `import nixpkgs { ... }` is not memoized, and spindrift's own checkset
  # calls mkHarness ~100 times per `nix flake check`. For the default toolset,
  # consult the shared per-system cache a `withSharedInstances`-wrapped input
  # carries (lib/nixpkgs-shared.nix). A Consumer passing `overlays` or
  # `config` cannot be cached: functions have no stable identity.
  defaultToolset = overlays == [ ] && config == { };
  instantiate =
    forSystem:
    if defaultToolset then
      (nixpkgsShared.sharedInstancesOf nixpkgs).${forSystem}
        or (nixpkgsShared.instantiate nixpkgs forSystem)
    else
      import nixpkgs {
        system = forSystem;
        inherit overlays;
        config = mergedConfig;
      };

  # Image toolset: the Consumer's locked nixpkgs, re-instantiated for Linux.
  pkgs = instantiate linuxSystem;

  # Host toolset: the launcher commands run on the Consumer's own system.
  # Takes the same overlays as the image so the tools pinned into the
  # launchers (gh/git/coreutils) can be overridden consistently.
  hostPkgs = if system == linuxSystem then pkgs else instantiate system;

  # The revision-free Go binaries and their sources (lib/harness-core.nix),
  # evaluated once per system for a wrapped nixpkgs and default toolset.
  core =
    let
      shared = (nixpkgsShared.sharedCoresOf nixpkgs).${system} or null;
    in
    if defaultToolset && shared != null && shared.root == ../cmd/launcher then
      shared
    else
      nixpkgsShared.harnessCore { inherit pkgs hostPkgs; };
  inherit (core)
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

  inherit (pkgs) lib;

  # Single source of truth for every runtime knob: name mapping, defaults,
  # scope. Generators below derive all per-knob output from this registry, so
  # no per-knob lines appear anywhere else in this file.
  schema = import ./env-schema.nix;
  driverMap = import ./driver-map.nix;

  # Single source of truth for the completion renderers' subcommand candidate
  # lists (issues #1575/#1577) and the man page SUBCOMMANDS section below.
  subcommandRegistry = import ./subcommands.nix;
  subcommands = subcommandRegistry;

  # One row per ISSUE_TRACKER/CODE_FORGE backend, carrying the capability bits
  # readOnlyCapabilityOk and codeForgeRow/issueTrackerRow below read
  # (issues #2521, #2527).
  backends = import ./backends/default.nix;

  # Section taxonomy and man-page renderer, shared with flakeModule.nix and
  # the nix/checks/schema-drift.nix guards so none of them can drift from each
  # other (issue #461).
  renderers = import ./renderers.nix;

  # Nix to bash preamble marshalling shared by the entrypoint and the Go
  # launcher wrappers below (issue #513); nix/checks/preambles.nix pins each
  # renderer's output shape.
  preambles = import ./preambles.nix;

  # Marker-delimited slicing/injection primitives (issue #512);
  # nix/checks/prompt-inject.nix pins each primitive's behavior.
  promptInject = import ./prompt-inject.nix;
  inherit (promptInject) sliceFromMarker injectSection;

  # Single source of truth for each harness-owned shared prompt block's
  # id/marker/source/slice-range/kinds (issues #2245/#2246), driving the
  # marker constants and canonical text below. The research block below is the
  # one exception that still slices its own source: it needs the
  # RESEARCH_VERDICTS-rendered text, not the registry's unrendered default.
  promptContract = import ./prompt-contract.nix;
  fragmentPaths = import ./fragment-paths.nix;
  # Shared per-file scan of the default prompt corpus (issue #5858).
  corpusScan = import ./prompt-corpus-scan.nix;
  # A source left at its default resolves to the shared per-file scan; an
  # override is scanned here. A prompt arg defaults to a readFile string, so
  # "left at default" is string equality. A fragment absent from the default
  # dir falls through to readFile and aborts eval; callers that tolerate
  # absence check pathExists first.
  promptScanOf =
    file: text:
    let
      cached = corpusScan.prompts.${file} or null;
    in
    if cached != null && cached.text == text then cached else corpusScan.scan text;
  fragmentScanOf =
    path:
    let
      scanned = corpusScan.scan (builtins.readFile (fragmentsDir + "/${path}"));
    in
    if fragmentsDir == corpusScan.fragmentsDir then
      corpusScan.fragments.${path} or scanned
    else
      scanned;
  inherit (promptContract) byId;

  # An alias for the fragmentsDir param above, kept so this file's existing
  # fragmentsSourceDir uses are unchanged (issue #463).
  fragmentsSourceDir = fragmentsDir;
  # Mirrors fragmentsSourceDir's alias, for choresDir (ADR 0056, issue #3875).
  choresSourceDir = choresDir;

  # The `<name>.md` stems lib/image.nix bakes under chores/, shared by the
  # CHORE_CATALOG run artifact (issue #3905) and butlerChoresPromptOk below
  # (issue #3991) so both read choresSourceDir once.
  choreStems = lib.pipe (builtins.readDir choresSourceDir) [
    (lib.filterAttrs (name: type: (type == "regular" || type == "symlink") && lib.hasSuffix ".md" name))
    builtins.attrNames
    (map (lib.removeSuffix ".md"))
  ];
  choreCatalog = lib.concatStringsSep " " choreStems;

  # The SPINDRIFT_OUTCOME contract is harness-owned (issue #419): a Consumer
  # `prompt` that drops it ships an agent that never emits the outcome line,
  # so the launcher never learns the PR and the merge silently never happens.
  # Sliced from the default prompt's own heading rather than duplicated into a
  # second file, so the injected block and the prompt cannot drift apart.
  outcomeContractMarker = (byId "outcome").marker;
  outcomeContract = promptContract.canonicalText.outcome;

  injectOutcomeContract = injectSection outcomeContractMarker outcomeContract;

  # COMMS and CHECK/COMMIT are the other two blocks fix-prompt.md used to
  # hand-copy from issue-prompt.md (issue #455), sliced the same way as the
  # outcome contract above so fix-prompt.md can drop them and receive the
  # byte-identical section at bake time instead.
  commsMarker = (byId "comms").marker;
  commsBlock = promptContract.canonicalText.comms;
  checkMarker = (byId "check").marker;
  checkBlock = promptContract.canonicalText.check;

  injectComms = injectSection commsMarker commsBlock;
  injectCheckCommit = injectSection checkMarker checkBlock;

  # COMMS, then CHECK/COMMIT, then the outcome contract, in that order, so a
  # fix prompt missing all three ends up with them in the order
  # issue-prompt.md carries them (issue #455). This mirrors the injection
  # order in cmd/launcher/internal/promptassembly, so the baked and
  # mounted-override cases agree.
  injectFixSharedBlocks =
    promptText: injectOutcomeContract (injectCheckCommit (injectComms promptText));

  # research-prompt.md carries its own harness-owned outcome contract
  # (issue #640) rather than sharing issue-prompt.md's blocks. Sliced from the
  # default research prompt's own "# POST THE VERDICT" heading through EOF, so
  # the injected block and that prompt's own copy cannot drift apart.
  researchPromptSource = builtins.readFile ../templates/default/prompts/research-prompt.md;
  researchOutcomeContractMarker = (byId "research-verdict").marker;
  # Render the verdict contract from the RESEARCH_VERDICTS knob (issue #2201)
  # before slicing the outcome contract and baking the prompt, so the default
  # set and a custom set both reach the baked prompt and the injected contract
  # through the same path (issue #2525). Even the default set is rendered, so
  # no case passes the template through byte for byte.
  researchVerdicts = import ./research-verdicts.nix;
  researchVerdictsKnob = mergedDefaults.researchVerdicts or "";
  researchPromptSourceRendered = researchVerdicts.render researchVerdictsKnob researchPromptSource;
  researchOutcomeContract = sliceFromMarker researchOutcomeContractMarker researchPromptSourceRendered;
  injectResearchOutcomeContract = injectSection researchOutcomeContractMarker researchOutcomeContract;

  # The work row's issue prompt role: keyed below and filtered on by the
  # forbidden-marker scan, so one binding keeps them in step.
  issueRole = "issue";

  # The one place a Consumer prompt parameter meets a dispatch kind row, keyed
  # by kind name then prompt role. A row prompt with no entry here is read
  # from kindPromptsDir instead (issue #5671).
  consumerKindPrompts =
    let
      bind = source: text: { inherit source text; };
    in
    {
      work = {
        ${issueRole} = bind "prompt" prompt;
        fix = bind "fixPrompt" fixPrompt;
      };
      research = {
        base = bind "researchPrompt" researchPrompt;
        selfContainedBase = bind "researchSelfContainedPrompt" researchSelfContainedPrompt;
      };
      butler = {
        base = bind "butlerPrompt" butlerPrompt;
        reviewer = bind "butlerReviewPrompt" butlerReviewPrompt;
      };
    };

  # What a row's Contract axis does to a prompt before it is baked: the
  # verdict set renders into research prompts (issue #2201), then each
  # harness-owned block is injected. A prompt the contract names nothing for
  # is baked verbatim.
  renderByContract.ContractVerdict = researchVerdicts.render researchVerdictsKnob;
  injectByContract = {
    ContractLanding = {
      ${issueRole} = injectOutcomeContract;
      fix = injectFixSharedBlocks;
    };
    ContractVerdict = {
      base = injectResearchOutcomeContract;
      selfContainedBase = injectResearchOutcomeContract;
    };
  };

  # A key in consumerKindPrompts, injectByContract or renderByContract that no
  # row declares would be skipped silently by the lookups below (a Consumer
  # override dropped, an injection omitted), so a rename or typo fails eval.
  kindPromptKeyStrays =
    let
      rowPrompts = lib.concatMap (
        row:
        map (role: {
          inherit (row) name contract;
          inherit role;
        }) (lib.attrNames (row.prompts or { }))
      ) dispatchKinds;
      declared = pred: lib.any pred rowPrompts;
      pathsOf =
        attrs:
        lib.concatMap (outer: map (inner: { inherit outer inner; }) (lib.attrNames attrs.${outer})) (
          lib.attrNames attrs
        );
    in
    map (p: "consumerKindPrompts.${p.outer}.${p.inner}") (
      lib.filter (p: !declared (r: r.name == p.outer && r.role == p.inner)) (pathsOf consumerKindPrompts)
    )
    ++ map (p: "injectByContract.${p.outer}.${p.inner}") (
      lib.filter (p: !declared (r: r.contract == p.outer && r.role == p.inner)) (pathsOf injectByContract)
    )
    ++ map (c: "renderByContract.${c}") (
      lib.filter (c: !declared (r: r.contract == c)) (lib.attrNames renderByContract)
    );

  # Forces kindPromptKeyStrays, the way forbiddenMarkerCheckOk forces its
  # violations; asserted ahead of the returned attrset.
  kindPromptKeysOk =
    if kindPromptKeyStrays == [ ] then
      true
    else
      throw "mkHarness: kind prompt binding names no dispatch kind row prompt (renamed or misspelled row, role or contract?): ${lib.concatStringsSep ", " kindPromptKeyStrays}";

  # Every prompt every dispatch kind row declares: `raw` is the Consumer text
  # (or the template), `text` has the Contract's rendering applied, `baked` is
  # what lands in /agent/prompts.
  kindPrompts = lib.concatMap (
    row:
    lib.mapAttrsToList (
      role: file:
      let
        bound =
          consumerKindPrompts.${row.name}.${role} or {
            source = file;
            text = builtins.readFile (kindPromptsDir + "/${file}");
          };
        text = (renderByContract.${row.contract} or lib.id) bound.text;
      in
      {
        kind = row.name;
        inherit (row) contract;
        inherit role file text;
        inherit (bound) source;
        raw = bound.text;
        baked = (injectByContract.${row.contract}.${role} or lib.id) text;
      }
    ) (row.prompts or { })
  ) dispatchKinds;

  # driverEntry is the selected Driver's in-box half (ADR 0009): invocation
  # binary and flags, agent-config rendering, skill wiring, and outcome
  # extraction, baked into the image below.
  driverRegistry = import ./drivers/default.nix { inherit lib; };
  driverEntry =
    driverRegistry.entries.${driver}
      or (throw "mkHarness: unknown driver '${driver}'; known drivers: ${lib.concatStringsSep ", " (lib.attrNames driverRegistry.entries)}");

  # The OCI image name, scoped to the selected Driver (issue #262). The claude
  # Driver keeps the historical `spindrift` name so existing tags and bats
  # fixtures are unchanged. Threaded through image.nix and preambles so the
  # image, its content-hash tag, and the launcher's re-tag all agree.
  imageName = if driver == "claude" then "spindrift" else "spindrift-${driver}";

  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Built-in run defaults from the schema; the Consumer's `defaults` arg
  # overrides them per key, and a matching env var overrides those at runtime.
  # Non-strict (issue #2506): most flakeOption keys have no model concept and
  # cannot guarantee a `.default`, unlike the roster helper's four model keys.
  schemaDefaults = rosterSchemaDefaults.readSchemaDefaults { strict = false; } flakeOptionEntries;
  mergedDefaults = schemaDefaults // defaults;

  # The two registry rows the selected CODE_FORGE and ISSUE_TRACKER values
  # pick out (issue #2527), so the capability bits below do not re-state
  # per-backend facts. Falls back to `{ }` on an unregistered name (every
  # capability bit then reads false) rather than throwing: Go's own validate()
  # already rejects an invalid CODE_FORGE/ISSUE_TRACKER at runtime.
  codeForgeRow = lib.findFirst (r: r.name == mergedDefaults.codeForge) { } backends;
  issueTrackerRow = lib.findFirst (r: r.name == mergedDefaults.issueTracker) { } backends;

  # Threaded into the Launcher input document's `run` artifacts as
  # HOST_MEDIATED_REMOTE / OUTBOX_RELAY_CAPABLE / IN_BOX_UNREACHABLE_TRACKER /
  # FULLY_LOCAL (issue #2527), so the Go side reads them from the document
  # instead of re-deriving backend facts itself.
  hostMediatedRemote = codeForgeRow.hostMediatedRemote or false;
  outboxRelayCapable = codeForgeRow.outboxRelayCapable or false;
  inBoxUnreachableTracker = issueTrackerRow.inBoxUnreachableTracker or false;
  fullyLocal = hostMediatedRemote && inBoxUnreachableTracker;

  # Threaded into the Launcher input document's `run` artifacts as
  # TRACKER_AXIS_READ / TRACKER_AXIS_WRITE / TRACKER_AXIS_FILER /
  # FORGE_BACKEND (issue #2533). cmd/launcher/internal/backend's
  # TrackerAxisSignals/ForgeBackendSignal read the same
  # lib/backends/default.nix row fields, so neither side switches by hand.
  trackerAxisRead = issueTrackerRow.trackerAxisRead or "GITHUB";
  trackerAxisWrite = issueTrackerRow.trackerAxisWrite or "GITHUB";
  trackerAxisFiler = issueTrackerRow.trackerAxisFiler or "GH";
  forgeBackend = codeForgeRow.forgeBackend or "GH";

  # Eval-time choices guard (issue #2519): flakeModule.nix's generated options
  # use `types.enum`, but that only protects Consumers going through the flake
  # module. A Consumer calling mkHarness directly could otherwise set an
  # invalid choice value. This validates the resolved mergedDefaults value, at
  # the one point both entry paths funnel through.
  choiceViolations = lib.filter (issue: issue != null) (
    lib.mapAttrsToList (
      key: entry:
      let
        choices = entry.choices or null;
        value = mergedDefaults.${key} or null;
        # toString null == "" would otherwise render as an empty-quoted
        # value ("") indistinguishable from a legitimate empty string,
        # hiding exactly which value was rejected.
        displayValue = if value == null then "null" else "\"${toString value}\"";
      in
      if choices == null || lib.elem value choices then
        null
      else
        "${entry.env or key}=${displayValue} (valid: ${lib.concatStringsSep ", " choices})"
    ) flakeOptionEntries
  );
  choicesCheckOk =
    if choiceViolations == [ ] then
      true
    else
      throw "mkHarness: invalid choice value(s) for ${lib.concatStringsSep "; " choiceViolations}";

  # NETWORK_MODE coherence (issue #2562): network.mode and the raw per-runtime
  # knobs say the same thing with no precedence rule between them, so a
  # Consumer that sets both must pick one. The mode must also be one the
  # runtime's runner row (lib/runners) lists in `networkModes`; why a family
  # omits a mode is documented on its row.
  networkModeCoherenceOk =
    if
      (defaults ? networkMode)
      && ((mergedDefaults.podmanNetwork or "" != "") || (mergedDefaults.bwrapUnshareNet or false))
    then
      throw "mkHarness: network.mode=${mergedDefaults.networkMode} is set together with a raw network knob (network.podman/network.bwrapUnshare) -- there is no precedence rule between them, so the Consumer must pick one"
    else if !(builtins.elem (mergedDefaults.networkMode or "open") runnerRow.networkModes) then
      throw "mkHarness: ${
        runners.unsupportedNetworkModeMessage runnerRow (mergedDefaults.networkMode or "open")
      }"
    else
      true;

  # nixStoreWritable needs nixInBox (issue #6136): the OCI image's nix/store
  # directory (which lib/image.nix chowns) only exists inside the nixInBox
  # block, and bwrap would silently ignore the knob. Checked here rather than
  # in image.nix so it fires on every runner, not just the ones that build it.
  nixStoreWritableCoherenceOk =
    if nixStoreWritable && !nixInBox then
      throw "mkHarness: nixStoreWritable (infra.nix.storeWritable) requires nixInBox (infra.nix.inBox) -- without in-box nix there is no store DB or nix.conf, so a writable /nix/store has no effect; set nixInBox = true or drop nixStoreWritable"
    else
      true;

  # The backend-specific CLI packages (nixpkgs attribute names) the selected
  # tracker and forge rows declare (issue #1963), which lib/image.nix bakes so
  # an image never carries a CLI its backends do not use. An unregistered
  # backend name contributes none.
  backendCLIPackages = lib.unique (
    lib.concatMap (row: lib.optional (row ? cliPackage) row.cliPackage) [
      issueTrackerRow
      codeForgeRow
    ]
  );

  defaultsKeyError = import ./defaults-key-error.nix {
    inherit lib;
    validKeys = lib.attrNames flakeOptionEntries;
  } defaults;
  removedFragmentVars = import ./removed-fragment-vars.nix;

  # An explicit `roster` always wins; otherwise it resolves from the four
  # deprecated per-agent model knobs (issue #264, lib/roster.nix).
  rosterLib = import ./roster.nix { inherit lib; };
  skillsLib = import ./skills.nix;
  # The one schema-defaults reader (issue #2506), also used above in
  # non-strict mode. It is a separate file so roster.nix can import it without
  # importing mkHarness.nix; lib/roster-schema-defaults.nix explains why.
  rosterSchemaDefaults = import ./roster-schema-defaults.nix { inherit lib; };
  resolvedRoster = rosterLib.normalizeRoster (
    if roster != null then
      roster
    else
      rosterLib.defaultRoster {
        scoutModel = mergedDefaults.scoutModel or "";
        reviewModel = mergedDefaults.reviewModel or "";
        filerModel = mergedDefaults.filerModel or "";
        workerModel = mergedDefaults.workerModel or "";
        inherit byName;
      }
  );
  # The #392 opt-out: drops any entry whose model is the explicit "" sentinel,
  # after normalizeRoster (which never filters) and before any Driver or
  # downstream consumer of finalRoster sees the roster (issue #2571).
  keptRoster = rosterLib.dropOptedOut resolvedRoster;

  # A Driver with `subagents = false` provisions no subagent yet, so only the
  # reviewer stays: the orchestrator's own review pass reads its model and
  # effort. Every subagent role drops, which bakes SCOUT_PROVISIONED,
  # WORKER_PROVISIONED and FILER_ENABLED false. Dropping before the
  # namespaced-model check keeps the inherited bare claude ids on those roles
  # from failing the build.
  provisionedRoster =
    if driverEntry.subagents or true then
      keptRoster
    else
      lib.filter (e: e.name == "reviewer") keptRoster;

  # A provisioned worker makes the coordinator delegate every IMPLEMENT slice
  # to it, so a worker on a model the Provider cannot serve breaks the whole
  # implement phase. Under a Driver with Provider-namespaced model ids, drop a
  # worker that lacks the "/" so neither the bake nor WORKER_PROVISIONED
  # claims it (issue #5331).
  providerNamespacedModelsLib = import ./provider-namespaced-models.nix { inherit lib; };
  bareWorker = e: e.name == "worker" && !(providerNamespacedModelsLib.namespaced e.model);
  rosterWithoutBareWorker =
    if driverEntry.providerNamespacedModels or false then
      lib.filter (e: !bareWorker e) provisionedRoster
    else
      provisionedRoster;

  # Every other model, the default included, must be namespaced too: the
  # inherited claude ids are bare and a Provider-namespaced Driver would reject them at run time,
  # so a bare one fails the build (issue #5355). The worker is exempt above.
  bareModelOffenders =
    if driverEntry.providerNamespacedModels or false then
      providerNamespacedModelsLib.offenders {
        roster = rosterWithoutBareWorker;
        defaultModel = mergedDefaults.model or "";
      }
    else
      [ ];
  bareModelCheckOk =
    if bareModelOffenders == [ ] then
      true
    else
      throw (lib.concatMapStringsSep "\n" providerNamespacedModelsLib.message bareModelOffenders);

  # reviewEffort (issue #2512) is the one legacy knob that overrides an
  # already-resolved roster's reviewer entry whatever the roster's source,
  # unlike the four model knobs above. Applied post-normalize so it reaches
  # the defaultRoster branch and an explicit roster identically.
  effortAppliedRoster =
    let
      reviewEffort = mergedDefaults.reviewEffort or "";
    in
    if reviewEffort == "" then
      rosterWithoutBareWorker
    else
      map (
        e: if e.name == "reviewer" then e // { effort = reviewEffort; } else e
      ) rosterWithoutBareWorker;

  # A Driver whose effort rides on the model validates each role's effort level
  # and folds an existing `:<level>` suffix here, after the opt-out drop and
  # reviewEffort, so the agent files, roleModels and flags all see the result
  # (issues #5483, #5811).
  finalRoster =
    if driverEntry ? effortOnModelLevels then
      (import ./effort-on-model.nix { inherit lib; }).applyEffort {
        roster = effortAppliedRoster;
        levels = driverEntry.effortOnModelLevels;
      }
    else
      effortAppliedRoster;

  # --agents JSON, rendered by the selected Driver (ADR 0009) so a Driver with
  # a different agent-config shape (opencode's agents/*.md) can supply its own
  # renderer without touching mkHarness.
  agentsJsonTemplate = driverEntry.agentsJsonTemplate { roster = finalRoster; };

  # Threaded into the Launcher input document's `run` artifacts as
  # FILER_ENABLED / WORKER_PROVISIONED / SCOUT_PROVISIONED (issues #2533,
  # #3157), so the Go side reads them from the document instead of
  # re-deriving roster membership itself.
  #
  # All three key off finalRoster rather than agentsJsonTemplate's parsed
  # output: opencode's agentsJsonTemplate always returns "" and provisions
  # subagents through driverAgentFiles (rendered by lib/drivers/opencode.nix's
  # agentFilesTemplate), so a check keyed off that JSON would wrongly read
  # false for an opencode box that does carry them (issues #5330, #5331). The
  # bake and the flag therefore cannot disagree under any Driver.
  scoutProvisioned = lib.any (e: e.name == "scout") finalRoster;
  workerProvisioned = lib.any (e: e.name == "worker") finalRoster;
  filerEnabled = lib.any (e: e.name == "filer") finalRoster;

  # Stamped onto every Pass log's dispatch_start (issue #4783). The Launcher
  # overlays the runtime-switchable "main" and reviewer models itself.
  roleModels = builtins.toJSON (
    builtins.listToAttrs (
      map (e: lib.nameValuePair e.name e.model) (lib.filter (e: (e.model or "") != "") finalRoster)
    )
  );
  driverVersion = (driverEntry.package pkgs).version or "";

  # On-disk files rendered by the selected Driver: subagent files and, for
  # opencode, the Bash guard plugin (issue #5346). A Driver with no on-disk
  # agent-config mechanism (claude.nix) returns { } here; its subagents ride
  # agentsJsonTemplate's --agents JSON flag instead.
  driverAgentFiles = driverEntry.agentFilesTemplate { roster = finalRoster; };
  driverConfigFiles = driverEntry.configFiles or { };

  # Name to prompt-file map (issue #264), read at runtime by the per-agent
  # prompt injection in cmd/launcher/internal/promptassembly so a custom
  # agent's prompt resolves the same way as the built-in names. normalizeRoster
  # guarantees every entry carries a promptFile (issue #2152), so there is no
  # fallback to re-derive.
  agentsPromptFilesJson = builtins.toJSON (
    lib.listToAttrs (
      map (e: {
        name = e.name;
        value = e.promptFile;
      }) finalRoster
    )
  );

  # Custom roster entries carrying their own prompt, baked into the image
  # alongside the fixed prompt files. An entry omitting `prompt`, or setting it
  # to null or "", is treated as having no inline prompt (issue #264 review
  # finding); "" in the copy loop would overwrite a read-only shipped prompt.
  customRosterPromptFiles = lib.filter rosterLib.hasInlinePrompt finalRoster;

  # The Driver's in-box half, rendered by the registry (issue #624) into
  # agent/entrypoint.sh's DRIVER_* vars and function definitions (ADR 0009),
  # shared between the image preamble and the bats harness file (issue #433)
  # so neither can drift from the other.
  driverPreamble = driverRegistry.renderPreamble driverEntry;

  # The baked /agent/* path literals and their fallback-preserving preamble
  # (issue #2531). lib/image.nix's agentFiles copy destinations read the same
  # binding, so a rename here updates the image and the entrypoint together.
  agentPaths = import ./agent-paths.nix;
  agentPathsPreamble = preambles.renderAgentPathsPreamble agentPaths;

  # The registry as JSON (issue #2354), for box's `--registry` flag.
  fragmentsRegistryJson = builtins.toJSON fragments;

  # lib/prompt-contract.nix's validateMarkers list as JSON (issue #2356), for
  # box's `--validate-markers-registry` flag.
  promptContractRegistryJson = builtins.toJSON promptContract.validateMarkers;

  # lib/prompt-contract.nix's forbiddenMarkers list as JSON (issue #2464), for
  # box's `--forbidden-markers-registry` flag (the read-only guards);
  # assemble-prompt no longer takes it (issue #2513).
  forbiddenMarkersRegistryJson = builtins.toJSON promptContract.forbiddenMarkers;

  # Build-time reject arm (issue #2250): resolves both validateMarkers
  # "reject" rows against this build's static knowledge. The relay fragment is
  # one shared root file for every tracker whose tracker-axis folder has no
  # relay file of its own; a tracker whose folder carries one resolves per
  # arm, so only the former name a file to check. For the rest the id is
  # omitted from contentByRowId and the row resolves to "advise".
  # buildTimeRejectOk below forces the list.
  researchRelayChecksFragment =
    issueTrackerRow ? name
    && !builtins.pathExists (
      fragmentsDir + "/${fragmentPaths.folderOf "tracker" issueTrackerRow}/research-verdict-relay.md"
    );
  buildTimeRejectVerdicts = promptContract.buildTimeRejectVerdicts {
    staticGates = {
      reviewPrompt = true;
      readOnlyResearch = mergedDefaults.boxForgeAndIssueAccess == "read-only";
    };
    contentByRowId = {
      "reviewer-verdict" = reviewPrompt;
    }
    // lib.optionalAttrs researchRelayChecksFragment {
      "verdict-comment-relay" = builtins.readFile (fragmentsDir + "/research-verdict-relay.md");
    };
  };

  # One spelling of "is this a FILER_FILE_DIRECT*-gated row", shared by
  # readOnlyReachableFragmentRows and directFileFragmentRows below
  # (issue #2595). Two spellings meant a new FILER_FILE_DIRECT_* gate could
  # miss one list and wrongly stay inside the forbidden-marker scan.
  isDirectFileGate = row: lib.hasInfix "FILER_FILE_DIRECT" row.gate;

  # The fragment rows the forbidden-marker scan reaches (issue #2510): every
  # row except those whose gate name already proves the fragment is
  # access-mode-aware, so a legitimate negation ("do NOT `git push`") in the
  # read-only half of a pair is never mistaken for a leak. Unconditional: such
  # a marker in the corpus is a problem for any Consumer, not just this build.
  readOnlyReachableFragmentRows = builtins.filter (
    row:
    !(
      lib.hasInfix "READ_ONLY" row.gate
      || lib.hasInfix "READONLY" row.gate
      || lib.hasInfix "READWRITE" row.gate
      || lib.hasInfix "READ_WRITE" row.gate
      || lib.hasInfix "_RW_" row.gate
      || isDirectFileGate row
    )
  ) fragments;

  # Every non-exempt fragment's raw content, plus the three shared templates'
  # unsubstituted text, scanned for any forbiddenMarkers "substring" row. See
  # lib/prompt-contract.nix's buildTimeForbiddenMarkerViolations for the design.
  forbiddenMarkerViolations = promptContract.buildTimeForbiddenMarkerViolations {
    fragmentHitsByFile = builtins.listToAttrs (
      builtins.concatMap (
        row:
        map
          (path: {
            name = path;
            value = (fragmentScanOf path).forbidden;
          })
          # An axis row's per-backend files and a deprecated row's files may
          # be absent from an old-layout fragmentsDir (a test fixture); a
          # plain row's absence still aborts eval.
          (
            builtins.filter (
              path: !(fragmentPaths.mayBeAbsent row) || builtins.pathExists (fragmentsDir + "/${path}")
            ) (fragmentPaths.scanPathsOf fragmentsDir row)
          )
      ) readOnlyReachableFragmentRows
    );
    # Just these three: issue #2510 scopes the shared-template half of this
    # rule to the issue, review and filer prompts by name. fix-prompt.md and
    # the research prompts also carry forbiddenMarkers substrings, but
    # bringing them under this scan is out of scope. The issue prompt is
    # whatever file the rows give the `issue` role.
    templateHitsByFile = {
      "review-prompt.md" = (promptScanOf "review-prompt.md" reviewPrompt).forbidden;
      "filer-prompt.md" = (promptScanOf "filer-prompt.md" filerPrompt).forbidden;
    }
    // lib.listToAttrs (
      map (e: lib.nameValuePair e.file (promptScanOf e.file e.raw).forbidden) (
        builtins.filter (e: e.role == issueRole) kindPrompts
      )
    );
  };

  # Forces forbiddenMarkerViolations' evaluation, the way buildTimeRejectOk
  # forces buildTimeRejectVerdicts; asserted ahead of the returned attrset.
  forbiddenMarkerCheckOk =
    if forbiddenMarkerViolations == [ ] then
      true
    else
      throw "mkHarness: structural forbidden-marker check failed -- a forbidden marker (lib/prompt-contract.nix forbiddenMarkers) must live only in a gate-paired fragment (issue #2510):\n${
        lib.concatMapStringsSep "\n" (
          v: "  ${v.file}: contains forbidden marker '${v.marker}' (${v.id})"
        ) forbiddenMarkerViolations
      }";

  # A removed fragment variable still written as a literal ${NAME} in a
  # Consumer-overridable prompt arg, fragment file or chore file renders as that
  # literal text (issue #4401); a deprecated one is collected here too and only
  # warns (deprecatedFragmentVarWarnings). Scans the prompt args, each lib/fragments.nix
  # row's fragment file present under fragmentsDir, and the top-level *.md
  # files under choresDir, so it fires regardless of which gates a build turns
  # on. Registry and rationale: lib/removed-fragment-vars.nix.
  removedFragmentVarHits =
    let
      promptArgs = {
        scoutPrompt = promptScanOf "scout-prompt.md" scoutPrompt;
        reviewPrompt = promptScanOf "review-prompt.md" reviewPrompt;
        reviewAxisPrompt = promptScanOf "review-axis-prompt.md" reviewAxisPrompt;
        filerPrompt = promptScanOf "filer-prompt.md" filerPrompt;
        workerPrompt = promptScanOf "worker-prompt.md" workerPrompt;
        conflictResolvePrompt = promptScanOf "conflict-resolve-prompt.md" conflictResolvePrompt;
      }
      // lib.listToAttrs (map (e: lib.nameValuePair e.source (promptScanOf e.file e.raw)) kindPrompts);
      labelled = label: attrs: lib.mapAttrs' (n: v: lib.nameValuePair "${label} ${n}" v) attrs;
      fragmentFiles = builtins.listToAttrs (
        # A fragment missing from a Consumer fragmentsDir renders empty, so
        # skip it rather than abort eval on a file-not-found.
        map
          (path: {
            name = path;
            value = fragmentScanOf path;
          })
          (
            builtins.filter (path: builtins.pathExists (fragmentsDir + "/${path}")) (
              builtins.concatMap (fragmentPaths.scanPathsOf fragmentsDir) fragments
            )
          )
      );
      choreFiles =
        if choresDir == corpusScan.choresDir then corpusScan.chores else corpusScan.scanDir choresDir;
      sources =
        labelled "prompt arg" promptArgs
        // labelled "fragment file" fragmentFiles
        // labelled "chore file" choreFiles;
    in
    lib.concatMap (
      source:
      map (name: {
        inherit source name;
        inherit (removedFragmentVars.${name}) issue;
        severity = removedFragmentVars.${name}.severity or "removed";
        replacement = removedFragmentVars.${name}.replacement or null;
      }) (lib.attrNames (builtins.intersectAttrs sources.${source}.vars removedFragmentVars))
    ) (lib.attrNames sources);

  removedFragmentVarViolations = builtins.filter (v: v.severity == "removed") removedFragmentVarHits;

  # A deprecated var only warns (through warnAll); the launcher repeats the
  # warning at run time. One line per variable, like the launcher, however many
  # sources name it. Exposed through `internals` so a check can assert on it
  # without capturing stderr.
  deprecatedFragmentVarWarnings =
    let
      hits = builtins.filter (v: v.severity == "deprecated") removedFragmentVarHits;
    in
    lib.optional (hits != [ ])
      "mkHarness: a prompt arg, fragment file or chore file references a deprecated fragment variable, slated for removal (see MIGRATING.md):\n${
        lib.concatMapStringsSep "\n" (
          name:
          let
            named = builtins.filter (v: v.name == name) hits;
            first = builtins.head named;
          in
          "  \${${name}} in ${
              lib.concatMapStringsSep ", " (v: v.source) named
            } (deprecated in issue #${toString first.issue}); use \${${first.replacement}} instead"
        ) (lib.unique (map (v: v.name) hits))
      }";

  removedFragmentVarCheckOk =
    if removedFragmentVarViolations == [ ] then
      true
    else
      throw "mkHarness: a prompt arg, fragment file or chore file references a removed fragment variable, which would render as literal text (see MIGRATING.md):\n${
        lib.concatMapStringsSep "\n" (
          v: "  ${v.source}: \${${v.name}} (removed in issue #${toString v.issue})"
        ) removedFragmentVarViolations
      }";

  # The FILER_FILE_DIRECT*-gated fragment rows (issue #2595, ADR 0041): the
  # ones telling the agent to run `gh issue create` and friends directly,
  # never rendered into a research prompt by design. lib/fragments.nix's
  # research-file-issues-relay.md row says why.
  directFileFragmentRows = builtins.filter isDirectFileGate fragments;

  # The research prompts scanned for a direct-file placeholder (issue #2595):
  # every prompt of a row whose Contract renders the verdict set into it (a key
  # of renderByContract). Named so nix/checks/prompts.nix can read it back
  # through `internals` below and assert its keys still cover every
  # research*-prompt.md file on disk.
  researchPromptContentByName = lib.listToAttrs (
    map (e: lib.nameValuePair e.file e.text) (
      builtins.filter (e: renderByContract ? ${e.contract}) kindPrompts
    )
  );

  researchDirectFileViolations = promptContract.buildTimeResearchDirectFileViolations {
    inherit directFileFragmentRows researchPromptContentByName;
  };

  # Forces researchDirectFileViolations' evaluation, the way
  # forbiddenMarkerCheckOk does; asserted ahead of the returned attrset.
  researchDirectFileCheckOk =
    if researchDirectFileViolations == [ ] then
      true
    else
      throw "mkHarness: a research prompt must never render a direct-file filing fragment (ADR 0041, docs/adr/0041-research-filing-is-host-mediated-and-relay-only.md: research filing is host-mediated and relay-only) -- issues are filed via the SPINDRIFT_ISSUE_INTENT relay, never gh/fj directly, in a research dispatch:\n${
        lib.concatMapStringsSep "\n" (
          v: "  ${v.promptName}: references direct-file fragment '${v.fragment}' via \${${v.var}}"
        ) researchDirectFileViolations
      }";

  # The image build lives in lib/image.nix (issue #514); the image derivation
  # must stay byte-identical, so every value the module needs is threaded in
  # exactly as it was computed here.

  # The host-native mirror derivations further down this file (promptDir,
  # driverPreambleFile, runArtifacts, and others) read the same fields off the
  # parameter groups below instead of re-deriving them from the bare locals.
  imagePackageSet = {
    inherit packages extraClosures;
  };
  imageDriver = {
    inherit
      driverEntry
      driverExecBin
      orchestratorBin
      boxBin
      driverPreamble
      driverAgentFiles
      driverConfigFiles
      ;
  };
  imageAgents = {
    inherit
      agentsJsonTemplate
      agentsPromptFilesJson
      customRosterPromptFiles
      ;
    # Validated here so both bake sites (skillsDir below, lib/image.nix) inherit it.
    skills = skillsLib.validateSkills skills;
  };
  imageContracts = {
    inherit
      agentPaths
      agentPathsPreamble
      fragmentsRegistryJson
      promptContractRegistryJson
      forbiddenMarkersRegistryJson
      outcomeContract
      commsBlock
      checkBlock
      researchOutcomeContract
      ;
  };
  imagePrompts = {
    inherit
      scoutPrompt
      reviewPrompt
      reviewAxisPrompt
      filerPrompt
      workerPrompt
      conflictResolvePrompt
      fragmentsSourceDir
      choresSourceDir
      ;
    kindPrompts = map (e: { inherit (e) file baked; }) kindPrompts;
  };
  imageKnobs = {
    inherit
      nixInBox
      nixStoreWritable
      backendCLIPackages
      prefetch
      imageName
      ;
    entrypointDefaultsPreamble = renderDefaultsPreamble { };
  };

  imageModule = import ./image.nix {
    inherit pkgs lib;
    packageSet = imagePackageSet;
    driver = imageDriver;
    agents = imageAgents;
    contracts = imageContracts;
    prompts = imagePrompts;
    knobs = imageKnobs;
  };
  inherit (imageModule)
    image
    agentEnv
    agentFiles
    passwdFile
    groupFile
    nixConfigFile
    syscallFilter
    ;

  # The canonical outcome contract as a host store path, so checks can diff it
  # against what a Consumer prompt lacking the contract gets injected with.
  # That is the proof the two cannot drift apart (issue #419).
  outcomeContractFile = hostPkgs.writeText "outcome-contract.md" imageContracts.outcomeContract;

  # The COMMS and CHECK/COMMIT blocks as host store paths, for the same
  # drift-proof reason (issue #455).
  commsContractFile = hostPkgs.writeText "comms-contract.md" imageContracts.commsBlock;
  checkContractFile = hostPkgs.writeText "check-contract.md" imageContracts.checkBlock;

  # The research dispatch kind's own outcome contract as a host store path,
  # for the same drift-proof reason (issue #640).
  researchOutcomeContractFile = hostPkgs.writeText "research-outcome-contract.md" imageContracts.researchOutcomeContract;

  # The Driver's registry-rendered preamble as a host store-path file. The
  # bats harness prepends it before exec-ing the entrypoint (issues
  # #433/#624) so tests exercise the exact bytes mkHarness bakes into the
  # image, not hand-copied duplicates or entrypoint fallback literals.
  driverPreambleFile = hostPkgs.writeText "driver-preamble.sh" imageDriver.driverPreamble;

  # The baked /agent/* path literals' fallback preamble as a host store-path
  # file (issue #2531), prepended by the bats harness for the same reason, so
  # tests do not run an entrypoint with no default for these vars at all.
  agentPathsPreambleFile = hostPkgs.writeText "agent-paths-preamble.sh" agentPathsPreamble;

  # The rendered prompt directory as a host store path (native-buildable on
  # darwin, so it needs no Linux builder). The prompt is normally baked into
  # the image via agentFiles; this output exists so tests can assert it is NOT
  # bind-mounted by default, and so SPINDRIFT_PROMPT_DIR can point to it.
  promptDir = hostPkgs.runCommand "prompt-dir" { } ''
    mkdir -p $out
    ${lib.concatMapStrings (
      e: "cp ${hostPkgs.writeText e.file e.baked} $out/${e.file}\n"
    ) imagePrompts.kindPrompts}
    cp ${hostPkgs.writeText "scout-prompt.md" imagePrompts.scoutPrompt} $out/scout-prompt.md
    cp ${hostPkgs.writeText "review-prompt.md" imagePrompts.reviewPrompt} $out/review-prompt.md
    cp ${hostPkgs.writeText "review-axis-prompt.md" imagePrompts.reviewAxisPrompt} $out/review-axis-prompt.md
    cp ${hostPkgs.writeText "filer-prompt.md" imagePrompts.filerPrompt} $out/filer-prompt.md
    cp ${hostPkgs.writeText "worker-prompt.md" imagePrompts.workerPrompt} $out/worker-prompt.md
    ${lib.concatMapStrings (
      e:
      let
        pf = e.promptFile;
      in
      "cp ${hostPkgs.writeText pf e.prompt} $out/${pf}\n"
    ) imageAgents.customRosterPromptFiles}
    cp ${hostPkgs.writeText "conflict-resolve-prompt.md" imagePrompts.conflictResolvePrompt} $out/conflict-resolve-prompt.md
    cp -r ${imagePrompts.fragmentsSourceDir} $out/fragments
    cp -r ${imagePrompts.choresSourceDir} $out/chores
  '';

  # agentFiles' /agent/skills and /home/agent slice, realized with hostPkgs so
  # the Go seam test can read it on a darwin host too (issue #4296).
  homeLayoutDir = imageModule.homeLayoutFor hostPkgs;

  # The baked-skills directory as a host store path, laid out as lib/image.nix
  # bakes it: each skill is a `<name>/SKILL.md` directory, because Claude Code
  # discovers skills only as directories. A { name; src; } content entry
  # (issue #597) is realized with hostPkgs because this is a host-only test
  # artifact, never an input to the Linux image.
  skillsDir = hostPkgs.runCommand "skills-dir" { } (
    if imageAgents.skills == [ ] then
      "mkdir -p $out"
    else
      ''
        mkdir -p $out
        ${lib.concatMapStrings (
          f:
          if builtins.isAttrs f && !(lib.isDerivation f) then
            ''
              mkdir -p $out/${lib.escapeShellArg f.name}
              cp ${hostPkgs.writeText "SKILL.md" f.src} $out/${lib.escapeShellArg f.name}/SKILL.md
            ''
          else
            ''
              cp -r ${f} $out/${if lib.isDerivation f then f.name else builtins.baseNameOf f}
            ''
        ) imageAgents.skills}
      ''
  );

  # Nix store paths are always `/nix/store/<32-char-base32-hash>-<name>`, so
  # characters 11 to 42 (0-indexed) are the hash. Shared by imageHash and
  # launcherCurrencyHash so the prefix-length and hash-width numbers live in
  # one place.
  storeHashOf = path: builtins.substring 11 32 path;

  # The image's store path as plain text (context discarded), so the launcher
  # commands embed the exact Linux image path without taking a build-time
  # dependency on it. That lets `build`, `run` and `nix flake check` build
  # natively on darwin; realizing the image stays `nix build .#<imageAttrName>`.
  imagePath = builtins.unsafeDiscardStringContext (toString image);

  # The content-hash image tag: a changed flake produces a new hash, so the
  # old tag is absent and `run` rebuilds.
  imageHash = storeHashOf imagePath;

  # The image's `.drv` path, also context-discarded. `build` realizes it with
  # `nix build "<drv>^*"` before loading, so a fresh machine builds the image
  # instead of failing on an unrealized path. Reading `.drvPath` instantiates
  # the derivation at eval time, so the .drv exists by the time `build` runs;
  # only realizing it needs a Linux builder.
  imageDrv = builtins.unsafeDiscardStringContext image.drvPath;

  # bwrap runner store paths, context-discarded for the same reason. Reading
  # `.drvPath` instantiates each derivation at eval time (creating the .drv
  # file) but does not realize the output; `bwrap build` does that.
  agentFilesPath = builtins.unsafeDiscardStringContext (toString agentFiles);
  agentFilesDrv = builtins.unsafeDiscardStringContext agentFiles.drvPath;
  agentEnvPath = builtins.unsafeDiscardStringContext (toString agentEnv);
  agentEnvDrv = builtins.unsafeDiscardStringContext agentEnv.drvPath;
  passwdFilePath = builtins.unsafeDiscardStringContext (toString passwdFile);
  passwdFileDrv = builtins.unsafeDiscardStringContext passwdFile.drvPath;
  groupFilePath = builtins.unsafeDiscardStringContext (toString groupFile);
  groupFileDrv = builtins.unsafeDiscardStringContext groupFile.drvPath;
  nixConfigFilePath = builtins.unsafeDiscardStringContext (toString nixConfigFile);
  nixConfigFileDrv = builtins.unsafeDiscardStringContext nixConfigFile.drvPath;
  syscallFilterPath = builtins.unsafeDiscardStringContext (toString syscallFilter);
  syscallFilterDrv = builtins.unsafeDiscardStringContext syscallFilter.drvPath;

  # The bwrap freshness check (issue #2667) needs one comparable output path
  # standing in for everything that changes bwrap Box behavior. linkFarm
  # bundles them into one derivation without merging their directory trees.
  # Any knob that reaches the Box at runtime belongs here: omitting `prefetch`
  # left Probe reporting the box fresh across a prefetch bump (issue #2954).
  agentClosure = pkgs.linkFarm "agent-closure" [
    {
      name = "files";
      path = agentFiles;
    }
    {
      name = "env";
      path = agentEnv;
    }
    {
      name = "nix-config";
      path = nixConfigFile;
    }
    {
      name = "prefetch";
      path = pkgs.writeText "prefetch" prefetch;
    }
  ];
  agentClosurePath = builtins.unsafeDiscardStringContext (toString agentClosure);

  # The runner row (lib/runners) that lists the runtime knob; its artifact picks
  # the flake package below.
  runners = import ./runners/index.nix { };
  runnerRow = runners.forRuntime runtime;

  # One renderer over the flakeOption schema entries, used by the entrypoint's
  # Box-side preamble and the document's `settings` section below. Box env is
  # launcher-to-Box plumbing, not an operator knob (ADR 0020).
  renderDefaultsPreamble =
    args: preambles.renderDefaultsPreamble (args // { inherit flakeOptionEntries mergedDefaults; });

  # The Launcher input document's `settings` section (ADR 0020): every
  # flakeOption knob's resolved mergedDefaults value, keyed by env var name
  # and carried as JSON rather than env.
  documentSettings = lib.mapAttrs' (
    key: entry: lib.nameValuePair entry.env (toString mergedDefaults.${key})
  ) flakeOptionEntries;

  # The document's `run` and `build` artifacts sections (ADR 0020): the
  # nix-computed plumbing (image refs, agent files, driver name).
  runArtifacts = preambles.runArtifacts {
    row = runnerRow;
    inherit
      agentFilesPath
      agentEnvPath
      passwdFilePath
      groupFilePath
      agentFilesDrv
      agentEnvDrv
      passwdFileDrv
      groupFileDrv
      agentClosurePath
      imagePath
      imageHash
      launcherCurrencyHash
      runtime
      imageAttrName
      imageDrv
      nixBuilderImage
      systems
      hostMediatedRemote
      outboxRelayCapable
      inBoxUnreachableTracker
      fullyLocal
      trackerAxisRead
      trackerAxisWrite
      trackerAxisFiler
      forgeBackend
      filerEnabled
      workerProvisioned
      scoutProvisioned
      choreCatalog
      roleModels
      driverVersion
      # Always renders the Consumer's raw knob value (issue #2665), unlike
      # nixConfigPath below. The AND-gate with NixConfigFile lives in
      # bwrap.go, not here.
      nixStoreWritable
      ;
    driverEntry = imageDriver.driverEntry;
    prefetch = imageKnobs.prefetch;
    imageName = imageKnobs.imageName;
    boxEnvVars = preambles.renderBoxEnvVarsList schema;
    # bwrap-only (issue #2664): renders as "" when nixInBox is off, matching
    # how the OCI branch never gets this key. The overlay store's nix.conf
    # only matters when the Box actually gets in-box nix.
    nixConfigPath = if nixInBox then nixConfigFilePath else "";
    # Mirrors the nixConfigPath line above.
    nixConfigDrv = if nixInBox then nixConfigFileDrv else "";
    # The syscall filter is independent of nix-in-box, so it always renders
    # its real path.
    inherit syscallFilterPath syscallFilterDrv;
  };

  # The rendered document as a host store-path JSON file. The generated
  # wrapper passes exactly one nix-computed argument, `--input <path>`.
  runInputDocumentFile = hostPkgs.writeText "launcher-run-input.json" (
    preambles.renderInputDocumentJSON {
      settings = documentSettings;
      artifacts = runArtifacts;
    }
  );

  # vendorHash lives in lib/build-constants.nix. Recompute it with
  # pkgs.lib.fakeHash against this recipe's own `src` and commit it alongside
  # go.sum. launcherCurrencyBin, driverExecBin, orchestratorBin and boxBin each vendor
  # a narrower fileset off the same go.mod/go.sum, so each needs its own
  # recompute against its own src; the hashes are not interchangeable (#784).
  launcherBin = hostPkgs.buildGoModule {
    pname = "spindrift-launcher";
    version = spindriftVersion;
    src = launcherModuleSrc;
    vendorHash = buildConstants.launcherVendorHash;
    subPackages = [ "." ]; # build only the launcher; driver-exec is in-box only
    # nix/checks/go.nix already runs `go test ./...` vendored and offline
    # against the same source, so running it again here is redundant
    # (issue #1142).
    doCheck = false;
    ldflags = [
      "-X main.version=${spindriftVersion}"
      "-X main.revision=${revision}"
    ];
    meta.license = lib.licenses.mit;
  };

  # Store path as plain text (context discarded), the same trick as imagePath
  # above: output paths are computed from the derivation's hash at eval time,
  # so reading this does not force a build.
  launcherCurrencyPath = builtins.unsafeDiscardStringContext (toString launcherCurrencyBin);

  # Used by the freshness probe to compare the loaded launcher's store hash
  # against the one the current flake would produce.
  launcherCurrencyHash = storeHashOf launcherCurrencyPath;

  # Shared by the spindrift CLI and the daemon wrapper: sources harness.env
  # (secrets, gitignored) from $PWD, since the harness is a store path with no
  # working tree. Knobs and artifacts flow through the --input
  # document instead (ADR 0020), so this wrapper bakes nothing per-knob.
  runShellBody =
    let
      # Sources `file` with every assignment exported; `pre` is extra lines run
      # inside the existence guard, before the source.
      sourceEnv =
        file: pre:
        lib.concatStringsSep "\n" (
          [ ''if [ -f "${file}" ]; then'' ]
          ++ map (l: "  " + l) (
            pre
            ++ [
              "set -a"
              "# shellcheck disable=SC1091"
              ''. "${file}"''
              "set +a"
            ]
          )
          ++ [ "fi" ]
        )
        + "\n";

      driverValuedEnvNames = map (k: schema.${k}.env) driverMap.driverValued.env;

      # A Driver-valued knob in harness.env still applies, to every Driver; it
      # belongs in harness.${driver}.env. Scans for NAME=, NAME+=, and the
      # export/readonly/declare forms, warning once per knob.
      warnDriverValued = lib.optionals (driverValuedEnvNames != [ ]) (
        lib.splitString "\n" (
          lib.removeSuffix "\n" ''
            warned=" "
            while IFS= read -r line || [ -n "$line" ]; do
              if [[ $line =~ ^[[:space:]]*((export|readonly|declare([[:space:]]+-[[:alnum:]]+)*)[[:space:]]+)?(${lib.concatStringsSep "|" driverValuedEnvNames})\+?= ]]; then
                knob=''${BASH_REMATCH[4]}
                case $warned in
                *" $knob "*) ;;
                *)
                  warned="$warned$knob "
                  echo "spindrift: warning: harness.env sets $knob, a Driver-valued knob, so it applies to every Driver; set it in harness.${driver}.env instead" >&2
                  ;;
                esac
              fi
            done <"$PWD/harness.env"
          ''
        )
      );
    in
    if !multiDriver then
      sourceEnv "$PWD/harness.env" [ ]
    else
      sourceEnv "$PWD/harness.env" warnDriverValued + sourceEnv "$PWD/harness.${driver}.env" [ ];

  # Roff man page rendered from the schema so `man spindrift` carries the full
  # flag reference while `spindrift --help` stays concise.
  manpageRoff = renderers.renderManpageRoff schema spindriftVersion subcommands;

  manpage = hostPkgs.runCommand "spindrift-manpage" { } ''
    install -Dm644 ${hostPkgs.writeText "spindrift.1" manpageRoff} \
      "$out/share/man/man1/spindrift.1"
  '';

  # Bash completion script rendered from the schema (issue #551), same
  # build-time-only pattern as the man page: no committed copy, out of
  # `nix run .#regen`, coverage-guarded by nix/checks/schema-drift.nix.
  bashCompletionScript = renderers.renderBashCompletion schema subcommandRegistry;

  bashCompletion = hostPkgs.runCommand "spindrift-bash-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "spindrift-completion.bash" bashCompletionScript} \
      "$out/share/bash-completion/completions/spindrift"
  '';

  # Same build-time-only pattern as the bash completion above (issue #553).
  fishCompletionScript = renderers.renderFishCompletion schema subcommandRegistry;

  fishCompletion = hostPkgs.runCommand "spindrift-fish-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "spindrift.fish" fishCompletionScript} \
      "$out/share/fish/vendor_completions.d/spindrift.fish"
  '';

  # Same build-time-only pattern as the bash completion above (issue #552).
  zshCompletionScript = renderers.renderZshCompletion schema subcommandRegistry;

  zshCompletion = hostPkgs.runCommand "spindrift-zsh-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "_spindrift" zshCompletionScript} \
      "$out/share/zsh/site-functions/_spindrift"
  '';

  # The spindrift CLI: passes the rendered Launcher input document via
  # --input and execs the Go launcher (ADR 0020). The man page is joined into
  # the same output so `man spindrift` resolves from the dev shell (nixpkgs
  # adds share/man to MANPATH) and on install.
  spindriftBin =
    (hostPkgs.writeShellApplication {
      name = "spindrift";
      # sqlite3 backs `launcher build`'s bwrap+nixInBox store-DB snapshot
      # step (ADR 0042). Whether a command needs it is a runtime decision (the
      # Consumer's nixInBox knob), which this derivation cannot gate, so it
      # carries sqlite3 unconditionally.
      runtimeInputs = with hostPkgs; [
        gh
        git
        coreutils
        sqlite
      ];
      text = runShellBody + ''
        exec ${launcherBin}/bin/launcher --input ${runInputDocumentFile} "$@"
      '';
    }).overrideAttrs
      (_: {
        meta.license = lib.licenses.mit;
      });

  spindrift = hostPkgs.symlinkJoin {
    name = "spindrift";
    paths = [
      spindriftBin
      manpage
      bashCompletion
      fishCompletion
      zshCompletion
    ];
    meta.license = lib.licenses.mit;
  };

  # The unattended driving loop (issue #3538): sources harness.env the same
  # way spindriftBin does and shares runInputDocumentFile with it, so no
  # knob is hand-copied and none can drift between the CLI and the loop. It
  # re-invokes the Consumer's own CLI app (DAEMON_APP, default `.#`) through
  # `nix run` rather than exec'ing launcherBin directly, because that is how
  # it drives a *child* Dispatch through the flake's own app resolution
  # (e.g. `.#dogfood-bwrap` for a bwrap Consumer) instead of always the
  # binary it happened to be built against. It is the only component that
  # invokes `nix` at runtime, hence the unconditional runtimeInput below.
  daemonWrapper =
    (hostPkgs.writeShellApplication {
      name = "spindrift-daemon";
      runtimeInputs = with hostPkgs; [
        nix
        git
        coreutils
      ];
      text = runShellBody + ''
        # The daemon compares its own program store path against what the daemon
        # attribute evaluates to at the fetched tip (issue #3543). It has to come
        # from $0 at runtime: a derivation cannot interpolate its own store path
        # into its own text without infinite recursion. Unset means the daemon
        # cannot know its own build, and the check is simply skipped. No
        # readlink -f: `nix eval --raw` returns the uncanonicalised store path,
        # so resolving symlinks here would make a legitimate store diverge from
        # what it is compared against. That trades one edge case for the other —
        # a wrapper invoked through a symlink or a relative path (./result/bin/…,
        # a hand-written shim) exports a non-store $0 that can never match, so
        # the daemon halts self-changed on iteration 1 forever — and this is the
        # cheaper hazard: daemonWrapper is reachable only as apps.daemon.program
        # (never a package, so never `nix profile`-installable), and `nix run`
        # execs that exact store path, making $0 byte-identical to what
        # `nix eval --raw` returns. Not a SPINDRIFT_* knob in env-schema.nix:
        # it is set by this wrapper from $0, not resolved from the run-input
        # document, so an operator could only use it to lie to the self check.
        SPINDRIFT_DAEMON_PROGRAM="$0"
        export SPINDRIFT_DAEMON_PROGRAM
        exec ${daemonBin}/bin/daemon --input ${runInputDocumentFile} "$@"
      '';
    }).overrideAttrs
      (_: {
        meta.license = lib.licenses.mit;
      });

  # Realizing the Linux image on darwin needs a Linux builder, so the image is
  # only offered as a package where it can actually build. `nix flake check`
  # on darwin thus never forces a Linux build.
  isLinux = system == linuxSystem;

  # Checked against the Consumer's own `defaults` arg, not mergedDefaults
  # (which always carries every schema key), so the warning fires only when
  # the Consumer actually set one of these knobs (issue #264). stderr-only,
  # so it never changes a derivation's output hash: the legacy knobs and an
  # equivalent `roster` still produce byte-identical images.
  legacyKnobsSet = lib.filter (k: defaults ? ${k}) [
    "scoutModel"
    "reviewModel"
    "filerModel"
    "workerModel"
  ];
  deprecationMsg = "spindrift: the per-agent model knobs (${lib.concatStringsSep ", " legacyKnobsSet}) are deprecated and will be removed; migrate to the `roster` option (see docs/reference.md).";

  # Silent-regression guard (issue #3447): an explicit `roster` replaces
  # defaultRoster wholesale, so one composed from the historical four entries
  # provisions no `review-axis` and the baked /code-review anchor silently
  # falls back to the Driver's ungoverned default. Read off finalRoster so
  # the #392 reviewer opt-out stays silent. stderr-only and hash-neutral.
  explicitRosterMissingReviewAxis =
    roster != null
    && lib.any (e: e.name == "reviewer") finalRoster
    && !(lib.any (e: e.name == "review-axis") finalRoster);
  missingReviewAxisMsg = "spindrift: the explicit `roster` carries a `reviewer` entry but no `review-axis` entry, so the /code-review fan-out runs on the Driver's ungoverned `general-purpose` default instead of a rostered agent (issue #3447); compose the roster from `rosterLib.defaultRoster { }`, or add a `review-axis` entry to it by hand.";

  # Under a Driver with Provider-namespaced model ids, a bare worker model the
  # Consumer set on purpose, via `roster`, `byName.worker.model`, or the legacy
  # `defaults.workerModel` knob, is otherwise dropped silently (issues #5331,
  # #6142). The inherited schema default is not warned: that drop is the
  # expected path. Worker names are unique (normalizeRoster rejects
  # duplicates), so at most one entry drops. `byName` is tested for a non-null
  # model, not presence: the flake module passes every `byName.<name>.model`
  # through, defaulting to null.
  workerModelSetOnPurpose =
    roster != null || defaults ? workerModel || (byName.worker.model or null) != null;
  droppedBareWorkers = lib.optionals (
    workerModelSetOnPurpose && (driverEntry.providerNamespacedModels or false)
  ) (lib.filter bareWorker keptRoster);
  droppedBareWorkerMsg = "spindrift: the worker model ${builtins.toJSON (lib.head droppedBareWorkers).model} is not a `<provider>/<model>` id, which this Driver requires, so the worker is not provisioned (issue #5331).";

  # The same warnings as data, so a check can assert on them without
  # capturing stderr.
  rosterWarnings =
    lib.optional explicitRosterMissingReviewAxis missingReviewAxisMsg
    ++ lib.optional (droppedBareWorkers != [ ]) droppedBareWorkerMsg;

  # Every eval-time warning, applied to the outputs attrset in one fold: a
  # nested `lib.warnIf` per warning would re-indent that whole attrset each
  # time another one is added.
  warnAll =
    outputs:
    lib.foldl' (acc: msg: lib.warn msg acc) outputs (
      lib.optional (legacyKnobsSet != [ ]) deprecationMsg
      ++ rosterWarnings
      ++ deprecatedFragmentVarWarnings
    );

  # Forces buildTimeRejectVerdicts' evaluation (issue #2250): builtins.all
  # must evaluate every element to decide its result, so a `throw` from one
  # element's "reject" branch propagates through it and the `assert` below,
  # with no lazy element to skip past. "advise" traces to stderr instead, and
  # "ok" is silent.
  buildTimeRejectOk = builtins.all (
    v:
    if v.verdict == "reject" then
      throw v.message
    else if v.verdict == "advise" then
      builtins.trace v.message true
    else
      true
  ) buildTimeRejectVerdicts;

  # REPO_SLUG is deliberately runtime-optional at the Nix layer, so this must
  # not throw merely because mergedDefaults.repoSlug is "": most Consumers
  # supply it via --repo-slug at dispatch time, and equivalence.nix pins
  # `mkRun {}` baking `"REPO_SLUG":""` as a must-succeed case (issue #2527).

  # What is eval-decidable is a Consumer that explicitly writes
  # `repoSlug = "";` (detected on the raw `defaults` arg) while selecting a
  # non-fully-local backend pairing. A genuinely runtime-missing slug is
  # caught instead by cmd/launcher/main.go's validate(); the two checks are
  # complementary, not overlapping.
  repoSlugCoherenceOk =
    if (defaults ? repoSlug) && defaults.repoSlug == "" && !fullyLocal then
      throw "mkHarness: repoSlug is explicitly set to an empty string, but CODE_FORGE=${mergedDefaults.codeForge}/ISSUE_TRACKER=${mergedDefaults.issueTracker} is not fully-local (CODE_FORGE=local and ISSUE_TRACKER=local) -- either supply a real repoSlug or omit the key entirely so REPO_SLUG is supplied at dispatch runtime instead"
    else
      true;

  # BOX_FORGE_AND_ISSUE_ACCESS=read-only denies the Box a write token on both
  # axes, so the selected CODE_FORGE must be relayCapable and the selected
  # ISSUE_TRACKER hostPostingCapable (lib/backends/default.nix, issue #2526).
  # read-write is a fast no-op that never inspects the backends, mirroring
  # cmd/launcher/main.go's checkReadOnlyCapabilityGate.
  readOnlyCapabilityOk =
    if mergedDefaults.boxForgeAndIssueAccess != "read-only" then
      true
    else if !(codeForgeRow.relayCapable or false) then
      throw "mkHarness: BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=${mergedDefaults.codeForge} does not implement bundle-relay (forge.BundleRelay) for the Box's finished branch hand-off"
    else if !(issueTrackerRow.hostPostingCapable or false) then
      throw "mkHarness: BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected ISSUE_TRACKER=${mergedDefaults.issueTracker} does not implement host-posted comments and issue-filing (forge.HostPostedCommenter / forge.HostPostedIssueFiler)"
    else
      true;

  # A selected backend row's `validateKnobs` (lib/backends/jira.nix: its
  # JIRA_STATUS_MAPPING parse, which mirrors jira.go's ParseStatusMapping,
  # issue #2539) fails the build here. Only the selected rows run, so the
  # knobs are dead config elsewhere and must not fail a github/forgejo/local
  # build. `builtins.all` forces the result so the `assert` below throws.
  backendKnobsOk = builtins.all (row: (row.validateKnobs or (_: true)) mergedDefaults) [
    issueTrackerRow
    codeForgeRow
  ];

  # lib/awake-window.nix's `parse` mirrors the runtime validation in
  # awake.go's ParseWindow (issue #3542). Unlike backendKnobsOk this
  # needs no backend gate -- the knob is valid or not regardless of
  # tracker/forge. `builtins.seq` forces the result so the `assert` below
  # triggers a throw.
  awakeWindow = import ./awake-window.nix;
  daemonAwakeWindowOk = builtins.seq (awakeWindow.parse (
    mergedDefaults.daemonAwakeWindow or ""
  )) true;

  # A Chore named in BUTLER_CHORES with no matching <name>.md would otherwise
  # go unnoticed until a Box claims its Ledger and finds nothing to run
  # (issue #3991) -- catch a typo at eval time instead, naming the offender.
  # Splits on runs of space, tab, CR and LF (close to Go's strings.Fields,
  # which also splits on \v, \f and Unicode spaces) against the same
  # choreStems already read for choreCatalog above.
  butlerChoresTokens = lib.filter (s: s != "") (
    lib.splitString " " (
      lib.replaceStrings [ "\t" "\n" "\r" ] [ " " " " " " ] (mergedDefaults.butlerChores or "")
    )
  );
  butlerChoresMissingPrompt = lib.filter (name: !(lib.elem name choreStems)) butlerChoresTokens;
  butlerChoresPromptOk =
    if butlerChoresMissingPrompt == [ ] then
      true
    else
      throw "mkHarness: BUTLER_CHORES enables chore(s) with no prompt file in choresDir: ${lib.concatStringsSep ", " butlerChoresMissingPrompt}; each needs <name>.md in the chores directory";
  # The always-present flake packages; imageAttrName must not shadow one of
  # them, or the `//` merge below would silently drop the sibling package.
  basePackages = {
    inherit spindrift;
    launcher-currency = launcherCurrencyBin;
    spindrift-manpage = manpage;
    spindrift-bash-completion = bashCompletion;
    spindrift-fish-completion = fishCompletion;
    spindrift-zsh-completion = zshCompletion;
  };
  imageAttrNameOk =
    if imageAttrName == "" then
      throw "mkHarness: imageAttrName must not be empty"
    else if lib.hasInfix "." imageAttrName then
      throw "mkHarness: imageAttrName '${imageAttrName}' must not contain '.' -- it names a single flake package attribute, and a dotted name would be read as an attribute path"
    else if basePackages ? ${imageAttrName} then
      throw "mkHarness: imageAttrName '${imageAttrName}' collides with the built-in package of the same name; pick a name outside: ${lib.concatStringsSep ", " (lib.attrNames basePackages)}"
    else
      true;
in
if defaultsKeyError != null then
  throw defaultsKeyError
else
  assert buildTimeRejectOk;
  assert bareModelCheckOk;
  assert kindPromptKeysOk;
  assert forbiddenMarkerCheckOk;
  assert removedFragmentVarCheckOk;
  assert researchDirectFileCheckOk;
  assert repoSlugCoherenceOk;
  assert choicesCheckOk;
  assert networkModeCoherenceOk;
  assert nixStoreWritableCoherenceOk;
  assert readOnlyCapabilityOk;
  assert backendKnobsOk;
  assert daemonAwakeWindowOk;
  assert butlerChoresPromptOk;
  assert imageAttrNameOk;
  warnAll {
    inherit
      image
      spindrift
      ;

    # Outputs checks and fixtures need that are not part of the versioned
    # Consumer contract (ADR 0010, scoped to image/spindrift/packages/apps,
    # issue #2529). The four completion and manpage outputs are also reachable
    # as `packages.spindrift-*`; this attrset is only where checks read them.
    internals = {
      inherit
        agentEnv
        agentFiles
        agentClosurePath
        manpage
        bashCompletion
        fishCompletion
        zshCompletion
        imagePath
        promptDir
        skillsDir
        homeLayoutDir
        outcomeContractFile
        commsContractFile
        checkContractFile
        researchOutcomeContractFile
        driverPreambleFile
        agentPathsPreambleFile
        runInputDocumentFile
        backendBundleDirs
        backendCLIPackages
        ;
      driverExecBin = imageDriver.driverExecBin;
      orchestratorBin = imageDriver.orchestratorBin;
      boxBin = imageDriver.boxBin;
      driverEntry = imageDriver.driverEntry;

      # The resolved name, so lib/flakeModule.nix aliases the right package
      # without re-deriving the bwrap/OCI default.
      inherit imageAttrName;

      # daemonBin is otherwise reachable only as apps.daemon.program, a
      # string -- exposed here so nix/checks/equivalence.nix's store-path
      # checks can compare and override it directly (issue #3621).
      inherit daemonBin;

      # The fully resolved roster, after dropOptedOut and the reviewEffort
      # step (issue #2512). Exposed for nix/checks/equivalence.nix's
      # eval-level introspection, not as part of the settings or CLI.
      roster = finalRoster;

      # The pre-toSource lib.fileset value backing launcherCurrencySrc: a
      # comparison derivation in nix/checks/equivalence.nix needs this exact
      # fileset value, not just the realized store path (issue #2677).
      inherit launcherCurrencyFileset;

      # Exposed for nix/checks/prompts.nix (issue #2595), so its checks run
      # against a real mkHarness build's computed values rather than a
      # reimplemented predicate that could drift from this file: the two row
      # lists must agree on every FILER_FILE_DIRECT*-gated row, and
      # researchPromptContentByName's keys must cover every prompt on disk.
      inherit
        directFileFragmentRows
        readOnlyReachableFragmentRows
        researchPromptContentByName
        kindPrompts
        ;

      inherit rosterWarnings deprecatedFragmentVarWarnings;
    };

    packages =
      basePackages
      # One flake package under ${imageAttrName}, whose artifact the runner row
      # picks: the OCI image (not built for bwrap, which has no image
      # build/load) or, for bwrap, the whole agent closure, so freshness
      # (issue #2667) has a single attr to realize.
      // lib.optionalAttrs isLinux {
        ${imageAttrName} =
          {
            oci-image = image;
            store-closure = agentClosure;
          }
          .${runnerRow.artifact};
      };

    apps.default = {
      type = "app";
      program = "${spindrift}/bin/spindrift";
    };

    # The daemon per Consumer, beside apps.default (issue #3538): re-invokes
    # this same Consumer's own CLI app for each child Dispatch, so choosing a
    # runtime is choosing which harness's apps.daemon to run, not editing a
    # script.
    apps.daemon = {
      type = "app";
      program = "${daemonWrapper}/bin/spindrift-daemon";
    };
  }
