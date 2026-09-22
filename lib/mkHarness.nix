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
  # Shell snippet the entrypoint runs after cloning, to warm toolchain caches
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
  # The conditional fragment registry (issue #622): rows of (gate, fragment,
  # var) that the entrypoint's fragment loop and its `_subst` allowlist are
  # both rendered from. Not Consumer-tunable; overridable here only for the
  # bats fixture-row test proving a new row needs no entrypoint edit.
  fragments ? import ./fragments.nix,
  # The directory the fragment registry's files live under, copied whole into
  # the image. Not Consumer-tunable; overridable here only so
  # nix/checks/prompts.nix (issue #2250) can point the
  # research-verdict-*-readonly.md lookup at a broken fixture directory
  # without touching the real templates tree.
  fragmentsDir ? ../templates/default/prompts/fragments,
  # Skill files baked into the image at /home/agent/.claude/skills. Each
  # element is a path/derivation (copied under its basename) or a
  # { name; src; } content entry (issue #597) re-realized with the image's own
  # Linux pkgs, never a consumer host derivation, which would tag the image's
  # drvPath with the host system. SPINDRIFT_SKILLS_DIR shadows all of them.
  skills ? [ ],
  # Non-secret run config baked into the `run` command as its built-in defaults;
  # a matching env var still wins at runtime, so one build can be re-pointed.
  defaults ? { },
  # Container runtime the launcher commands drive: "podman" (default), "docker",
  # or "rancher" (Rancher Desktop containerd mode; invokes nerdctl).
  runtime ? "podman",
  # The agent CLI Driver (ADR 0009, issues #261/#262): selects one entry from
  # the lib/drivers/ registry, baked into the image (in-box half) and threaded
  # to the Go launcher as DRIVER (host-side half). "claude" and "opencode".
  driver ? "claude",
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

  # The single param preambles.runArtifacts and preambles.buildArtifacts take
  # (issue #2770); lib/preambles.nix explains the bundling.
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
  instantiate =
    forSystem:
    if overlays == [ ] && config == { } then
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

  inherit (pkgs) lib;

  # Single source of truth for every runtime knob: name mapping, defaults,
  # scope. Generators below derive all per-knob output from this registry, so
  # no per-knob lines appear anywhere else in this file.
  schema = import ./env-schema.nix;

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
  inherit (promptContract) byId;

  # An alias for the fragmentsDir param above, kept so this file's existing
  # fragmentsSourceDir uses are unchanged (issue #463).
  fragmentsSourceDir = fragmentsDir;

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
  # order in agent/entrypoint.sh, so the baked and mounted-override cases
  # agree.
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
  researchPromptRendered = researchVerdicts.render researchVerdictsKnob researchPrompt;
  # Same verdict-set rendering, applied to the self-contained sub-mode prompt
  # (issue #2202) so a custom RESEARCH_VERDICTS knob reaches both prompts.
  researchSelfContainedPromptRendered = researchVerdicts.render researchVerdictsKnob researchSelfContainedPrompt;
  researchPromptSourceRendered = researchVerdicts.render researchVerdictsKnob researchPromptSource;
  researchOutcomeContract = sliceFromMarker researchOutcomeContractMarker researchPromptSourceRendered;
  injectResearchOutcomeContract = injectSection researchOutcomeContractMarker researchOutcomeContract;

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
  # FORGE_BACKEND (issue #2533). cmd/launcher/main.go reads the same
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
  # Consumer that sets both must pick one. no-host-loopback is rejected on
  # bwrap because a bwrap Box already isolates its network namespace by
  # default (issue #2666), so the choice would render identically to "open".
  networkModeCoherenceOk =
    if
      (defaults ? networkMode)
      && ((mergedDefaults.podmanNetwork or "" != "") || (mergedDefaults.bwrapUnshareNet or false))
    then
      throw "mkHarness: network.mode=${mergedDefaults.networkMode} is set together with a raw network knob (network.podman/network.bwrapUnshare) -- there is no precedence rule between them, so the Consumer must pick one"
    else if mergedDefaults.networkMode or "open" == "no-host-loopback" && runnerKind == "bwrap" then
      throw "mkHarness: network.mode=no-host-loopback is unsupported on runtime=bwrap -- it has no rendering distinct from the isolated-by-default network.mode=open; use network.mode=open instead, or runtime=podman/docker/rancher for the docker/nerdctl inert-but-correct render"
    else
      true;

  # Whether either backend knob selects forgejo (issue #1963): drives
  # lib/image.nix's fj (forgejo-cli) bake, so a github-backend Consumer's
  # image never carries an unused CLI.
  forgejoBackend =
    (mergedDefaults.issueTracker or "github") == "forgejo"
    || (mergedDefaults.codeForge or "github") == "forgejo";

  # Unknown defaults keys are caught at eval time. A typo like `basebranch`
  # would otherwise be silently ignored and never baked.
  unknownDefaultKeys = lib.filter (k: !(lib.hasAttr k flakeOptionEntries)) (lib.attrNames defaults);

  # An explicit `roster` always wins; otherwise it resolves from the four
  # deprecated per-agent model knobs (issue #264, lib/roster.nix).
  rosterLib = import ./roster.nix { inherit lib; };
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

  # reviewEffort (issue #2512) is the one legacy knob that overrides an
  # already-resolved roster's reviewer entry whatever the roster's source,
  # unlike the four model knobs above. Applied post-normalize so it reaches
  # the defaultRoster branch and an explicit roster identically.
  finalRoster =
    let
      reviewEffort = mergedDefaults.reviewEffort or "";
    in
    if reviewEffort == "" then
      keptRoster
    else
      map (e: if e.name == "reviewer" then e // { effort = reviewEffort; } else e) keptRoster;

  # --agents JSON, rendered by the selected Driver (ADR 0009) so a Driver with
  # a different agent-config shape (opencode's agents/*.md) can supply its own
  # renderer without touching mkHarness.
  agentsJsonTemplate = driverEntry.agentsJsonTemplate { roster = finalRoster; };

  # Threaded into the Launcher input document's `run` artifacts as
  # FILER_ENABLED / WORKER_PROVISIONED / SCOUT_PROVISIONED /
  # REVIEW_LOOP_INLINE / REVIEW_LOOP_ORCHESTRATOR (issues #2533, #3157), so
  # the Go side reads them from the document instead of re-deriving roster
  # membership and orchestration mode itself.
  agentsJsonAttrs = if agentsJsonTemplate == "" then { } else builtins.fromJSON agentsJsonTemplate;

  # These two key off agentsJsonTemplate's rendered output, not finalRoster:
  # opencode's agentsJsonTemplate always returns "" and provisions subagents
  # through driverAgentFiles instead, so a roster-only check would wrongly
  # report WORKER_PROVISIONED true for an opencode box (issue #2533 review).
  filerEnabled = agentsJsonAttrs ? filer;
  workerProvisioned = agentsJsonAttrs ? worker;

  # scoutProvisioned keys off finalRoster instead of agentsJsonAttrs, the
  # mirror image of filerEnabled/workerProvisioned above: opencode provisions
  # scout through driverAgentFiles (rendered by lib/drivers/opencode.nix's
  # agentFilesTemplate), so agentsJsonAttrs would wrongly read false for an
  # opencode box that does carry scout.
  scoutProvisioned = lib.any (e: e.name == "scout") finalRoster;
  reviewLoopInline = !mergedDefaults.orchestratorEnabled;
  reviewLoopOrchestrator = mergedDefaults.orchestratorEnabled;

  # On-disk subagent files, rendered by the selected Driver. A Driver with no
  # on-disk agent-config mechanism (claude.nix) returns { } here; its
  # subagents ride agentsJsonTemplate's --agents JSON flag instead.
  driverAgentFiles = driverEntry.agentFilesTemplate { roster = finalRoster; };

  # Name to prompt-file map (issue #264), read at runtime by entrypoint.sh's
  # per-agent prompt injection loop so a custom agent's prompt resolves the
  # same way as the built-in names. normalizeRoster guarantees every entry
  # carries a promptFile (issue #2152), so there is no fallback to re-derive.
  agentsPromptFilesJson = builtins.toJSON (
    lib.listToAttrs (
      map (e: {
        name = e.name;
        value = e.promptFile;
      }) finalRoster
    )
  );

  # Custom roster entries carrying their own prompt, baked into the image
  # alongside the fixed prompt files. An entry omitting `prompt` is treated
  # the same as one setting it to null (issue #264 review finding).
  customRosterPromptFiles = lib.filter (e: (e.prompt or null) != null) finalRoster;

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

  # The fragment registry rendered for agent/entrypoint.sh (issue #622): a
  # bash array of "gate|fragment|var" rows plus every var an envsubst call
  # must know about. The loop and `_subst` are generic over this data, so a
  # new row needs no entrypoint edit. Shared with the bats harness file
  # (issue #433) so neither can drift from the other.
  fragmentRegistryRows = map (row: "${row.gate}|${row.fragment}|${row.var}") fragments;
  fragmentSubstVars = lib.concatMap (row: [ row.var ] ++ (row.extraSubstVars or [ ])) fragments;
  fragmentRegistryPreamble =
    "_FRAGMENT_ROWS=(\n"
    + lib.concatMapStrings (row: "  " + lib.escapeShellArg row + "\n") fragmentRegistryRows
    + ")\n"
    + "_FRAGMENT_SUBST_VARS=(\n"
    + lib.concatMapStrings (v: "  " + lib.escapeShellArg v + "\n") fragmentSubstVars
    + ")\n";

  # The same registry as JSON (issue #2354), for the Go `driver-exec
  # assemble-prompt` verb's `--registry` flag. A sibling of
  # fragmentRegistryPreamble above, not a replacement: the bash preamble
  # still drives entrypoint.sh's own fragment loop.
  fragmentsRegistryJson = builtins.toJSON fragments;

  # lib/prompt-contract.nix's validateMarkers list as JSON (issue #2356), for
  # the Go `driver-exec assemble-prompt` verb's `--validate-markers-registry`
  # flag.
  promptContractRegistryJson = builtins.toJSON promptContract.validateMarkers;

  # lib/prompt-contract.nix's forbiddenMarkers list as JSON (issue #2464), for
  # the Go `driver-exec readonly-guards` verb's `--forbidden-markers-registry`
  # flag. assemble-prompt no longer takes this flag (issue #2513).
  forbiddenMarkersRegistryJson = builtins.toJSON promptContract.forbiddenMarkers;

  # Build-time reject arm (issue #2250): resolves both validateMarkers
  # "reject" rows against this build's static knowledge. Only github and
  # forgejo have a distinct "-readonly" fragment file (lib/fragments.nix), so
  # for local/jira the suffix is null, the id is omitted from contentByRowId,
  # and the row resolves to "advise". buildTimeRejectOk below forces the list.
  researchReadonlyForgeSuffix =
    if mergedDefaults.issueTracker == "github" then
      "github"
    else if mergedDefaults.issueTracker == "forgejo" then
      "forgejo"
    else
      null;
  buildTimeRejectVerdicts = promptContract.buildTimeRejectVerdicts {
    staticGates = {
      orchestratorEnabled = mergedDefaults.orchestratorEnabled == true;
      readOnlyResearch = mergedDefaults.boxForgeAndIssueAccess == "read-only";
    };
    contentByRowId = {
      "reviewer-verdict" = reviewPrompt;
    }
    // lib.optionalAttrs (researchReadonlyForgeSuffix != null) {
      "verdict-comment-relay" = builtins.readFile (
        fragmentsDir + "/research-verdict-${researchReadonlyForgeSuffix}-readonly.md"
      );
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
    fragmentContentByFile = builtins.listToAttrs (
      map (row: {
        name = row.fragment;
        value = builtins.readFile (fragmentsDir + "/${row.fragment}");
      }) readOnlyReachableFragmentRows
    );
    # Just these three: issue #2510 scopes the shared-template half of this
    # rule to the issue, review and filer prompts by name. fix-prompt.md and
    # the research prompts also carry forbiddenMarkers substrings, but
    # bringing them under this scan is out of scope.
    templateContentByFile = {
      "issue-prompt.md" = prompt;
      "review-prompt.md" = reviewPrompt;
      "filer-prompt.md" = filerPrompt;
    };
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

  # The FILER_FILE_DIRECT*-gated fragment rows (issue #2595, ADR 0041): the
  # ones telling the agent to run `gh issue create` and friends directly,
  # never rendered into a research prompt by design. lib/fragments.nix's
  # research-file-issues-relay.md row says why.
  directFileFragmentRows = builtins.filter isDirectFileGate fragments;

  # The research prompts scanned for a direct-file placeholder (issue #2595).
  # Hand-typed, not derived from a directory listing, so a third
  # research*-prompt.md template would silently miss this scan. Named so
  # nix/checks/prompts.nix can read it back through `internals` below and
  # assert its keys still cover every research*-prompt.md file on disk.
  researchPromptContentByName = {
    "research-prompt.md" = researchPromptRendered;
    "research-self-contained-prompt.md" = researchSelfContainedPromptRendered;
  };

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

  # Version sourced from the release-please manifest so mkHarness always tracks
  # the bot-maintained source of truth (ADR-0010).
  spindriftVersion = (builtins.fromJSON (builtins.readFile ../.release-please-manifest.json)).".";

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
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/driver)
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
        ) ../cmd/launcher/internal/logscan)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/outcome)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/bundleout)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/seambundle)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/outcomebackstop)
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
        ) ../cmd/launcher/internal/markergate)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/readonlyguards)
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
        ) ../cmd/launcher/internal/registryvocab)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/registryprobe)
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

  # In-box orchestrator (issue #1996, ADR 0007): the Go binary entrypoint.sh
  # hands the implementor pass off to when ORCHESTRATOR_ENABLED is set,
  # instead of calling driver-exec directly. Its fileset carries the same
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
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/driver)
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
        ) ../cmd/launcher/internal/logscan)
        (lib.fileset.fileFilter (
          f: f.hasExt "go" && !lib.hasSuffix "_test.go" f.name
        ) ../cmd/launcher/internal/runstate)
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
        ) ../cmd/launcher/internal/deltareview)
      ];
    };
    # Its own hash, not driverExecBin's: the orchestrator's fileset (above)
    # has no internal/ecosystem, so it vendors none of the third-party
    # parsing dependencies that package's rows reach for.
    vendorHash = buildConstants.orchestratorVendorHash;
    subPackages = [ "orchestrator" ];
    meta.license = lib.licenses.mit;
  };

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
      driverPreamble
      driverAgentFiles
      ;
  };
  imageAgents = {
    inherit
      agentsJsonTemplate
      agentsPromptFilesJson
      customRosterPromptFiles
      skills
      ;
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
      injectOutcomeContract
      injectFixSharedBlocks
      injectResearchOutcomeContract
      ;
  };
  imagePrompts = {
    inherit
      prompt
      scoutPrompt
      reviewPrompt
      reviewAxisPrompt
      filerPrompt
      workerPrompt
      conflictResolvePrompt
      fixPrompt
      fragmentsSourceDir
      fragmentRegistryPreamble
      ;
    # Carries the verdict contract rendered from RESEARCH_VERDICTS (#2201).
    researchPrompt = researchPromptRendered;
    # The self-contained sub-mode's own prompt (issue #2202), same rendering.
    researchSelfContainedPrompt = researchSelfContainedPromptRendered;
  };
  imageKnobs = {
    inherit
      nixInBox
      nixStoreWritable
      forgejoBackend
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

  # The fragment registry as a host store-path file (issue #622), prepended by
  # the bats harness for the same reason, so tests exercise the same loop
  # input and substitution allowlist that mkHarness bakes into the image.
  fragmentRegistryFile = hostPkgs.writeText "fragment-registry.sh" imagePrompts.fragmentRegistryPreamble;

  # The rendered prompt directory as a host store path (native-buildable on
  # darwin, so it needs no Linux builder). The prompt is normally baked into
  # the image via agentFiles; this output exists so tests can assert it is NOT
  # bind-mounted by default, and so SPINDRIFT_PROMPT_DIR can point to it.
  promptDir = hostPkgs.runCommand "prompt-dir" { } ''
    mkdir -p $out
    cp ${hostPkgs.writeText "issue-prompt.md" (imageContracts.injectOutcomeContract imagePrompts.prompt)} $out/issue-prompt.md
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
    cp ${hostPkgs.writeText "fix-prompt.md" (imageContracts.injectFixSharedBlocks imagePrompts.fixPrompt)} $out/fix-prompt.md
    cp ${hostPkgs.writeText "research-prompt.md" (imageContracts.injectResearchOutcomeContract imagePrompts.researchPrompt)} $out/research-prompt.md
    cp ${hostPkgs.writeText "research-self-contained-prompt.md" (imageContracts.injectResearchOutcomeContract imagePrompts.researchSelfContainedPrompt)} $out/research-self-contained-prompt.md
    cp -r ${imagePrompts.fragmentsSourceDir} $out/fragments
  '';

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
              mkdir -p $out/${f.name}
              cp ${hostPkgs.writeText "SKILL.md" f.src} $out/${f.name}/SKILL.md
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
  # natively on darwin; realizing the image stays `nix build .#agent-image`.
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

  # runnerKind collapses the runtime knob to the two adapter families the
  # launcher knows: "bwrap" (daemonless) or "oci" (podman/docker).
  runnerKind = if runtime == "bwrap" then "bwrap" else "oci";

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
    inherit
      runnerKind
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
      reviewLoopInline
      reviewLoopOrchestrator
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

  buildArtifacts = preambles.buildArtifacts {
    inherit
      runnerKind
      agentFilesDrv
      agentEnvDrv
      passwdFileDrv
      groupFileDrv
      runtime
      imagePath
      imageHash
      launcherCurrencyHash
      imageDrv
      nixBuilderImage
      systems
      agentClosurePath
      ;
    imageName = imageKnobs.imageName;
    # See runArtifacts' nixConfigPath comment above.
    nixConfigDrv = if nixInBox then nixConfigFileDrv else "";
    # Unconditional; see runArtifacts' syscallFilterPath comment above.
    inherit syscallFilterDrv;
  };

  # The rendered documents as host store-path JSON files. The generated
  # wrapper passes exactly one nix-computed argument, `--input <path>`.
  runInputDocumentFile = hostPkgs.writeText "launcher-run-input.json" (
    preambles.renderInputDocumentJSON {
      settings = documentSettings;
      artifacts = runArtifacts;
    }
  );

  buildInputDocumentFile = hostPkgs.writeText "launcher-build-input.json" (
    preambles.renderInputDocumentJSON {
      settings = documentSettings;
      artifacts = buildArtifacts;
    }
  );

  # buildGoModule's checkPhase runs `go test` from within its src, so docs/
  # must sit alongside cmd/launcher there too, mirroring the repo layout, for
  # TestReferenceDocLabelSnippetMatchesTriageDefaults's ../../docs/reference.md
  # path to resolve (#611).
  launcherSrc = hostPkgs.runCommand "launcher-src" { } ''
    mkdir -p $out/cmd/launcher
    cp -r ${../cmd/launcher}/. $out/cmd/launcher/
    cp -r ${../docs} $out/docs
  '';

  # vendorHash lives in lib/build-constants.nix. Recompute it with
  # pkgs.lib.fakeHash against this recipe's own `src` and commit it alongside
  # go.sum. launcherCurrencyBin, driverExecBin and orchestratorBin each vendor
  # a narrower fileset off the same go.mod/go.sum, so each needs its own
  # recompute against its own src; the hashes are not interchangeable (#784).
  launcherBin = hostPkgs.buildGoModule {
    pname = "spindrift-launcher";
    version = spindriftVersion;
    src = launcherSrc;
    modRoot = "cmd/launcher";
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

  # daemonBin is one input to the wrapper whose own $0
  # (SPINDRIFT_DAEMON_PROGRAM) is what runSlot's inline Tip.SelfPath
  # comparison (cmd/launcher/internal/daemon/loop.go) compares at each
  # iteration boundary (issue #3543); a mismatch halts a running daemon with
  # exit 10. src is scoped with lib.fileset, not launcherSrc: launcherSrc
  # copies ../docs alongside cmd/launcher for launcherBin's checkPhase
  # (#611), and pulling docs in here would halt a running daemon on a
  # docs-only commit (issue #3621).

  # The fileset is the whole module, not a narrowed one like
  # launcherCurrencyFileset below: the launcher's _test.go files pull
  # test-only deps (github.com/charmbracelet/x/exp/golden, .../teatest) into
  # go.sum, so trimming them out would vendor differently and force a second
  # vendorHash. Only docs/ is excluded, so a test-only or sibling-package
  # commit still moves the daemon's path.
  daemonSrc = lib.fileset.toSource {
    root = ../cmd/launcher;
    fileset = ../cmd/launcher;
  };

  daemonBin = hostPkgs.buildGoModule {
    pname = "spindrift-daemon";
    version = spindriftVersion;
    src = daemonSrc;
    # No modRoot here (unlike launcherBin): the fileset's `root` is already
    # ../cmd/launcher. Only subPackages differs from launcherBin, which per
    # nix/quickstart.nix's comment does not change vendoring, so the
    # whole-module fileset still vendors to launcherVendorHash rather than
    # needing its own the way launcherCurrencyVendorHash does.
    vendorHash = buildConstants.launcherVendorHash;
    subPackages = [ "daemon" ];
    doCheck = false;
    # No ldflags: the daemon bakes no version or revision of its own, so its
    # path moves only when the code it is built from moves.
    meta.license = lib.licenses.mit;
  };

  # A revision-independent sibling of launcherBin (issue #2677, ADR 0043):
  # launcherBin bakes `-X main.revision`, so its store path moves on every
  # commit. Staleness detection (issue #1364) needs a hash stable across
  # revision-only changes, so this one drops that ldflag. It is never
  # invoked; only its store hash is read.

  # src is scoped with lib.fileset, not launcherSrc: launcherSrc copies
  # ../docs alongside cmd/launcher for launcherBin's checkPhase (#611), and
  # pulling docs in here would move this hash on a docs-only commit,
  # defeating the point.

  # The fileset is a directory-level approximation of the launcher's import
  # graph, not the graph itself: it subtracts the driver-exec, orchestrator,
  # quickstart and daemon subtrees (each an independent `package main` the
  # launcher never imports), plus internal/daemon (issue #3538: no
  # non-test package outside cmd/launcher/daemon imports it, and every test
  # file is filtered out below). A reviewer found 13 directories included
  # here that are outside the real import graph (internal/testutil, for
  # one), so perturbing those still moves this outPath (issue #2677 review
  # fix).
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
          ../cmd/launcher/internal/daemon
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
    # No modRoot here (unlike launcherBin): the fileset's `root` is already
    # ../cmd/launcher, so the src's top level is cmd/launcher's contents.
    # The narrower fileset also vendors differently off the identical
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

  # Store path as plain text (context discarded), the same trick as imagePath
  # above: output paths are computed from the derivation's hash at eval time,
  # so reading this does not force a build.
  launcherCurrencyPath = builtins.unsafeDiscardStringContext (toString launcherCurrencyBin);

  # Used by the freshness probe to compare the loaded launcher's store hash
  # against the one the current flake would produce.
  launcherCurrencyHash = storeHashOf launcherCurrencyPath;

  # Single-verb wrapper execing `launcher build`. Off the flake outputs
  # (issue #613); it survives only as a bats/equivalence test fixture for the
  # build-time preamble baking.
  build =
    (hostPkgs.writeShellApplication {
      name = "build";
      # sqlite3 backs `launcher build`'s bwrap+nixInBox store-DB snapshot
      # step (ADR 0042); this fixture mirrors spindriftBin's runtimeInputs.
      runtimeInputs = [
        hostPkgs.coreutils
        hostPkgs.sqlite
      ];
      text = ''
        exec ${launcherBin}/bin/launcher --input ${buildInputDocumentFile} build
      '';
    }).overrideAttrs
      (_: {
        meta.license = lib.licenses.mit;
      });

  # Shared by the spindrift CLI and the `run` test fixture: sources
  # harness.env (secrets, gitignored) from $PWD, since the harness is a store
  # path with no working tree. Knobs and artifacts flow through the --input
  # document instead (ADR 0020), so this wrapper bakes nothing per-knob.
  runShellBody = ''
    if [ -f "$PWD/harness.env" ]; then
      set -a
      # shellcheck disable=SC1091
      . "$PWD/harness.env"
      set +a
    fi
  '';

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
      # carries sqlite3 unconditionally. The `run` wrapper below never runs
      # `build`, so it alone can omit it.
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

  # Single-verb wrapper execing `launcher dispatch`. Off the flake outputs
  # (issue #613); it survives only as a bats/equivalence test fixture for the
  # dispatch-time preamble baking.
  run =
    (hostPkgs.writeShellApplication {
      name = "run";
      runtimeInputs = with hostPkgs; [
        gh
        git
        coreutils
      ];
      text = runShellBody + ''
        exec ${launcherBin}/bin/launcher --input ${runInputDocumentFile} dispatch "$@"
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

  # The same warning as data, so a check can assert on it without
  # capturing stderr.
  rosterWarnings = lib.optional explicitRosterMissingReviewAxis missingReviewAxisMsg;

  # Every eval-time warning, applied to the outputs attrset in one fold: a
  # nested `lib.warnIf` per warning would re-indent that whole attrset each
  # time another one is added.
  warnAll =
    outputs:
    lib.foldl' (acc: msg: lib.warn msg acc) outputs (
      lib.optional (legacyKnobsSet != [ ]) deprecationMsg ++ rosterWarnings
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

  # lib/jira-status-mapping.nix's `parse` mirrors the runtime validation in
  # jira.go's ParseStatusMapping, so an unknown-key mapping fails the build
  # here (issue #2539). Gated on ISSUE_TRACKER=jira: elsewhere the knob is
  # dead config and must not fail a github/forgejo/local build.
  # `builtins.seq` forces the result so the `assert` below triggers a throw.
  jiraStatusMapping = import ./jira-status-mapping.nix;
  jiraStatusMappingOk =
    if mergedDefaults.issueTracker != "jira" then
      true
    else
      builtins.seq (jiraStatusMapping.parse (mergedDefaults.jiraStatusMapping or "")) true;

  # lib/awake-window.nix's `parse` mirrors the runtime validation in
  # awake.go's ParseWindow (issue #3542). Unlike jiraStatusMappingOk this
  # needs no backend gate -- the knob is valid or not regardless of
  # tracker/forge. `builtins.seq` forces the result so the `assert` below
  # triggers a throw.
  awakeWindow = import ./awake-window.nix;
  daemonAwakeWindowOk = builtins.seq (awakeWindow.parse (
    mergedDefaults.daemonAwakeWindow or ""
  )) true;
in
if unknownDefaultKeys != [ ] then
  throw "mkHarness: unknown defaults key(s): ${lib.concatStringsSep ", " unknownDefaultKeys}; valid keys: ${lib.concatStringsSep ", " (lib.attrNames flakeOptionEntries)}"
else
  assert buildTimeRejectOk;
  assert forbiddenMarkerCheckOk;
  assert researchDirectFileCheckOk;
  assert repoSlugCoherenceOk;
  assert choicesCheckOk;
  assert networkModeCoherenceOk;
  assert readOnlyCapabilityOk;
  assert jiraStatusMappingOk;
  assert daemonAwakeWindowOk;
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
        build
        run
        manpage
        bashCompletion
        fishCompletion
        zshCompletion
        imagePath
        promptDir
        skillsDir
        outcomeContractFile
        commsContractFile
        checkContractFile
        researchOutcomeContractFile
        driverPreambleFile
        agentPathsPreambleFile
        fragmentRegistryFile
        runInputDocumentFile
        buildInputDocumentFile
        ;
      driverExecBin = imageDriver.driverExecBin;
      orchestratorBin = imageDriver.orchestratorBin;
      driverEntry = imageDriver.driverEntry;

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
        ;

      inherit rosterWarnings;
    };

    packages = {
      inherit spindrift;
      launcher-currency = launcherCurrencyBin;
      spindrift-manpage = manpage;
      spindrift-bash-completion = bashCompletion;
      spindrift-fish-completion = fishCompletion;
      spindrift-zsh-completion = zshCompletion;
    }
    # The OCI image is not relevant for the bwrap runner (no image build/load).
    // lib.optionalAttrs (isLinux && runtime != "bwrap") { agent-image = image; }
    # The bwrap counterpart: one flake package standing in for the whole
    # agent closure, so freshness (issue #2667) has a single attr to realize.
    // lib.optionalAttrs (isLinux && runtime == "bwrap") { agent-closure = agentClosure; };

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
