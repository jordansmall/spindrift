# The engine. A pure function a Consumer flake calls with its own locked
# `nixpkgs` input and `system`; returns the agent image plus the `spindrift`
# CLI (as both `packages.spindrift` and `apps.default`).
#
# Takes the locked *input* rather than a pre-built `pkgs` so it can map a darwin
# `system` to its Linux twin and re-instantiate for the OCI image, keeping the
# agent's toolchain and the Consumer's dev shell from one pin (ADR 0002). The
# image is target-agnostic: REPO_SLUG, auth, and commit identity stay runtime
# env, never Nix options (ADR 0001).
{
  nixpkgs,
  system,
  overlays ? [ ],
  config ? { },
  # Project-specific tools baked into the image on top of the harness plumbing,
  # as a function of the (Linux) pkgs — the Consumer's language/toolchain surface.
  packages ? (_pkgs: [ ]),
  # Shell snippet the entrypoint runs after cloning, to warm toolchain caches
  # (e.g. fetch pinned deps). Baked into the image; default is a no-op.
  prefetch ? "",
  # Prompts below are baked into the image at /agent/prompts (see agentFiles), so
  # changing one requires an image rebuild; SPINDRIFT_PROMPT_DIR mounts an
  # override directory at runtime for zero-rebuild iteration.
  prompt ? builtins.readFile ../templates/default/prompts/issue-prompt.md,
  scoutPrompt ? builtins.readFile ../templates/default/prompts/scout-prompt.md,
  reviewPrompt ? builtins.readFile ../templates/default/prompts/review-prompt.md,
  # Opt-in: provisioned only when filerModel is non-empty (see agentsJsonTemplate).
  filerPrompt ? builtins.readFile ../templates/default/prompts/filer-prompt.md,
  # Provisioned by default; empty only when workerModel is set to "".
  workerPrompt ? builtins.readFile ../templates/default/prompts/worker-prompt.md,
  # The first-class N-agent roster (lib/roster.nix), rendered by the selected
  # Driver into --agents JSON (claude) or on-disk agents/*.md (opencode) below.
  # `null` resolves to `rosterLib.defaultRoster` built from the four deprecated
  # model knobs (scoutModel/reviewModel/filerModel/workerModel), so a Consumer
  # that has never heard of `roster` keeps its historical four agents. Setting
  # `roster` takes over agent composition entirely and ignores the legacy knobs.
  roster ? null,
  # Name-keyed model/effort shorthand forwarded into `rosterLib.defaultRoster`.
  # Only takes effect when `roster` is null -- an explicit `roster` always wins.
  byName ? { },
  conflictResolvePrompt ? builtins.readFile ../templates/default/prompts/conflict-resolve-prompt.md,
  # Driven instead of `prompt` on a fix box (FIX_PASS>0): the branch is already
  # checked out, so this skips scout/implement-from-scratch and goes straight to
  # check/fix/commit/push/watch-CI.
  fixPrompt ? builtins.readFile ../templates/default/prompts/fix-prompt.md,
  # Driven instead of `prompt` when DISPATCH_KIND=research (ADR 0022): the
  # researcher posts a verdict comment instead of implementing, so this replaces
  # the whole issue-prompt.md flow rather than sharing its COMMS/CHECK blocks.
  researchPrompt ? builtins.readFile ../templates/default/prompts/research-prompt.md,
  # Driven instead of `researchPrompt` in research's self-contained sub-mode
  # (ADR 0022): no repo, no clone -- the issue body/comments are the only input,
  # so this prompt skips the EXPLORE step entirely.
  researchSelfContainedPrompt ? builtins.readFile ../templates/default/prompts/research-self-contained-prompt.md,
  # The Conditional fragment registry (CONTEXT.md): rows of (gate, fragment, var)
  # that the entrypoint's single fragment loop and its `_subst` substitution
  # allowlist are both rendered from. Not Consumer-tunable; overridable here only
  # for the bats fixture-row test proving a new row needs no entrypoint edit.
  fragments ? import ./fragments.nix,
  # The directory the fragment registry's files live under, cp -r'd whole into
  # the image. Not Consumer-tunable; overridable here only so
  # nix/checks/prompts.nix can point buildTimeRejectVerdicts' lookup at a broken
  # fixture directory without touching the real templates tree.
  fragmentsDir ? ../templates/default/prompts/fragments,
  # Skill files baked into the image at /home/agent/.claude/skills so the
  # headless agent can invoke them without a runtime mount. Each element is
  # either a path/derivation (copied under its basename), or a { name; src; }
  # content entry baked under the given name by re-realizing src with the image's
  # own Linux pkgs — never a consumer host derivation, which would tag the
  # image's drvPath with the host system. SPINDRIFT_SKILLS_DIR at runtime mounts
  # over the same path and shadows all baked skills.
  skills ? [ ],
  # Non-secret run config baked into the `run` command as its built-in defaults;
  # a matching env var still wins at runtime, so one build can be re-pointed.
  defaults ? { },
  # Container runtime the launcher commands drive: "podman" (default), "docker",
  # or "rancher" (Rancher Desktop containerd mode; invokes nerdctl).
  runtime ? "podman",
  # The agent CLI Driver (ADR 0009): a build-time choice selecting one entry from
  # the lib/drivers/ registry, baked into the image (in-box half) and threaded to
  # the Go launcher as DRIVER (host-side half).
  driver ? "claude",
  # Fallback Linux builder for when the host can't realize the Linux image itself
  # (the stock-mac case). Fully qualified so podman needs no default registry.
  # Pinned by manifest-list digest for supply-chain safety — this container runs
  # with the consumer tree bind-mounted read-write, so a silently-updated :latest
  # would be a code-execution vector. To bump: pull the image, run
  # `podman image inspect --format '{{.RepoDigests}}' nixos/nix`, and update the
  # digest in lib/build-constants.nix and docs/reference.md.
  nixBuilderImage ? (import ./build-constants.nix).nixBuilderImage,
  # Bake a usable nix into the box (binary + a registered store DB + a
  # single-user, sandbox-off nix.conf) so `nix flake check` and `nix develop`
  # run inside the unprivileged throwaway container. On by default — this is the
  # nix-centric baseline every box gets; set to false for a lean, nix-free image.
  nixInBox ? true,
  # Self-test mode (ADR 0018): makes the /nix/store DIRECTORY (not its existing
  # contents, which stay root-owned and immutable) writable by the agent uid, so
  # a `nix flake check` inside the Box can build new store paths instead of
  # hitting EACCES. New paths land in the container's ephemeral copy-on-write
  # layer and die with the Box. Off by default: trades hermeticity for in-box
  # feedback, so the entrypoint prints a loud warning when enabled. Both runtimes
  # support it (ADR 0042): OCI bakes the writable directory into the image at
  # build time (chown, lib/image.nix); bwrap instead overlays an ephemeral tmpfs
  # upper on the host's real, unmodified store at run time.
  nixStoreWritable ? false,
  # Extra derivations whose closures are baked into the image contents and, when
  # nixInBox is on, registered in the store DB alongside the runtime closure — so
  # in-box nix sees them as already present instead of cold-substituting the
  # world on every Box. A function of the (Linux) pkgs, like `packages`, so
  # Consumer-supplied derivations stay correct on a darwin host.
  extraClosures ? (_pkgs: [ ]),
  # Short git revision injected into the binary via ldflags for `spindrift --version`.
  # Callers pass self.shortRev or self.rev; defaults to "unknown" for impure builds.
  revision ? "unknown",
}:
let
  # Single source of truth for the vendorHash values and the nix-builder digest
  # otherwise duplicated across nix build sites.
  buildConstants = import ./build-constants.nix;

  # Shared default-toolset instantiation and the Linux-twin system map — see
  # lib/nixpkgs-shared.nix for the memoization story.
  nixpkgsShared = import ./nixpkgs-shared.nix;

  # OCI images are Linux-only. Map the Consumer's (possibly darwin) system to
  # its Linux twin for the image.
  linuxSystem = nixpkgsShared.linuxTwin.${system};

  # Bundled into the single param preambles.runArtifacts and
  # preambles.buildArtifacts take — see lib/preambles.nix's runArtifacts comment
  # for the bundling rationale.
  systems = {
    host = system;
    linux = linuxSystem;
  };

  mergedConfig = {
    allowUnfree = true;
  }
  // config;

  # `import nixpkgs { ... }` is not memoized by Nix — every call site pays for
  # another fixed-point evaluation, and spindrift's own checkset calls mkHarness
  # ~100 times per `nix flake check`. For the default toolset (no Consumer
  # overlays, no Consumer config) consult the shared per-system cache a
  # `withSharedInstances`-wrapped nixpkgs input carries, falling back to a
  # per-call instantiation for a bare input. A Consumer passing `overlays` or
  # `config` gets its own instantiation — functions have no stable identity, so
  # those cannot be keyed on.
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

  # Host toolset: the launcher commands run on the Consumer's own system. Takes
  # the same overlays as the image so the tools pinned into the launchers
  # (gh/git/coreutils via runtimeInputs) can be overridden consistently. On a
  # Linux Consumer the two systems coincide, so this is the same instantiation.
  hostPkgs = if system == linuxSystem then pkgs else instantiate system;

  inherit (pkgs) lib;

  # Single source of truth for every runtime knob — name mapping, defaults, scope.
  # Generators below derive all per-knob output from this registry; no per-knob
  # lines appear anywhere else in this file.
  schema = import ./env-schema.nix;

  # Single source of truth for the completion renderers' subcommand candidate
  # lists and the man page SUBCOMMANDS section below.
  subcommandRegistry = import ./subcommands.nix;
  subcommands = subcommandRegistry;

  # The backend descriptor registry: one row per ISSUE_TRACKER/CODE_FORGE
  # backend, carrying the capability bits consumed by readOnlyCapabilityOk and
  # codeForgeRow/issueTrackerRow below.
  backends = import ./backends/default.nix;

  # Section taxonomy and man-page renderer, shared with flakeModule.nix and the
  # nix/checks/schema-drift.nix guards so none of them can drift from each
  # other (issue #461).
  renderers = import ./renderers.nix;

  # Nix→bash preamble marshalling shared by the entrypoint and the Go launcher
  # wrappers below; nix/checks/preambles.nix pins each renderer's output shape.
  preambles = import ./preambles.nix;

  # Marker-delimited slicing/injection primitives;
  # nix/checks/prompt-inject.nix pins each primitive's behavior.
  promptInject = import ./prompt-inject.nix;
  inherit (promptInject) sliceFromMarker injectSection;

  # Pure-data registry of the harness-owned shared prompt blocks: the single
  # source of truth for each block's id/marker/source/slice-range/kinds, driving
  # the marker constants and canonical text below.
  # nix/checks/prompt-contract.nix pins the registry's cross-field invariants and
  # its canonical text against the real prompt sources. Only the research block
  # below slices its own source directly, since it needs the
  # RESEARCH_VERDICTS-rendered text, not the registry's unrendered default.
  promptContract = import ./prompt-contract.nix;
  inherit (promptContract) byId;

  # The conditional prompt steps (skill preamble, FILE ISSUES, AUTO-FORMAT,
  # AUTO-LINT, CI FAILURE) live as fragment files under /agent/prompts/fragments
  # rather than heredocs in agent/entrypoint.sh. Not Consumer-tunable: a
  # SPINDRIFT_PROMPT_DIR override supplies its own fragment for whichever knob it
  # enables, exactly as it already must supply filer-prompt.md.
  fragmentsSourceDir = fragmentsDir;

  # The SPINDRIFT_OUTCOME contract (the LAND THE CHANGE / WATCH CI / OUTCOME /
  # IF BLOCKED sections) is harness-owned: a Consumer `prompt` that drops it
  # would ship an agent that never emits the outcome line, so the launcher never
  # learns the PR and the merge/takeover silently never happens. Sliced from the
  # default prompt's own heading rather than duplicated into a second file, so
  # the injected block and the default prompt's sections cannot drift apart.
  outcomeContractMarker = (byId "outcome").marker;
  outcomeContract = promptContract.canonicalText.outcome;

  injectOutcomeContract = injectSection outcomeContractMarker outcomeContract;

  # COMMS and CHECK/COMMIT are shared with fix-prompt.md, sliced the same way as
  # the outcome contract above so fix-prompt.md receives the byte-identical
  # section at bake/run time. COMMS runs from its own heading up to SCOUT
  # (issue-prompt-only — the fix prompt runs FIX in its place); CHECK/COMMIT runs
  # from CHECK up to REVIEW (also issue-prompt-only — a fix pass has no review).
  commsMarker = (byId "comms").marker;
  commsBlock = promptContract.canonicalText.comms;
  checkMarker = (byId "check").marker;
  checkBlock = promptContract.canonicalText.check;

  injectComms = injectSection commsMarker commsBlock;
  injectCheckCommit = injectSection checkMarker checkBlock;

  # CODE COMMENTS is a fourth shared block, sliced from its own heading up to
  # CHECK, so a fix prompt gets the same comment-discipline rule an issue prompt
  # already carries inline.
  codeCommentsMarker = (byId "code-comments").marker;
  codeCommentsBlock = promptContract.canonicalText."code-comments";

  injectCodeComments = injectSection codeCommentsMarker codeCommentsBlock;

  # Order matters: COMMS, CODE COMMENTS, CHECK/COMMIT, outcome contract, so a fix
  # prompt missing all four ends up with them in issue-prompt.md's order —
  # mirrors agent/entrypoint.sh so the baked and mounted-override cases agree.
  injectFixSharedBlocks =
    promptText: injectOutcomeContract (injectCheckCommit (injectCodeComments (injectComms promptText)));

  # research-prompt.md carries its own harness-owned outcome contract rather than
  # sharing issue-prompt.md's blocks: posting the verdict comment and emitting
  # the outcome line, sliced from its own "# POST THE VERDICT" heading through
  # EOF so injected block and default prompt cannot drift apart.
  researchPromptSource = builtins.readFile ../templates/default/prompts/research-prompt.md;
  researchOutcomeContractMarker = (byId "research-verdict").marker;
  # Render the verdict contract from the RESEARCH_VERDICTS knob *before* slicing
  # the outcome contract and baking the prompt, so default and custom verdict
  # sets flow through the same rendering path -- there is no
  # byte-identical-to-template no-op case.
  researchVerdicts = import ./research-verdicts.nix;
  researchVerdictsKnob = mergedDefaults.researchVerdicts or "";
  researchPromptRendered = researchVerdicts.render researchVerdictsKnob researchPrompt;
  # Same rendering for the self-contained sub-mode prompt, so a custom
  # RESEARCH_VERDICTS knob reaches both prompts.
  researchSelfContainedPromptRendered = researchVerdicts.render researchVerdictsKnob researchSelfContainedPrompt;
  researchPromptSourceRendered = researchVerdicts.render researchVerdictsKnob researchPromptSource;
  researchOutcomeContract = sliceFromMarker researchOutcomeContractMarker researchPromptSourceRendered;
  injectResearchOutcomeContract = injectSection researchOutcomeContractMarker researchOutcomeContract;

  # The Driver registry (ADR 0009); driverEntry is the selected Driver's
  # in-box half — invocation binary/flags, agent-config rendering, skill
  # wiring, and outcome extraction — baked into the image below.
  driverRegistry = import ./drivers/default.nix { inherit lib; };
  driverEntry =
    driverRegistry.entries.${driver}
      or (throw "mkHarness: unknown driver '${driver}'; known drivers: ${lib.concatStringsSep ", " (lib.attrNames driverRegistry.entries)}");

  # The OCI image name, scoped to the selected Driver: the default claude Driver
  # keeps the historical `spindrift` name so existing tags stay valid, while any
  # other Driver realises its own `spindrift-<driver>` artifact. Threaded through
  # image.nix and preambles so the built image, its content-hash tag, and the
  # launcher's load/re-tag all agree.
  imageName = if driver == "claude" then "spindrift" else "spindrift-${driver}";

  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Built-in run defaults derived from the schema; the Consumer's `defaults` arg
  # overrides them per key, and a matching env var overrides those again at
  # runtime. Non-strict because flakeOptionEntries spans every flakeOption-
  # flagged schema key, most of which can't guarantee a `.default` (e.g.
  # devShellName), unlike the roster helper's four model keys.
  schemaDefaults = rosterSchemaDefaults.readSchemaDefaults { strict = false; } flakeOptionEntries;
  mergedDefaults = schemaDefaults // defaults;

  # The two registry rows the *selected* CODE_FORGE and ISSUE_TRACKER knob values
  # pick out, so the capability bits below don't hand-duplicate per-backend facts
  # already declared once in the registry. Falls back to `{ }` on a bogus name
  # (every capability bit then reads `false`) rather than throwing: Go's own
  # validate() already rejects an invalid CODE_FORGE/ISSUE_TRACKER at runtime.
  codeForgeRow = lib.findFirst (r: r.name == mergedDefaults.codeForge) { } backends;
  issueTrackerRow = lib.findFirst (r: r.name == mergedDefaults.issueTracker) { } backends;

  # Capability signals derived from the resolved backend rows above, threaded
  # into the Launcher input document's `run` artifacts as HOST_MEDIATED_REMOTE /
  # OUTBOX_RELAY_CAPABLE / IN_BOX_UNREACHABLE_TRACKER / FULLY_LOCAL; the Go side
  # reads them via docArtifact instead of re-deriving backend facts itself.
  hostMediatedRemote = codeForgeRow.hostMediatedRemote or false;
  outboxRelayCapable = codeForgeRow.outboxRelayCapable or false;
  inBoxUnreachableTracker = issueTrackerRow.inBoxUnreachableTracker or false;
  fullyLocal = hostMediatedRemote && inBoxUnreachableTracker;

  # Tracker/forge axis strings from the same registry rows, threaded into the
  # `run` artifacts as TRACKER_AXIS_READ / TRACKER_AXIS_WRITE /
  # TRACKER_AXIS_FILER / FORGE_BACKEND. cmd/launcher/main.go's matching switch
  # reads the same lib/backends/default.nix row fields, so neither side carries a
  # hand-rolled per-backend switch.
  trackerAxisRead = issueTrackerRow.trackerAxisRead or "GITHUB";
  trackerAxisWrite = issueTrackerRow.trackerAxisWrite or "GITHUB";
  trackerAxisFiler = issueTrackerRow.trackerAxisFiler or "GH";
  forgeBackend = codeForgeRow.forgeBackend or "GH";

  # Eval-time choices guard: lib/flakeModule.nix's generated Consumer options use
  # `types.enum`, but that only protects Consumers going through the flake
  # module. A Consumer calling `mkHarness { defaults = {...}; }` directly could
  # otherwise set an invalid choice value with no eval-time protection at all.
  # Distinct from nix/checks/schema-drift.nix's schemaChoiceIssues, which
  # validates the *schema's own* choices shape -- this validates a resolved
  # *runtime* value, at the one point every entry path funnels through.
  choiceViolations = lib.filter (issue: issue != null) (
    lib.mapAttrsToList (
      key: entry:
      let
        choices = entry.choices or null;
        value = mergedDefaults.${key} or null;
        # toString null == "" would render indistinguishably from a legitimate
        # empty string, hiding exactly which value was rejected.
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

  # Eval-time coherence assert for NETWORK_MODE. network.mode and the raw
  # per-runtime knobs (network.podman / network.bwrapUnshare) say the same thing
  # with no precedence rule between them -- a Consumer that sets both must pick
  # one rather than have mkHarness silently choose a winner.
  # Separately, network.mode = no-host-loopback has no bwrap rendering: a bwrap
  # Box already isolates its network namespace by default (hardened pasta helper
  # -- working egress, host loopback blocked), so no-host-loopback would render
  # byte-identical to "open". It stays rejected not because it's mechanically
  # impossible but because a distinct choice with no distinct rendering would
  # mislead a Consumer into thinking they get something "open" doesn't already
  # give them.
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

  # Drives lib/image.nix's fj (forgejo-cli) bake, so a github-backend Consumer's
  # image never carries an unused CLI.
  forgejoBackend =
    (mergedDefaults.issueTracker or "github") == "forgejo"
    || (mergedDefaults.codeForge or "github") == "forgejo";

  # Unknown defaults keys are caught at eval time — a typo like `basebranch`
  # would otherwise be silently ignored, never baked, never surfaced.
  unknownDefaultKeys = lib.filter (k: !(lib.hasAttr k flakeOptionEntries)) (lib.attrNames defaults);

  # An explicit `roster` arg always wins; otherwise it's resolved from the four
  # deprecated per-agent model knobs so an existing Consumer keeps building the
  # same four agents it always has.
  rosterLib = import ./roster.nix { inherit lib; };
  # The one schema-defaults reader, reused above in non-strict mode for
  # schemaDefaults; see lib/roster-schema-defaults.nix for why it's a separate
  # file both this and roster.nix import directly.
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
  # Drops any entry whose model is the explicit "" opt-out sentinel, after
  # normalizeRoster (which deliberately never filters) and before any Driver or
  # downstream consumer of finalRoster sees the roster.
  keptRoster = rosterLib.dropOptedOut resolvedRoster;

  # reviewEffort is the one legacy knob that overrides an already-resolved
  # roster's reviewer entry regardless of roster source (contrast the four model
  # knobs above) -- applied post-normalize so it reaches both the defaultRoster
  # branch and a Consumer-supplied explicit roster identically.
  finalRoster =
    let
      reviewEffort = mergedDefaults.reviewEffort or "";
    in
    if reviewEffort == "" then
      keptRoster
    else
      map (e: if e.name == "reviewer" then e // { effort = reviewEffort; } else e) keptRoster;

  # --agents JSON, rendered by the selected Driver (ADR 0009) from the resolved
  # roster above, so a Driver with a different agent-config shape can supply its
  # own renderer without touching mkHarness.
  agentsJsonTemplate = driverEntry.agentsJsonTemplate { roster = finalRoster; };

  # Roster/review-loop bools threaded into the Launcher input document's `run`
  # artifacts as FILER_ENABLED / WORKER_PROVISIONED / REVIEW_LOOP_INLINE /
  # REVIEW_LOOP_ORCHESTRATOR; the Go side reads them via docArtifact instead of
  # re-deriving roster membership itself.
  # filerEnabled/workerProvisioned deliberately key off agentsJsonTemplate's
  # rendered output rather than finalRoster: lib/drivers/opencode.nix's
  # agentsJsonTemplate always returns "" regardless of roster contents (it
  # provisions subagents via on-disk agents/*.md instead, see driverAgentFiles
  # below), so a finalRoster-only check would silently flip WORKER_PROVISIONED
  # true for opencode even though opencode's mechanism never carries that key.
  agentsJsonAttrs = if agentsJsonTemplate == "" then { } else builtins.fromJSON agentsJsonTemplate;
  filerEnabled = agentsJsonAttrs ? filer;
  workerProvisioned = agentsJsonAttrs ? worker;
  reviewLoopInline = !mergedDefaults.orchestratorEnabled;
  reviewLoopOrchestrator = mergedDefaults.orchestratorEnabled;

  # On-disk subagent files, rendered by the selected Driver the same way
  # agentsJsonTemplate is above: a Driver with no on-disk agent-config mechanism
  # (claude.nix) returns { } here, since its subagents ride agentsJsonTemplate's
  # --agents JSON flag instead.
  driverAgentFiles = driverEntry.agentFilesTemplate { roster = finalRoster; };

  # Nix-baked name -> prompt file map, read at runtime by entrypoint.sh's generic
  # per-agent prompt injection loop so a custom Nth agent's prompt resolves the
  # same way as the built-in names. `rosterLib.normalizeRoster` above guarantees
  # every entry carries a `promptFile`, so there's no fallback to re-derive here.
  agentsPromptFilesJson = builtins.toJSON (
    lib.listToAttrs (
      map (e: {
        name = e.name;
        value = e.promptFile;
      }) finalRoster
    )
  );

  # Roster entries carrying their own prompt (a custom agent, as opposed to the
  # built-in ones baked separately below). Omitting `prompt` entirely is treated
  # the same as setting it to null.
  customRosterPromptFiles = lib.filter (e: (e.prompt or null) != null) finalRoster;

  # The Driver's in-box half (ADR 0009), rendered into agent/entrypoint.sh's
  # DRIVER_* vars and function definitions. Shared between the image preamble and
  # the bats harness file so neither can drift from the other.
  driverPreamble = driverRegistry.renderPreamble driverEntry;

  # The baked /agent/* path literals (contracts, registries, prompts dir) and
  # their rendered fallback-preserving preamble: the same nix binding
  # lib/image.nix's agentFiles cp destinations read, so a rename here updates
  # both the image's copy destination and the entrypoint's baked default.
  agentPaths = import ./agent-paths.nix;
  agentPathsPreamble = preambles.renderAgentPathsPreamble agentPaths;

  # The Conditional fragment registry, rendered into agent/entrypoint.sh's
  # fragment loop input and `_subst` substitution allowlist: a bash array of
  # "gate|fragment|var" rows, plus every var an envsubst call must know about
  # (each row's own var, plus any extraSubstVars a fragment's body interpolates).
  # entrypoint.sh's loop and `_subst` are generic over this data — a new row
  # needs no entrypoint edit. Shared with the bats harness file so neither can
  # drift.
  fragmentRegistryRows = map (row: "${row.gate}|${row.fragment}|${row.var}") fragments;
  fragmentSubstVars = lib.concatMap (row: [ row.var ] ++ (row.extraSubstVars or [ ])) fragments;
  fragmentRegistryPreamble =
    "_FRAGMENT_ROWS=(\n"
    + lib.concatMapStrings (row: "  " + lib.escapeShellArg row + "\n") fragmentRegistryRows
    + ")\n"
    + "_FRAGMENT_SUBST_VARS=(\n"
    + lib.concatMapStrings (v: "  " + lib.escapeShellArg v + "\n") fragmentSubstVars
    + ")\n";

  # The same registry as JSON, baked into the image for the Go
  # `driver-exec assemble-prompt` verb's `--registry` flag. A sibling of
  # fragmentRegistryPreamble above, not a replacement -- the bash preamble still
  # drives entrypoint.sh's own fragment loop.
  fragmentsRegistryJson = builtins.toJSON fragments;

  # lib/prompt-contract.nix's validateMarkers list as JSON, baked into the image
  # for `driver-exec assemble-prompt`'s `--validate-markers-registry` flag.
  promptContractRegistryJson = builtins.toJSON promptContract.validateMarkers;

  # lib/prompt-contract.nix's forbiddenMarkers list as JSON, baked into the image
  # for `driver-exec readonly-guards`'s `--forbidden-markers-registry` flag.
  forbiddenMarkersRegistryJson = builtins.toJSON promptContract.forbiddenMarkers;

  # Build-time reject arm: resolves both validateMarkers "reject" rows against
  # this build's own static knowledge. `reviewer-verdict` is gated on
  # orchestratorEnabled and checked against the literal reviewPrompt this image
  # bakes. `verdict-comment-relay` is gated on read-only research and checked
  # against the research-verdict-*-readonly.md fragment issueTracker statically
  # selects -- github and forgejo are the only trackers with such a fragment;
  # local/jira have none, so the suffix is null and the id is omitted from
  # contentByRowId, resolving to "advise". buildTimeRejectOk below is what
  # actually forces this list's evaluation at build time.
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

  # Single spelling of "is this a FILER_FILE_DIRECT*-gated row", shared by
  # readOnlyReachableFragmentRows' exclusion list and directFileFragmentRows
  # below. Substring, not an equality list: a future FILER_FILE_DIRECT_GITLAB
  # gate added only to lib/fragments.nix would otherwise silently stay inside the
  # forbidden-marker scan it is meant to be exempted from.
  isDirectFileGate = row: lib.hasInfix "FILER_FILE_DIRECT" row.gate;

  # Structural forbidden-marker check: the fragment rows the fragment-body scan
  # actually reaches -- every fragments.nix row EXCEPT the ones whose `gate` name
  # itself proves the fragment is access-mode-aware (or independently
  # authorized), so a legitimate negation ("do NOT `git push`") in the read-only
  # half of an access-mode pair is never mistaken for a leak. Unconditional,
  # unlike buildTimeRejectVerdicts above: a forbidden marker shipped in the
  # corpus is a problem for any Consumer that might configure boxAccessReadOnly,
  # not just this build.
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

  # Every non-exempt fragment's raw content, plus the three shared top-level
  # templates' raw (unsubstituted) text, scanned for any forbiddenMarkers
  # "substring" row -- see lib/prompt-contract.nix's
  # buildTimeForbiddenMarkerViolations doc comment for the full design.
  forbiddenMarkerViolations = promptContract.buildTimeForbiddenMarkerViolations {
    fragmentContentByFile = builtins.listToAttrs (
      map (row: {
        name = row.fragment;
        value = builtins.readFile (fragmentsDir + "/${row.fragment}");
      }) readOnlyReachableFragmentRows
    );
    # Deliberately just these three. fix-prompt.md and
    # research{,-self-contained}-prompt.md are shared templates too and do carry
    # forbiddenMarkers substrings (a negation and a descriptive mention), so
    # adding them here would need those handled first.
    templateContentByFile = {
      "issue-prompt.md" = prompt;
      "review-prompt.md" = reviewPrompt;
      "filer-prompt.md" = filerPrompt;
    };
  };

  # Forces forbiddenMarkerViolations' evaluation the same way buildTimeRejectOk
  # forces buildTimeRejectVerdicts below -- consumed by `assert
  # forbiddenMarkerCheckOk;` ahead of the returned attrset.
  forbiddenMarkerCheckOk =
    if forbiddenMarkerViolations == [ ] then
      true
    else
      throw "mkHarness: structural forbidden-marker check failed -- a forbidden marker (lib/prompt-contract.nix forbiddenMarkers) must live only in a gate-paired fragment (issue #2510):\n${
        lib.concatMapStringsSep "\n" (
          v: "  ${v.file}: contains forbidden marker '${v.marker}' (${v.id})"
        ) forbiddenMarkerViolations
      }";

  # The FILER_FILE_DIRECT*-gated fragment rows (ADR 0041): the ones whose
  # fragment tells the agent to run `gh issue create`/`fj issue create` directly,
  # never rendered into a research prompt by design.
  directFileFragmentRows = builtins.filter isDirectFileGate fragments;

  # The research prompts scanned for a direct-file placeholder. Hand-typed rather
  # than derived from a directory listing, so a future third
  # research*-prompt.md would silently miss this scan -- named so
  # nix/checks/prompts.nix can read it back through `internals` below and assert
  # its keys still cover every research*-prompt.md on disk.
  researchPromptContentByName = {
    "research-prompt.md" = researchPromptRendered;
    "research-self-contained-prompt.md" = researchSelfContainedPromptRendered;
  };

  researchDirectFileViolations = promptContract.buildTimeResearchDirectFileViolations {
    inherit directFileFragmentRows researchPromptContentByName;
  };

  # Forces researchDirectFileViolations' evaluation the same way
  # forbiddenMarkerCheckOk forces forbiddenMarkerViolations above -- consumed
  # by `assert researchDirectFileCheckOk;` ahead of the returned attrset.
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

  # In-box Driver runner: runs one Driver invocation, direct or inside the
  # Project devShell, tees the stream to a log path, and filters heartbeats
  # in-process. Built for Linux (pkgs, not hostPkgs), and goes through the Driver
  # seam (ADR 0009) rather than a heartbeat package directly.
  #
  # INVARIANT: the agent image drvPath must not change when host-side launcher
  # code outside this binary's import closure is modified (e.g. test-only
  # launcher commits). Hence the deliberately tight fileset below, with
  # *_test.go excluded. If a new import is added outside this closure the build
  # fails loudly (missing package) — that is the intended failure mode.
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
        ) ../cmd/launcher/internal/registryproxy)
      ];
    };
    # Same go.mod/go.sum as launcherBin above, but NOT the same vendorHash:
    # `go mod vendor` prunes to packages actually imported by the source tree
    # present, and driver-exec's fileset is narrower than launcherBin's full
    # cmd/launcher tree, so the two vendor differently off identical go.mod.
    vendorHash = buildConstants.driverExecVendorHash;
    subPackages = [ "driver-exec" ];
    meta.license = lib.licenses.mit;
  };

  # In-box orchestrator (ADR 0007): the Go binary entrypoint.sh hands the
  # implementor pass off to when ORCHESTRATOR_ENABLED is set, instead of calling
  # driver-exec directly. Its fileset largely mirrors driverExecBin's, for the
  # same drvPath-stability reason.
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
        ) ../cmd/launcher/internal/agentpaths)
      ];
    };
    vendorHash = buildConstants.driverExecVendorHash;
    subPackages = [ "orchestrator" ];
    meta.license = lib.licenses.mit;
  };

  # lib/image.nix's parameters, grouped into six attrsets. The host-native mirror
  # derivations/documents further down this file (promptDir, driverPreambleFile,
  # runArtifacts, and others) read the same fields off these groups rather than
  # re-deriving them from the bare local values.
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
      codeCommentsBlock
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
      filerPrompt
      workerPrompt
      conflictResolvePrompt
      fixPrompt
      fragmentsSourceDir
      fragmentRegistryPreamble
      ;
    # Both research prompts carry the verdict contract rendered from the
    # configured set; the default knob value is a no-op.
    researchPrompt = researchPromptRendered;
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

  # Each harness-owned prompt block as a host store path, so checks can diff it
  # against what a Consumer prompt lacking the block gets injected with — proof
  # the two cannot drift apart.
  outcomeContractFile = hostPkgs.writeText "outcome-contract.md" imageContracts.outcomeContract;
  commsContractFile = hostPkgs.writeText "comms-contract.md" imageContracts.commsBlock;
  checkContractFile = hostPkgs.writeText "check-contract.md" imageContracts.checkBlock;
  codeCommentsContractFile = hostPkgs.writeText "code-comments-contract.md" imageContracts.codeCommentsBlock;
  researchOutcomeContractFile = hostPkgs.writeText "research-outcome-contract.md" imageContracts.researchOutcomeContract;

  # The three rendered preambles as host store-path files. The bats harness
  # prepends each before exec-ing the entrypoint, so tests exercise the exact
  # bytes mkHarness bakes into the image — never a hand-copied duplicate or an
  # entrypoint fallback literal.
  driverPreambleFile = hostPkgs.writeText "driver-preamble.sh" imageDriver.driverPreamble;
  agentPathsPreambleFile = hostPkgs.writeText "agent-paths-preamble.sh" agentPathsPreamble;
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

  # The baked-skills directory as a host store path (native-buildable on darwin),
  # laid out exactly as lib/image.nix bakes it: each skill is a `<name>/SKILL.md`
  # directory (Claude Code discovers skills only as directories). A
  # { name; src; } entry is realized with hostPkgs — this is a host-only test
  # artifact, never an input to the Linux image, so it carries no
  # host-independence requirement.
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

  # Extracts the 32-char nix store hash from a store path as PLAIN TEXT. Store
  # paths are always `/nix/store/<32-char-base32-hash>-<name>`, so characters
  # 11–42 (0-indexed) are the hash. Shared by imageHash and launcherCurrencyHash
  # so the magic numbers live in one place.
  storeHashOf = path: builtins.substring 11 32 path;

  # The image's store path as PLAIN TEXT (context discarded), so the launcher
  # commands embed the exact Linux image path WITHOUT taking a build-time
  # dependency on it. That lets `build`/`run` — and `nix flake check` — build
  # natively on darwin, while realizing the image stays an explicit, Linux-only
  # `nix build .#agent-image`.
  imagePath = builtins.unsafeDiscardStringContext (toString image);

  # The nix store hash extracted from imagePath. Used as the content-hash
  # image tag so that a changed flake produces a new hash → the old tag is
  # absent → run rebuilds.
  imageHash = storeHashOf imagePath;

  # The image's `.drv` path, also context-discarded. `build` realizes this with
  # `nix build "<drv>^*"` before loading, so a fresh machine builds the image
  # instead of failing on an unrealized path — while discarding the context
  # keeps `nix flake check` and the launcher builds off any Linux build. Reading
  # `.drvPath` instantiates the derivation at eval time, so the .drv exists in
  # the store by the time `build` runs; only realizing it needs a Linux builder.
  imageDrv = builtins.unsafeDiscardStringContext image.drvPath;

  # bwrap runner: store paths for the agent files and env, context-discarded so
  # the launcher commands embed the exact paths without a build-time dependency.
  # Reading `.drvPath` instantiates each derivation at eval time (creating the
  # .drv file) but does not realize the output — `bwrap build` does that.
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

  # The bwrap freshness dimension needs ONE comparable output path standing in
  # for "the bwrap agent closure as a whole". linkFarm bundles agentFiles +
  # agentEnv into a single derivation whose output path changes whenever either
  # sub-closure does, without merging their directory trees (which
  # agentFiles/agentEnv are not guaranteed not to collide on).
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
  ];
  agentClosurePath = builtins.unsafeDiscardStringContext (toString agentClosure);

  # runnerKind collapses the runtime knob to the two adapter families the
  # launcher knows: "bwrap" (daemonless) or "oci" (podman/docker).
  runnerKind = if runtime == "bwrap" then "bwrap" else "oci";

  # One renderer over flakeOption schema entries, used by both the entrypoint's
  # Box-side preamble and the document's `settings` section below. Box env is
  # launcher→Box plumbing, not an operator surface (ADR 0020).
  renderDefaultsPreamble =
    args: preambles.renderDefaultsPreamble (args // { inherit flakeOptionEntries mergedDefaults; });

  # The Launcher input document's `settings` section (ADR 0020): every
  # flakeOption knob's resolved value (mergedDefaults), keyed by env var name.
  documentSettings = lib.mapAttrs' (
    key: entry: lib.nameValuePair entry.env (toString mergedDefaults.${key})
  ) flakeOptionEntries;

  # The document's `run`/`build` artifacts sections (ADR 0020): the nix-computed
  # plumbing — image refs, agent files, driver name, capability bits.
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
      reviewLoopInline
      reviewLoopOrchestrator
      # Unlike nixConfigPath below (blanked to "" when nixInBox is off),
      # nixStoreWritable always renders the Consumer's raw knob value; the
      # AND-gate with NixConfigFile lives in bwrap.go, not here.
      nixStoreWritable
      ;
    driverEntry = imageDriver.driverEntry;
    prefetch = imageKnobs.prefetch;
    imageName = imageKnobs.imageName;
    boxEnvVars = preambles.renderBoxEnvVarsList schema;
    # bwrap-only: renders as "" when nixInBox is off, matching how the OCI branch
    # never gets this key at all -- the ephemeral overlay store's nix.conf is
    # only relevant when the Box actually gets in-box nix.
    nixConfigPath = if nixInBox then nixConfigFilePath else "";
    nixConfigDrv = if nixInBox then nixConfigFileDrv else "";
    # Unlike nixConfigPath, the syscall filter is a bwrap-hardening concern
    # orthogonal to nix-in-box -- it always renders its real path.
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
      ;
    imageName = imageKnobs.imageName;
    # See runArtifacts' nixConfigPath comment above.
    nixConfigDrv = if nixInBox then nixConfigFileDrv else "";
    inherit syscallFilterDrv;
  };

  # The rendered documents as host store-path JSON files. The generated wrapper
  # passes exactly one nix-computed argument, `--input <path>`.
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

  # The Go launcher binary, built hermetically by buildGoModule.
  #
  # To recompute vendorHash after a go.mod/go.sum change, run:
  #   nix build --impure --expr \
  #     'let flake = builtins.getFlake (toString ./.); \
  #      pkgs = import flake.inputs.nixpkgs { system = builtins.currentSystem; }; \
  #      in pkgs.buildGoModule { pname="x"; version="0"; \
  #      src = ./cmd/launcher; \
  #      vendorHash = pkgs.lib.fakeHash; }'
  # and set it in lib/build-constants.nix's launcherVendorHash, committing
  # go.sum and the hash together. launcherCurrencyBin and driverExecBin vendor
  # the same go.mod/go.sum against narrower filesets, so `go mod vendor` prunes
  # differently and each needs its own recompute — the hashes are NOT
  # interchangeable.
  launcherBin = hostPkgs.buildGoModule {
    pname = "spindrift-launcher";
    version = spindriftVersion;
    src = launcherSrc;
    modRoot = "cmd/launcher";
    vendorHash = buildConstants.launcherVendorHash;
    subPackages = [ "." ]; # build only the launcher; driver-exec is in-box only
    # go test ./... already runs, vendored and offline, as the launcher-go-test
    # check (nix/checks/go.nix) against the same source.
    doCheck = false;
    ldflags = [
      "-X main.version=${spindriftVersion}"
      "-X main.revision=${revision}"
    ];
    meta.license = lib.licenses.mit;
  };

  # A revision-independent sibling of launcherBin (ADR 0043). launcherBin bakes
  # `-X main.revision=${revision}`, moving its store path on every commit --
  # even docs-only ones. Staleness detection needs a hash stable across
  # revision-only changes, so this binary is never invoked, only its store hash
  # read, and its ldflags drop `-X main.revision=...` entirely.
  # `spindriftVersion` still reads .release-please-manifest.json, so this hash
  # moves once per release -- but not once per commit, which is the churn this
  # derivation exists to avoid.
  #
  # src is scoped with lib.fileset, NOT launcherSrc: launcherSrc copies ../docs
  # alongside cmd/launcher for launcherBin's checkPhase, and pulling docs in here
  # would make a docs-only commit move this hash too, defeating the point.
  #
  # The fileset is a directory-level approximation of the launcher's import
  # graph, not the graph itself: go.mod, go.sum, and every non-test .go file
  # under cmd/launcher, minus the driver-exec, orchestrator, and quickstart
  # subtrees (independent `package main`s, unreachable from the launcher's
  # imports). ~13 directories included here are outside the launcher's real
  # import graph (e.g. internal/passmachine is orchestrator-only), so perturbing
  # those still moves this derivation's outPath.
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
    # No modRoot (unlike launcherBin): the fileset's `root` is already
    # ../cmd/launcher, so the resulting src's top level IS cmd/launcher's
    # contents. Same go.mod/go.sum as launcherBin but a narrower fileset, so
    # `go mod vendor` prunes differently -- hence its own
    # launcherCurrencyVendorHash rather than reusing launcherVendorHash.
    vendorHash = buildConstants.launcherCurrencyVendorHash;
    subPackages = [ "." ];
    doCheck = false;
    ldflags = [
      "-X main.version=${spindriftVersion}"
      # main.revision intentionally omitted -- see comment above.
    ];
    meta.license = lib.licenses.mit;
  };

  # Same context-discarding trick as imagePath above -- output paths are computed
  # at eval time, so reading this does NOT force a build.
  launcherCurrencyPath = builtins.unsafeDiscardStringContext (toString launcherCurrencyBin);

  # Used by the freshness probe to compare the loaded launcher's store hash
  # against the one the current flake would produce.
  launcherCurrencyHash = storeHashOf launcherCurrencyPath;

  # Single-verb wrapper execing `launcher build`. Off the flake surface: it
  # exists only as a bats/equivalence test fixture for build-time preamble
  # baking.
  build =
    (hostPkgs.writeShellApplication {
      name = "build";
      # sqlite3 backs `launcher build`'s bwrap+nixInBox store-DB snapshot step
      # (ADR 0042) -- this fixture mirrors spindriftBin's real runtimeInputs.
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

  # Shared shell body used by both the spindrift CLI and the `run` test fixture:
  # sources harness.env (secrets, gitignored, read from $PWD since the harness is
  # a store path with no working tree) before execing the Go binary (ADR 0007).
  # No knob or artifact env export lives here — those flow via the --input
  # document (ADR 0020), so the wrapper bakes nothing per-knob.
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

  # The three completion scripts follow the man page's build-time-only pattern:
  # no committed copy, out of `nix run .#regen`, coverage-guarded by
  # nix/checks/schema-drift.nix.
  bashCompletionScript = renderers.renderBashCompletion schema subcommandRegistry;

  bashCompletion = hostPkgs.runCommand "spindrift-bash-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "spindrift-completion.bash" bashCompletionScript} \
      "$out/share/bash-completion/completions/spindrift"
  '';

  fishCompletionScript = renderers.renderFishCompletion schema subcommandRegistry;

  fishCompletion = hostPkgs.runCommand "spindrift-fish-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "spindrift.fish" fishCompletionScript} \
      "$out/share/fish/vendor_completions.d/spindrift.fish"
  '';

  zshCompletionScript = renderers.renderZshCompletion schema subcommandRegistry;

  zshCompletion = hostPkgs.runCommand "spindrift-zsh-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "_spindrift" zshCompletionScript} \
      "$out/share/zsh/site-functions/_spindrift"
  '';

  # The spindrift CLI: passes the rendered Launcher input document via
  # --input and execs the Go launcher (ADR 0020) — no per-knob env export.
  # Exposed as packages.spindrift, apps.default, and in devShells.
  # The man page is joined into the same output so `man spindrift` resolves
  # from the dev shell (nixpkgs adds share/man to MANPATH) and on install.
  spindriftBin =
    (hostPkgs.writeShellApplication {
      name = "spindrift";
      # sqlite3 backs `launcher build`'s bwrap+nixInBox store-DB snapshot step
      # (ADR 0042). Whether a given command needs it is a runtime decision (the
      # Consumer's nixInBox knob, read from the input document), not something
      # this derivation can gate per-Consumer, so it carries sqlite3
      # unconditionally. The `run` fixture below never runs `build`, so it alone
      # can omit sqlite3.
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

  # Single-verb wrapper execing `launcher dispatch`. Off the flake surface: it
  # exists only as a bats/equivalence test fixture for dispatch-time preamble
  # baking.
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

  # Realizing the Linux image on darwin needs a Linux builder, so only offer it
  # as a package where it can actually build; the launcher commands (which merely
  # reference its path) are always available. `nix flake check` on darwin thus
  # never forces a Linux build.
  isLinux = system == linuxSystem;

  # The four per-agent model knobs are superseded by `roster`. Checked against
  # the Consumer's own `defaults` arg (not mergedDefaults, which always carries
  # every schema key) so the warning fires only when the Consumer actually set
  # one. stderr-only, so it never changes a derivation's output hash -- a
  # Consumer on the legacy knobs and one on an equivalent `roster` still produce
  # byte-identical images.
  legacyKnobsSet = lib.filter (k: defaults ? ${k}) [
    "scoutModel"
    "reviewModel"
    "filerModel"
    "workerModel"
  ];
  deprecationMsg = "spindrift: the per-agent model knobs (${lib.concatStringsSep ", " legacyKnobsSet}) are deprecated and will be removed; migrate to the `roster` option (see docs/reference.md).";

  # Forces buildTimeRejectVerdicts' evaluation: builtins.all must evaluate every
  # element to decide its result, so a `throw` from one element's "reject" branch
  # propagates through it and then through the `assert` below -- there is no lazy
  # element `assert` skips past. "reject" throws (unrecoverable build failure),
  # "advise" is a non-fatal trace to stderr, "ok" is silent.
  buildTimeRejectOk = builtins.all (
    v:
    if v.verdict == "reject" then
      throw v.message
    else if v.verdict == "advise" then
      builtins.trace v.message true
    else
      true
  ) buildTimeRejectVerdicts;

  # REPO_SLUG is deliberately runtime-optional at the Nix layer, so this must NOT
  # throw merely because mergedDefaults.repoSlug is "" -- most Consumers never
  # set `defaults.repoSlug`, supplying it via `--repo-slug`/REPO_SLUG at dispatch
  # time, and nix/checks/equivalence.nix pins `mkRun {}` baking `"REPO_SLUG":""`
  # as a MUST-succeed case so runtime required-validation isn't masked.
  #
  # What IS eval-decidable: a Consumer flake that EXPLICITLY writes
  # `repoSlug = "";` (detected via attribute-presence on the raw `defaults`
  # argument, not the schema-defaulted mergedDefaults) while also selecting a
  # non-fully-local CODE_FORGE/ISSUE_TRACKER pairing -- a narrow foot-gun (e.g. a
  # copy-pasted template placeholder) that would otherwise bake an image dying at
  # launcher startup instead of at eval time. The genuinely-runtime-missing case
  # is caught instead by cmd/launcher/main.go's validate(); the two are
  # complementary, not overlapping.
  repoSlugCoherenceOk =
    if (defaults ? repoSlug) && defaults.repoSlug == "" && !fullyLocal then
      throw "mkHarness: repoSlug is explicitly set to an empty string, but CODE_FORGE=${mergedDefaults.codeForge}/ISSUE_TRACKER=${mergedDefaults.issueTracker} is not fully-local (CODE_FORGE=local and ISSUE_TRACKER=local) -- either supply a real repoSlug or omit the key entirely so REPO_SLUG is supplied at dispatch runtime instead"
    else
      true;

  # BOX_FORGE_AND_ISSUE_ACCESS=read-only denies the Box a write token on both
  # axes, so every write must instead be host-mediated: the selected CODE_FORGE
  # row must be relayCapable and the selected ISSUE_TRACKER row must be
  # hostPostingCapable. Mirrors cmd/launcher/main.go's
  # checkReadOnlyCapabilityGate, which re-derives the same facts at runtime via
  # Go interface assertions. read-write (the default) is a fast no-op that never
  # inspects the selected backends.
  readOnlyCapabilityOk =
    if mergedDefaults.boxForgeAndIssueAccess != "read-only" then
      true
    else if !(codeForgeRow.relayCapable or false) then
      throw "mkHarness: BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=${mergedDefaults.codeForge} does not implement bundle-relay (forge.BundleRelay) for the Box's finished branch hand-off"
    else if !(issueTrackerRow.hostPostingCapable or false) then
      throw "mkHarness: BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected ISSUE_TRACKER=${mergedDefaults.issueTracker} does not implement host-posted comments and issue-filing (forge.HostPostedCommenter / forge.HostPostedIssueFiler)"
    else
      true;

  # lib/jira-status-mapping.nix's `parse` mirrors jira.go's ParseStatusMapping,
  # so an unknown-key mapping fails the build here rather than only at Box
  # runtime. Gated on ISSUE_TRACKER=jira: a non-jira consumer's stale/typoed
  # JIRA_STATUS_MAPPING is dead config the launcher never reads, and must not
  # fail a github/forgejo/local build. `builtins.seq` forces `parse`'s result to
  # WHNF so the `assert` below actually triggers any throw.
  jiraStatusMapping = import ./jira-status-mapping.nix;
  jiraStatusMappingOk =
    if mergedDefaults.issueTracker != "jira" then
      true
    else
      builtins.seq (jiraStatusMapping.parse (mergedDefaults.jiraStatusMapping or "")) true;
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
  lib.warnIf (legacyKnobsSet != [ ]) deprecationMsg {
    inherit
      image
      spindrift
      ;

    # Outputs that checks/fixtures need but that aren't part of the versioned
    # Consumer contract (ADR 0010, scoped to `image`/`spindrift`/`packages`/
    # `apps`). The manpage/completions are also Consumer-reachable below as
    # `packages.spindrift-*`; this attrset is where checks reach them, not their
    # only surface.
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
        codeCommentsContractFile
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

      # The fully resolved roster, after dropOptedOut and reviewEffort
      # post-processing -- for nix/checks/equivalence.nix's eval-level
      # introspection only, not part of the settings/CLI surface.
      roster = finalRoster;

      # The pre-toSource lib.fileset value backing launcherCurrencySrc: a
      # comparison derivation in nix/checks/equivalence.nix needs this exact
      # value, not just the realized store path.
      inherit launcherCurrencyFileset;

      # For nix/checks/prompts.nix's eval-level introspection: proving the two
      # fragment-row lists agree on every FILER_FILE_DIRECT*-gated row against a
      # real build's computed values rather than a reimplementation of the
      # predicate, and that researchPromptContentByName's keys still cover every
      # research*-prompt.md on disk.
      inherit
        directFileFragmentRows
        readOnlyReachableFragmentRows
        researchPromptContentByName
        ;
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
    # The bwrap counterpart: one flake package standing in for the whole agent
    # closure, so freshness has a single attr to realize.
    // lib.optionalAttrs (isLinux && runtime == "bwrap") { agent-closure = agentClosure; };

    # apps.default (`nix run .`) is the sole app output: the spindrift CLI.
    apps.default = {
      type = "app";
      program = "${spindrift}/bin/spindrift";
    };
  }
