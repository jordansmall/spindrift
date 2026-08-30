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
  # Optional shell snippet the entrypoint runs after cloning, to warm toolchain
  # caches (e.g. fetch pinned deps). Baked into the image; default is a no-op.
  prefetch ? "",
  # The agent prompt template, a Consumer-owned artifact. Baked into the image
  # at /agent/prompts (see agentFiles); changing it requires an image rebuild.
  # SPINDRIFT_PROMPT_DIR mounts an override directory at runtime for zero-rebuild
  # iteration (the Go launcher mounts it in cmd/launcher/internal/runner).
  prompt ? builtins.readFile ../templates/default/prompts/issue-prompt.md,
  # Subagent system prompts. Defaults ship with the harness; Consumers can
  # override via the `prompt` directory mechanism (SPINDRIFT_PROMPT_DIR).
  scoutPrompt ? builtins.readFile ../templates/default/prompts/scout-prompt.md,
  reviewPrompt ? builtins.readFile ../templates/default/prompts/review-prompt.md,
  # Opt-in: provisioned only when filerModel is non-empty (see agentsJsonTemplate).
  filerPrompt ? builtins.readFile ../templates/default/prompts/filer-prompt.md,
  # Provisioned by default (workerModel defaults to claude-sonnet-5, issue
  # #2054); empty only when workerModel is set to "" (see agentsJsonTemplate).
  workerPrompt ? builtins.readFile ../templates/default/prompts/worker-prompt.md,
  # The first-class N-agent roster (issue #264, lib/roster.nix), rendered by
  # the selected Driver into --agents JSON (claude) or on-disk agents/*.md
  # (opencode) below. `null` (the default) resolves to
  # `rosterLib.defaultRoster` built from the four legacy model knobs
  # (scoutModel/reviewModel/filerModel/workerModel, deprecated -- see
  # mergedDefaults/resolvedRoster below), so an existing Consumer that has
  # never heard of `roster` keeps building the exact same four agents it
  # always has. A Consumer that sets `roster` explicitly takes over agent
  # composition entirely; the legacy knobs are then ignored.
  roster ? null,
  # Name-keyed model/effort shorthand (issue #2560) forwarded straight into
  # `rosterLib.defaultRoster`'s own `byName` param below. Like the legacy
  # per-agent model knobs, it only takes effect when `roster` is null -- an
  # explicit `roster` always wins over every shorthand (see the doc comment
  # above `roster ? null,`).
  byName ? { },
  conflictResolvePrompt ? builtins.readFile ../templates/default/prompts/conflict-resolve-prompt.md,
  # Driven instead of `prompt` on a fix box (FIX_PASS>0, ADR: selfHeal/runFix
  # in cmd/launcher): the branch is already checked out, so this warm-fix
  # prompt skips scout/implement-from-scratch and goes straight to
  # check/fix/commit/push/watch-CI.
  fixPrompt ? builtins.readFile ../templates/default/prompts/fix-prompt.md,
  # Driven instead of `prompt` when DISPATCH_KIND=research (ADR 0022, issue
  # #640): the researcher explores the fresh clone and posts a verdict
  # comment instead of implementing the issue, so this prompt replaces the
  # whole issue-prompt.md flow rather than sharing its COMMS/CHECK blocks.
  researchPrompt ? builtins.readFile ../templates/default/prompts/research-prompt.md,
  # Driven instead of `researchPrompt` when the research dispatch runs in its
  # self-contained sub-mode (ADR 0022, issue #2202): no repo, no clone -- the
  # issue body/comments are the only input, so this prompt skips the EXPLORE
  # step entirely rather than sharing research-prompt.md's repo-exploration
  # prose.
  researchSelfContainedPrompt ? builtins.readFile ../templates/default/prompts/research-self-contained-prompt.md,
  # The Conditional fragment registry (issue #622, CONTEXT.md): rows of
  # (gate, fragment, var) the entrypoint's single fragment loop and its
  # `_subst` substitution allowlist are both rendered from. Not
  # Consumer-tunable like `prompt`/`scoutPrompt`/etc above (see
  # fragmentsSourceDir below); overridable here only for the bats
  # fixture-row test proving a new row needs no entrypoint edit.
  fragments ? import ./fragments.nix,
  # The directory the fragment registry's files live under, cp -r'd whole
  # into the image (see fragmentsSourceDir/fragmentRegistryPreamble below).
  # Not Consumer-tunable like `prompt`/`scoutPrompt`/etc above; overridable
  # here only so nix/checks/prompts.nix's build-time-reject-research-
  # verdict-comment-relay-* checks (issue #2250) can point buildTimeReject-
  # Verdicts' research-verdict-*-readonly.md lookup at a broken fixture
  # directory without touching the real templates tree.
  fragmentsDir ? ../templates/default/prompts/fragments,
  # Skill files baked into the image at /home/agent/.claude/skills so the
  # headless agent can invoke them without a runtime mount. Each element is
  # either a path/derivation (copied under its basename), or a
  # { name; src; } content entry (issue #597) baked under the given name by
  # re-realizing src with the image's own Linux pkgs — never a consumer host
  # derivation, which would tag the image's drvPath with the host system.
  # SPINDRIFT_SKILLS_DIR at runtime mounts over the same path and takes
  # precedence, shadowing all baked skills.
  skills ? [ ],
  # Non-secret run config baked into the `run` command as its built-in defaults;
  # a matching env var still wins at runtime, so one build can be re-pointed.
  defaults ? { },
  # Container runtime the launcher commands drive: "podman" (default), "docker",
  # or "rancher" (Rancher Desktop containerd mode; invokes nerdctl).
  runtime ? "podman",
  # The agent CLI Driver (ADR 0009): a build-time choice selecting one entry
  # from the lib/drivers/ registry, baked into the image (in-box half) and
  # threaded to the Go launcher as DRIVER (host-side half). "claude" (default)
  # and "opencode" are the Drivers today (ADR 0009, issues #261/#262).
  driver ? "claude",
  # Fallback Linux builder for when the host can't realize the Linux image itself
  # (the stock-mac case). Fully qualified so podman needs no default registry.
  # Pinned by manifest-list digest for reproducibility and supply-chain safety —
  # this container runs with the consumer tree bind-mounted read-write, so a
  # silently-updated :latest would be a code-execution vector.
  # To bump: pull the image, run `podman image inspect --format '{{.RepoDigests}}' nixos/nix`,
  # and update the digest in lib/build-constants.nix and docs/reference.md.
  nixBuilderImage ? (import ./build-constants.nix).nixBuilderImage,
  # Bake a usable nix into the box (binary + a registered store DB + a
  # single-user, sandbox-off nix.conf) so `nix flake check` and `nix develop`
  # run inside the unprivileged throwaway container. On by default — this is the
  # nix-centric baseline every box gets; set to false for a lean, nix-free image.
  nixInBox ? true,
  # Self-test mode (ADR 0018, issue #469): makes the /nix/store DIRECTORY
  # (not its existing contents, which stay root-owned and immutable) writable
  # by the agent uid in the built OCI image, so a `nix flake check` run inside
  # the Box can substitute/build new store paths instead of hitting EACCES.
  # New paths land in the container's ephemeral copy-on-write layer and die
  # with the Box — the image and any shared volumes are never mutated. Off by
  # default: this trades hermeticity for in-box feedback, so the entrypoint
  # prints a loud warning when it is enabled. Both runtimes support it now
  # (ADR 0042): OCI still bakes the writable directory into the image at
  # build time (chown, lib/image.nix); bwrap instead overlays an ephemeral
  # tmpfs upper on top of the host's real, unmodified store at run time.
  nixStoreWritable ? false,
  # Extra derivations whose closures are baked into the image contents and,
  # when nixInBox is on, registered in the store DB alongside the runtime
  # closure — so in-box nix sees them as already present instead of
  # cold-substituting the world on every Box. A function of the (Linux) pkgs,
  # like `packages`, so Consumer-supplied derivations stay correct on a
  # darwin host. A generic Consumer knob, not a spindrift special case
  # (issue #469).
  extraClosures ? (_pkgs: [ ]),
  # Short git revision injected into the binary via ldflags for `spindrift --version`.
  # Callers pass self.shortRev or self.rev; defaults to "unknown" for impure builds.
  revision ? "unknown",
}:
let
  # Single source of truth for the vendorHash values and the nix-builder
  # digest duplicated across nix build sites (issue #784 / #2523) — see
  # lib/build-constants.nix.
  buildConstants = import ./build-constants.nix;

  # Shared default-toolset instantiation and the Linux-twin system map — see
  # lib/nixpkgs-shared.nix for the memoization story.
  nixpkgsShared = import ./nixpkgs-shared.nix;

  # OCI images are Linux-only. Map the Consumer's (possibly darwin) system to
  # its Linux twin for the image.
  linuxSystem = nixpkgsShared.linuxTwin.${system};

  # Bundled into the single param preambles.runArtifacts and
  # preambles.buildArtifacts take (issue #2770 slices 1/2) — see
  # lib/preambles.nix's runArtifacts comment for the bundling rationale.
  # linuxSystem/system stay in scope too — they're used directly elsewhere in
  # this file (pkgs, hostPkgs, isLinux).
  systems = {
    host = system;
    linux = linuxSystem;
  };

  mergedConfig = {
    allowUnfree = true;
  }
  // config;

  # `import nixpkgs { ... }` is not memoized by Nix — every call site pays for
  # another fixed-point evaluation, and spindrift's own checkset calls
  # mkHarness ~100 times per `nix flake check`. For the default toolset — no
  # Consumer overlays, no Consumer config — consult the shared per-system
  # cache a `withSharedInstances`-wrapped nixpkgs input carries (spindrift's
  # flake.nix wraps its input; see lib/nixpkgs-shared.nix), falling back to a
  # per-call instantiation for a bare input. A Consumer passing `overlays` or
  # `config` gets its own instantiation as before — functions have no stable
  # identity, so those cannot be keyed on.
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

  # Subcommand registry (issue #1575): single source of truth for the
  # completion renderers' subcommand candidate lists (issue #1577) and the
  # man page SUBCOMMANDS section below.
  subcommandRegistry = import ./subcommands.nix;
  subcommands = subcommandRegistry;

  # The backend descriptor registry (issue #2521): one row per ISSUE_TRACKER/
  # CODE_FORGE backend, carrying capability bits like relayCapable/
  # hostPostingCapable (consumed by readOnlyCapabilityOk below) and
  # hostMediatedRemote/outboxRelayCapable/inBoxUnreachableTracker (consumed
  # by codeForgeRow/issueTrackerRow below, issue #2527 slice 1). Imported
  # the same way lib/env-schema.nix does (no `lib` in scope needed).
  backends = import ./backends/default.nix;

  # Section taxonomy and man-page renderer, shared with flakeModule.nix and the
  # nix/checks/schema-drift.nix guards so none of them can drift from each
  # other (issue #461).
  renderers = import ./renderers.nix;

  # Nix→bash preamble marshalling shared by the entrypoint and the Go
  # launcher wrappers below (issue #513); nix/checks/preambles.nix pins each
  # renderer's output shape.
  preambles = import ./preambles.nix;

  # Marker-delimited slicing/injection primitives (issue #512);
  # nix/checks/prompt-inject.nix pins each primitive's behavior.
  promptInject = import ./prompt-inject.nix;
  inherit (promptInject) sliceFromMarker injectSection;

  # Pure-data registry of the harness-owned shared prompt blocks (issue
  # #2245): the single source of truth for each block's id/marker/source/
  # slice-range/kinds, driving the marker constants and canonical text below
  # instead of each being a separate hand-wired literal (issue #2246 slice
  # 1). nix/checks/prompt-contract.nix pins the registry's cross-field
  # invariants and its canonical text against the real prompt sources, not
  # its row count/order/literal values (issue #2536). The outcome/comms/check
  # blocks' canonical text is now read from
  # promptContract.canonicalText (which slices issue-prompt.md itself, see
  # lib/prompt-contract.nix) rather than a local issuePromptSource re-read
  # here; only the research block below still slices its own source
  # directly, since it needs the RESEARCH_VERDICTS-rendered text, not the
  # registry's unrendered default.
  promptContract = import ./prompt-contract.nix;
  inherit (promptContract) byId;

  # The conditional prompt steps (skill preamble, FILE ISSUES, AUTO-FORMAT,
  # AUTO-LINT, CI FAILURE) live as fragment files under the prompts directory
  # rather than heredocs in agent/entrypoint.sh (issue #463): not
  # Consumer-tunable like `prompt`/`scoutPrompt`/etc above, so baked from this
  # fixed source into every image the same way, under /agent/prompts/fragments
  # -- a SPINDRIFT_PROMPT_DIR override supplies its own fragment for whichever
  # knob it enables, exactly as it already must supply filer-prompt.md.
  # (fragmentsDir above is the actual param; this is a thin alias so the rest
  # of this file's existing fragmentsSourceDir usages are unchanged.)
  fragmentsSourceDir = fragmentsDir;

  # The SPINDRIFT_OUTCOME contract (the LAND THE CHANGE / WATCH CI / OUTCOME /
  # IF BLOCKED sections) is harness-owned (issue #419): a Consumer `prompt`
  # that drops it would ship an agent that never emits the outcome line, so
  # the launcher never learns the PR and the merge/takeover silently never
  # happens. Sliced from the default prompt's own heading rather than
  # duplicated into a second file, so the injected block and the default
  # prompt's sections cannot drift apart — same source, same bytes.
  outcomeContractMarker = (byId "outcome").marker;
  outcomeContract = promptContract.canonicalText.outcome;

  injectOutcomeContract = injectSection outcomeContractMarker outcomeContract;

  # COMMS and CHECK/COMMIT are the other two blocks fix-prompt.md used to
  # hand-copy from issue-prompt.md (issue #455): sliced the same way as the
  # outcome contract above, so fix-prompt.md's default template can drop
  # them entirely and receive the byte-identical section at bake/run time
  # instead. COMMS runs from its own heading up to SCOUT (issue-prompt-only —
  # the fix prompt runs FIX in its place); CHECK/COMMIT runs from CHECK up to
  # REVIEW (also issue-prompt-only — a fix pass has no review step).
  commsMarker = (byId "comms").marker;
  commsBlock = promptContract.canonicalText.comms;
  checkMarker = (byId "check").marker;
  checkBlock = promptContract.canonicalText.check;

  injectComms = injectSection commsMarker commsBlock;
  injectCheckCommit = injectSection checkMarker checkBlock;

  # CODE COMMENTS is a fourth block fix-prompt.md shares with issue-prompt.md
  # (issue #2880): sliced the same way as COMMS/CHECK above, from its own
  # heading up to CHECK, so a fix prompt gets the same comment-discipline
  # rule an issue prompt already carries inline.
  codeCommentsMarker = (byId "code-comments").marker;
  codeCommentsBlock = promptContract.canonicalText."code-comments";

  injectCodeComments = injectSection codeCommentsMarker codeCommentsBlock;

  # fix-prompt.md's full shared-block treatment (issue #455): COMMS, then
  # CODE COMMENTS, then CHECK/COMMIT, then the outcome contract, applied in
  # that order so a fix prompt missing all four ends up with them in the
  # same order issue-prompt.md carries them — mirrors the injection order in
  # agent/entrypoint.sh so the baked and mounted-override cases agree.
  injectFixSharedBlocks =
    promptText: injectOutcomeContract (injectCheckCommit (injectCodeComments (injectComms promptText)));

  # research-prompt.md carries its own harness-owned outcome contract (issue
  # #640) rather than sharing issue-prompt.md's COMMS/CHECK/outcome-contract
  # blocks: posting the verdict comment and emitting the outcome line, sliced
  # from the default research prompt's own "# POST THE VERDICT" heading
  # through EOF (mirrors outcomeContractMarker/outcomeContract above) so the
  # injected block and the default prompt's own copy cannot drift apart.
  researchPromptSource = builtins.readFile ../templates/default/prompts/research-prompt.md;
  researchOutcomeContractMarker = (byId "research-verdict").marker;
  # The configurable verdict vocabulary (issue #2201): render the verdict
  # contract from the RESEARCH_VERDICTS knob before slicing the outcome
  # contract and baking the prompt, so both the default set and a custom set
  # flow into the baked research prompt and the contract injected into a
  # Consumer prompt lacking it, through the same rendering path (issue
  # #2525) -- there is no byte-identical-to-template no-op case.
  researchVerdicts = import ./research-verdicts.nix;
  researchVerdictsKnob = mergedDefaults.researchVerdicts or "";
  researchPromptRendered = researchVerdicts.render researchVerdictsKnob researchPrompt;
  # Same verdict-set rendering, applied to the self-contained sub-mode prompt
  # (issue #2202) so a custom RESEARCH_VERDICTS knob reaches both prompts.
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

  # The OCI image name, scoped to the selected Driver (issue #262 AC1): the
  # default claude Driver keeps the historical `spindrift` name so existing
  # tags and bats fixtures are unchanged, while any other Driver realises its
  # own `spindrift-<driver>` artifact. Threaded through image.nix (the
  # buildLayeredImage name) and preambles (the baked IMAGE_TAG) so the built
  # image, its content-hash tag, and the launcher's load/re-tag all agree.
  imageName = if driver == "claude" then "spindrift" else "spindrift-${driver}";

  # flakeOption entries are the Consumer-tunable subset.
  flakeOptionEntries = lib.filterAttrs (_: e: e.flakeOption or false) schema;

  # Built-in run defaults derived from the schema; the Consumer's `defaults` arg
  # overrides them per key, and a matching env var overrides those again at runtime.
  # Non-strict (issue #2506): flakeOptionEntries spans every flakeOption-flagged
  # schema key, most of which have no model concept at all (e.g. devShellName)
  # and so can't guarantee a `.default`, unlike the roster helper's four model
  # keys, which are expected to carry one (strict mode there throws on a miss).
  schemaDefaults = rosterSchemaDefaults.readSchemaDefaults { strict = false; } flakeOptionEntries;
  mergedDefaults = schemaDefaults // defaults;

  # lib/env-schema.nix imports the same `backends` registry (bound above)
  # to derive its codeForge/issueTracker choices lists -- resolved here to
  # the two rows the *selected* CODE_FORGE and ISSUE_TRACKER knob values
  # pick out (issue #2527 slice 1), so the capability bits below don't
  # hand-duplicate per-backend facts already declared once in the registry.
  # Falls back to `{ }` on a bogus/unregistered name (every capability bit
  # then reads `false`) rather than throwing: Go's own validate() already
  # rejects an invalid CODE_FORGE/ISSUE_TRACKER at runtime, so nix doesn't
  # need to duplicate that rejection here. readOnlyCapabilityOk below
  # reuses these same two rows for its own relayCapable/hostPostingCapable
  # checks instead of looking them up a second time.
  codeForgeRow = lib.findFirst (r: r.name == mergedDefaults.codeForge) { } backends;
  issueTrackerRow = lib.findFirst (r: r.name == mergedDefaults.issueTracker) { } backends;

  # Four capability signals derived from the resolved CODE_FORGE/
  # ISSUE_TRACKER backend rows above, threaded into the Launcher input
  # document's `run` artifacts (preambles.runArtifacts) as
  # HOST_MEDIATED_REMOTE / OUTBOX_RELAY_CAPABLE / IN_BOX_UNREACHABLE_TRACKER
  # / FULLY_LOCAL (issue #2527 slice 1); the Go side reads them via
  # docArtifact (cmd/launcher/main.go's dispatchConfig) instead of
  # re-deriving backend facts itself.
  hostMediatedRemote = codeForgeRow.hostMediatedRemote or false;
  outboxRelayCapable = codeForgeRow.outboxRelayCapable or false;
  inBoxUnreachableTracker = issueTrackerRow.inBoxUnreachableTracker or false;
  fullyLocal = hostMediatedRemote && inBoxUnreachableTracker;

  # Tracker/forge axis strings derived from the same codeForgeRow/
  # issueTrackerRow registry rows the capability signals above already
  # read, threaded into the Launcher input document's `run` artifacts
  # (preambles.runArtifacts) as TRACKER_AXIS_READ / TRACKER_AXIS_WRITE /
  # TRACKER_AXIS_FILER / FORGE_BACKEND (issue #2533; review finding: this
  # used to be its own hand-rolled if/else chain sitting three lines below
  # the registry-row reads for the other four capability signals, with no
  # drift check tying it to cmd/launcher/main.go's matching Go switch --
  # now both sides read the same lib/backends/default.nix row fields
  # (trackerAxisRead/Write/Filer, forgeBackend), eliminating the last
  # hand-rolled switch on this axis.
  trackerAxisRead = issueTrackerRow.trackerAxisRead or "GITHUB";
  trackerAxisWrite = issueTrackerRow.trackerAxisWrite or "GITHUB";
  trackerAxisFiler = issueTrackerRow.trackerAxisFiler or "GH";
  forgeBackend = codeForgeRow.forgeBackend or "GH";

  # Eval-time choices guard (issue #2519 slice 2): lib/flakeModule.nix's
  # generated Consumer options use `types.enum` for every schema knob
  # declaring `choices`, but that only protects Consumers going through the
  # flake module. A Consumer calling `mkHarness { defaults = {...}; }`
  # directly (bypassing the flake module) could otherwise set an invalid
  # choice value with no eval-time protection at all. Distinct from
  # nix/checks/schema-drift.nix's schemaChoiceIssues/assertSchemaChoicesOk,
  # which validate the *schema's own* choices shape/default/secret rules --
  # this instead validates a *runtime* value (mergedDefaults, the resolved
  # schema-default-overridden-by-Consumer-defaults value documentSettings
  # below renders into the Launcher input document's JSON) against the
  # schema's choices, at the one point every entry path (flake module or
  # direct call) funnels through.
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

  # Eval-time coherence assert for the NETWORK_MODE knob (issue #2562, slice
  # 2): network.mode and the raw per-runtime network knobs (network.podman /
  # network.bwrapUnshare) are alternative ways to say the same thing, and
  # there is no precedence rule between them -- a Consumer that sets both
  # must pick one rather than have mkHarness silently choose a winner.
  # Separately, network.mode = no-host-loopback has no bwrap rendering: since
  # issue #2666 a bwrap Box isolates its network namespace by default (via a
  # hardened pasta helper -- working egress, host loopback blocked), so
  # no-host-loopback would render byte-identical to the default "open" on
  # bwrap. It stays rejected anyway, not because it's mechanically
  # impossible (pasta demonstrably gives bwrap exactly that partial-
  # isolation posture), but because a distinct choice with no distinct
  # rendering would mislead a Consumer into thinking they get something
  # "open" doesn't already give them -- unlike the podman/docker/rancher OCI
  # adapters, which render it as a network mode that keeps the container off
  # the host network but still reachable via slirp4netns/pasta
  # port-forwarding.
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

  # Unknown defaults keys are caught at eval time — a typo like `basebranch`
  # would otherwise be silently ignored, never baked, never surfaced.
  unknownDefaultKeys = lib.filter (k: !(lib.hasAttr k flakeOptionEntries)) (lib.attrNames defaults);

  # The first-class N-agent roster (issue #264, lib/roster.nix): an explicit
  # `roster` arg always wins; otherwise it's resolved from the four legacy
  # per-agent model knobs (scoutModel/reviewModel/filerModel/workerModel,
  # deprecated -- see the lib.warnIf below) so an existing Consumer keeps
  # building the exact same four agents it always has.
  rosterLib = import ./roster.nix { inherit lib; };
  # The one schema-defaults reader (issue #2506), reused above in non-strict
  # mode for schemaDefaults; see lib/roster-schema-defaults.nix's own doc
  # comment for why it's a separate file both this and roster.nix import
  # directly, rather than roster.nix importing mkHarness.nix for it.
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
  # The #392 opt-out (rosterLib.dropOptedOut, issue #2571 review fix): drops
  # any entry whose model is the explicit "" sentinel, right after
  # normalizeRoster (which deliberately never filters) and before any
  # Driver or downstream consumer of finalRoster below ever sees the
  # roster. `keptRoster` holds the survivors -- the entries that were NOT
  # opted out.
  keptRoster = rosterLib.dropOptedOut resolvedRoster;

  # reviewEffort (issue #2512) is the one legacy knob that overrides an
  # already-resolved roster's reviewer entry regardless of roster source
  # (contrast the four model knobs above, explicit-roster-wins per the
  # doc comment above resolvedRoster) -- applied here, post-normalize, so it
  # reaches both the defaultRoster branch and a Consumer-supplied explicit
  # roster identically.
  finalRoster =
    let
      reviewEffort = mergedDefaults.reviewEffort or "";
    in
    if reviewEffort == "" then
      keptRoster
    else
      map (e: if e.name == "reviewer" then e // { effort = reviewEffort; } else e) keptRoster;

  # --agents JSON, rendered by the selected Driver (ADR 0009) from the
  # resolved roster above, so a future Driver with a different agent-config
  # shape (e.g. opencode's agents/*.md) can supply its own renderer without
  # touching mkHarness.
  agentsJsonTemplate = driverEntry.agentsJsonTemplate { roster = finalRoster; };

  # Roster/review-loop bools derived from agentsJsonTemplate/mergedDefaults,
  # threaded into the Launcher input document's `run` artifacts
  # (preambles.runArtifacts) as FILER_ENABLED / WORKER_PROVISIONED /
  # REVIEW_LOOP_INLINE / REVIEW_LOOP_ORCHESTRATOR (issue #2533); the Go side
  # reads them via docArtifact (cmd/launcher/main.go's dispatchConfig)
  # instead of re-deriving roster membership/orchestration mode itself.
  # filerEnabled/workerProvisioned key off agentsJsonTemplate's own rendered
  # output rather than finalRoster directly, reproducing exactly what the
  # pre-#2533 in-box code computed (an in-box `jq -e 'has("filer"|"worker")'`
  # reparse of the AGENTS_JSON_TEMPLATE env var, gates.go:42-47) instead of a
  # roster-only presence check: finalRoster above already had #392-opted-out
  # entries (model = "") dropped by rosterLib.dropOptedOut before
  # agentsJsonTemplate ever rendered it, so an opted-out entry never reaches
  # lib/drivers/claude.nix's agentsJsonTemplate at all, which renders "" when
  # nothing remains, while lib/drivers/opencode.nix's
  # agentsJsonTemplate always returns "" regardless of roster contents (it
  # provisions subagents via on-disk agents/*.md files instead, rendered
  # separately below as driverAgentFiles) -- a finalRoster-only check would
  # silently flip WORKER_PROVISIONED true for opencode even though opencode's
  # own --agents-equivalent mechanism never carries that key (issue #2533
  # review).
  agentsJsonAttrs = if agentsJsonTemplate == "" then { } else builtins.fromJSON agentsJsonTemplate;
  filerEnabled = agentsJsonAttrs ? filer;
  workerProvisioned = agentsJsonAttrs ? worker;
  reviewLoopInline = !mergedDefaults.orchestratorEnabled;
  reviewLoopOrchestrator = mergedDefaults.orchestratorEnabled;

  # On-disk subagent files (AC4), rendered by the selected Driver the same
  # way agentsJsonTemplate is above: a Driver with no on-disk agent-config
  # mechanism (claude.nix) returns { } here, since its subagents ride
  # agentsJsonTemplate's --agents JSON flag instead.
  driverAgentFiles = driverEntry.agentFilesTemplate { roster = finalRoster; };

  # Nix-baked name -> prompt file map (issue #264), read at runtime by
  # entrypoint.sh's generic per-agent prompt injection loop so a custom Nth
  # agent's prompt resolves the same way as the four built-in names. Every
  # `finalRoster` entry is guaranteed to carry a `promptFile` by
  # `rosterLib.normalizeRoster` above (issue #2152 slice B), which injects the
  # "<name>-prompt.md" default for any entry that omits one -- so there's no
  # fallback left to re-derive here.
  agentsPromptFilesJson = builtins.toJSON (
    lib.listToAttrs (
      map (e: {
        name = e.name;
        value = e.promptFile;
      }) finalRoster
    )
  );

  # Roster entries carrying their own prompt (a custom agent, as opposed to
  # the four built-in ones whose prompt is always baked separately below) --
  # baked into the image alongside the four fixed prompt files. A custom
  # roster entry omitting `prompt` entirely is treated the same as one
  # explicitly setting it to null (issue #264 review finding).
  customRosterPromptFiles = lib.filter (e: (e.prompt or null) != null) finalRoster;

  # The Driver's in-box half, rendered by the registry (issue #624) into
  # agent/entrypoint.sh's DRIVER_* vars and function definitions (ADR 0009),
  # shared between the image preamble and the bats harness file (issue #433)
  # so neither can drift from the other.
  driverPreamble = driverRegistry.renderPreamble driverEntry;

  # The 9 baked /agent/* path literals (contracts, registries, prompts dir)
  # and their rendered fallback-preserving preamble (issue #2531): the same
  # nix binding lib/image.nix's agentFiles cp destinations read, so a rename
  # here updates both the image's copy destination and the entrypoint's
  # baked default together.
  agentPaths = import ./agent-paths.nix;
  agentPathsPreamble = preambles.renderAgentPathsPreamble agentPaths;

  # The Conditional fragment registry (issue #622, CONTEXT.md), rendered into
  # agent/entrypoint.sh's single fragment loop input and `_subst`
  # substitution allowlist: a bash array of "gate|fragment|var" rows, plus a
  # space-separated list of every var an envsubst call must know about (each
  # row's own var, plus any extraSubstVars a fragment's body interpolates).
  # entrypoint.sh's loop and `_subst` are both generic over this data — a new
  # row needs no entrypoint edit. Shared between the image preamble and the
  # bats harness file the same way driverPreamble/driverPreambleFile are
  # shared (issue #433), so neither can drift from the other.
  fragmentRegistryRows = map (row: "${row.gate}|${row.fragment}|${row.var}") fragments;
  fragmentSubstVars = lib.concatMap (row: [ row.var ] ++ (row.extraSubstVars or [ ])) fragments;
  fragmentRegistryPreamble =
    "_FRAGMENT_ROWS=(\n"
    + lib.concatMapStrings (row: "  " + lib.escapeShellArg row + "\n") fragmentRegistryRows
    + ")\n"
    + "_FRAGMENT_SUBST_VARS=(\n"
    + lib.concatMapStrings (v: "  " + lib.escapeShellArg v + "\n") fragmentSubstVars
    + ")\n";

  # The same Conditional fragment registry, as JSON rather than a bash
  # preamble (issue #2354): baked into the image for the Go
  # `driver-exec assemble-prompt` verb's `--registry` flag (lib/image.nix), a
  # sibling of fragmentRegistryPreamble above rather than a replacement for
  # it -- the bash preamble still drives entrypoint.sh's own fragment loop
  # until a later slice flips that call site onto the verb.
  fragmentsRegistryJson = builtins.toJSON fragments;

  # lib/prompt-contract.nix's validateMarkers list, as JSON rather than a
  # bash preamble (issue #2356): baked into the image for the Go
  # `driver-exec assemble-prompt` verb's `--validate-markers-registry` flag
  # (lib/image.nix), a sibling of fragmentsRegistryJson above.
  promptContractRegistryJson = builtins.toJSON promptContract.validateMarkers;

  # lib/prompt-contract.nix's forbiddenMarkers list, as JSON rather than a
  # bash preamble (issue #2464): baked into the image for the Go
  # `driver-exec readonly-guards` verb's `--forbidden-markers-registry` flag
  # (lib/image.nix, issue #2513: assemble-prompt no longer takes this
  # flag), a sibling of promptContractRegistryJson above.
  forbiddenMarkersRegistryJson = builtins.toJSON promptContract.forbiddenMarkers;

  # Build-time reject arm (issue #2250, parent #2244): resolves both
  # validateMarkers "reject" rows against this build's own static knowledge.
  # `reviewer-verdict` is gated on whether the orchestrator is enabled
  # (mergedDefaults.orchestratorEnabled) and checked against the literal
  # reviewPrompt text this image bakes. `verdict-comment-relay` is gated on
  # whether research runs read-only (mergedDefaults.boxForgeAndIssueAccess)
  # and checked against the literal research-verdict-*-readonly.md fragment
  # this build's mergedDefaults.issueTracker statically selects -- github and
  # forgejo are the only trackers with a distinct "-readonly" fragment file
  # (lib/fragments.nix); local/jira have none, so researchReadonlyForgeSuffix
  # is null and the id is simply omitted from contentByRowId below, resolving
  # to "advise" per lib/prompt-contract.nix's own doc comment. buildTimeReject
  # Ok below is what actually forces this list's evaluation at build time.
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

  # Single spelling of "is this a FILER_FILE_DIRECT*-gated row" (issue #2595
  # review finding A): shared by readOnlyReachableFragmentRows' exclusion
  # list below and directFileFragmentRows further down, which used to spell
  # this two different ways -- three gate-name equality checks here, one
  # hasInfix substring check there -- so a future FILER_FILE_DIRECT_GITLAB
  # (or similar) gate added only to lib/fragments.nix would be picked up by
  # the hasInfix spelling but silently miss the hand-typed equality list,
  # wrongly staying inside the forbidden-marker scan it's meant to be
  # exempted from.
  isDirectFileGate = row: lib.hasInfix "FILER_FILE_DIRECT" row.gate;

  # Structural forbidden-marker check (issue #2510, parent #2498 campaign R):
  # the fragment rows the fragment-body scan actually reaches -- every
  # fragments.nix row EXCEPT the ones whose `gate` name itself already
  # proves the fragment is access-mode-aware (or independently authorized),
  # so a legitimate negation ("do NOT `git push`") in the read-only half of
  # an explicit access-mode pair is never mistaken for a leak. This is
  # unconditional -- unlike buildTimeRejectVerdicts above, it does not
  # depend on this build's own mergedDefaults/staticGates, because a
  # forbidden marker shipped in the corpus is a problem for any Consumer
  # that might configure boxAccessReadOnly, not just this particular build.
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
    # Deliberately just these three: issue #2510 scopes the shared-template
    # half of this rule to "the shared top-level templates (issue, review,
    # filer prompts)" by name. fix-prompt.md and research{,-self-contained}
    # -prompt.md are shared templates too and do carry forbiddenMarkers
    # substrings (a negation and a descriptive mention, respectively), but
    # bringing them under this scan is out of scope here.
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

  # The FILER_FILE_DIRECT*-gated fragment rows (issue #2595, ADR 0041: "Research
  # filing is host-mediated and relay-only"): the ones whose fragment tells
  # the agent to run `gh issue create`/`fj issue create`/`gh label create`
  # directly, never rendered into a research prompt by design (see
  # lib/fragments.nix's own doc comment on its research-file-issues-relay.md
  # row for why).
  directFileFragmentRows = builtins.filter isDirectFileGate fragments;

  # The research prompts actually scanned for a direct-file placeholder
  # (issue #2595 review finding B): a hand-typed name -> rendered-content map,
  # not derived from a directory listing, so a future third
  # templates/default/prompts/research*-prompt.md template would silently
  # miss this scan unless someone also adds a row here. Named so
  # nix/checks/prompts.nix can read it back through `internals` below and
  # assert its keys still cover every research*-prompt.md file actually on
  # disk, instead of re-typing this same two-name list a second time.
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

  # In-box Driver runner (issue #626): runs one Driver invocation, direct or
  # inside the Project devShell, tees the stream to a log path, and filters
  # heartbeats in-process -- absorbing the standalone spindrift-heartbeat-filter
  # binary the image used to bake alongside it, so there is one in-box Go unit,
  # not two. Built for Linux (pkgs, not hostPkgs). Goes through the Driver seam
  # (driver.New("claude").NewHeartbeatWriter, ADR 0009 / issue #620) rather than
  # a heartbeat package directly.
  #
  # INVARIANT: the agent image drvPath must not change when host-side launcher
  # code outside this binary's import closure is modified (e.g. test-only
  # launcher commits). The fileset is intentionally tight: go.mod, driver-exec,
  # internal/driver, internal/driver/claude and internal/driver/opencode (each
  # Driver's own heartbeat/transcript/classify/usage parsing), internal/usage
  # (Driver-agnostic report types), internal/logscan (claude's log-scan
  # helper), internal/outcome (the SPINDRIFT_OUTCOME grammar/log-scan, issue
  # #1808's bundle-out verb reads/writes it), internal/bundleout
  # (CODE_FORGE=local's harness-owned code-out step bundle-out wraps),
  # internal/seambundle (the bundle filename constant bundleout and the
  # launcher's local Code Forge both share), internal/outcomebackstop (issue
  # #2157's outcome-backstop verb decision), internal/retry (the shared
  # linear-backoff leaf that verb's push retry rides),
  # internal/promptassembly (issue #2349's assemble-prompt verb: the pure
  # gate computation, fragment registry loader, and prompt assembly logic
  # that mirrors agent/entrypoint.sh's phase_prompt_assembly),
  # internal/runstate (issue #2505's shared RunState type/read/write,
  # imported by outcomebackstop's readLastVerdict), internal/markergate
  # (issue #2511's marker-gate verb: the outcome/pr-intent required-marker
  # gate's nudge-prompt/resolve decision logic), and internal/readonlyguards
  # (issue #2509's readonly-guards verb: renders and installs the runtime
  # read-only guards named by the forbiddenMarkers registry, the Go
  # successor to agent/entrypoint.sh's
  # install_readonly_push_hook/install_readonly_gh_shim) only, with
  # *_test.go excluded. If a new import is added outside this closure the
  # build fails loudly (missing package) — that is the intended failure mode
  # (#474).
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
      ];
    };
    # Same go.mod/go.sum as launcherBin above, but NOT the same vendorHash:
    # `go mod vendor` prunes to packages actually imported by the source tree
    # present, and driver-exec's fileset (above) is narrower than
    # launcherBin's full cmd/launcher tree, so the two vendor differently even
    # off identical go.mod/go.sum (#784 fix pass).
    vendorHash = buildConstants.driverExecVendorHash;
    subPackages = [ "driver-exec" ];
    meta.license = lib.licenses.mit;
  };

  # In-box orchestrator (issue #1996, ADR 0007): the Go binary entrypoint.sh
  # hands the implementor pass off to when ORCHESTRATOR_ENABLED is set,
  # instead of calling driver-exec directly. Its multi-pass loop (issue
  # #1998) scans each pass's own raw stream-json log via the selected Driver's
  # own RenderTranscript strategy (internal/driver + internal/driver/claude and
  # internal/driver/opencode, which pull internal/usage) to turn it back into
  # readable text, then internal/outcome (which pulls internal/logscan) for the
  # SPINDRIFT_OUTCOME grammar -- the same import closure driverExecBin
  # already needs, for the same reason (it also calls driver.New) -- plus
  # internal/runstate (issue #2505's shared RunState type/read/write) for its
  # own state handoff between passes, plus internal/agentpaths (the
  # single-sourced baked PROMPTS_DIR default, issue #2060) for the
  # cherry-pick conflict template path.
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

  # The harness plumbing package set, agent environment, agent files,
  # passwd/group files, and the layered OCI image build itself — extracted to
  # lib/image.nix (issue #514) as a pure code move; the image derivation must
  # stay byte-identical, so every value the module needs is threaded in
  # exactly as it was computed here.
  #
  # lib/image.nix's parameters are grouped into six attrsets. The host-native
  # mirror derivations/documents further down this same file (promptDir,
  # driverPreambleFile, runArtifacts, and others) read the same fields off
  # these groups too, instead of re-deriving them from the bare local
  # values.
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
    # The research prompt baked into the image carries the verdict contract
    # rendered from the configured set (issue #2201); default knob is a no-op.
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

  # The canonical outcome contract as a host store path, so checks can diff
  # it against what a Consumer prompt lacking the contract gets injected with
  # — proof the two cannot drift apart (issue #419).
  outcomeContractFile = hostPkgs.writeText "outcome-contract.md" imageContracts.outcomeContract;

  # The COMMS and CHECK/COMMIT blocks as host store paths, for the same
  # drift-proof reason (issue #455).
  commsContractFile = hostPkgs.writeText "comms-contract.md" imageContracts.commsBlock;
  checkContractFile = hostPkgs.writeText "check-contract.md" imageContracts.checkBlock;

  # The CODE COMMENTS block as a host store path, for the same drift-proof
  # reason (issue #2880).
  codeCommentsContractFile = hostPkgs.writeText "code-comments-contract.md" imageContracts.codeCommentsBlock;

  # The research dispatch kind's own outcome contract as a host store path,
  # for the same drift-proof reason (issue #640).
  researchOutcomeContractFile = hostPkgs.writeText "research-outcome-contract.md" imageContracts.researchOutcomeContract;

  # The Driver's registry-rendered preamble (DRIVER_* vars and function
  # definitions) as a host store-path file. The bats harness prepends this
  # before exec-ing the entrypoint (issue #433) so tests exercise the exact
  # same registry-rendered bytes that mkHarness bakes into the image (issue
  # #624) — not any hand-copied duplicates or entrypoint fallback literals.
  driverPreambleFile = hostPkgs.writeText "driver-preamble.sh" imageDriver.driverPreamble;

  # The 9 baked /agent/* path literals' rendered fallback preamble as a host
  # store-path file (issue #2531, mirrors driverPreambleFile above). The bats
  # harness prepends this before exec-ing the entrypoint so tests exercise
  # the same rendered defaults that mkHarness bakes into the image, instead
  # of an entrypoint with no default for these vars at all.
  agentPathsPreambleFile = hostPkgs.writeText "agent-paths-preamble.sh" agentPathsPreamble;

  # The Conditional fragment registry as a host store-path file (issue #622,
  # mirrors driverPreambleFile above). The bats harness prepends this before
  # exec-ing the entrypoint so tests exercise the same registry-rendered loop
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

  # The baked-skills directory as a host store path (native-buildable on
  # darwin), laid out exactly as lib/image.nix bakes it: each skill is a
  # `<name>/SKILL.md` directory (Claude Code discovers skills only as
  # directories). A { name; src; } content entry (issue #597) is realized with
  # hostPkgs here — this directory is a host-only test artifact, never an input
  # to the (Linux) image itself, so it carries no host-independence requirement.
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

  # Extracts the 32-char nix store hash from a store path as PLAIN TEXT. Nix
  # store paths are always `/nix/store/<32-char-base32-hash>-<name>`, so
  # characters 11–42 (0-indexed) are the hash. Shared by imageHash and
  # launcherCurrencyHash below so the prefix-length/hash-width magic numbers
  # live in exactly one place.
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

  # The bwrap freshness dimension (issue #2667) needs ONE comparable output
  # path standing in for "the bwrap agent closure as a whole" — linkFarm
  # bundles agentFiles + agentEnv into a single derivation whose own output
  # path changes whenever either sub-closure does, without merging their
  # directory trees (which agentFiles/agentEnv aren't guaranteed not to
  # collide on).
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

  # One renderer used by both the entrypoint's Box-side preamble and (below)
  # the document's `settings` section: iterates over flakeOption schema
  # entries. renderDefaultsPreamble ({}) still backs entrypointDefaultsPreamble
  # above — Box env is launcher→Box plumbing, not an operator surface (ADR
  # 0020) — but the launcher-side `export = true` bash preamble it used to
  # also back retired with goRunDefaultsPreamble below.
  renderDefaultsPreamble =
    args: preambles.renderDefaultsPreamble (args // { inherit flakeOptionEntries mergedDefaults; });

  # The Launcher input document's `settings` section (ADR 0020): every
  # flakeOption knob's resolved value (schema default overridden by the
  # Consumer flake's settings, i.e. mergedDefaults), keyed by env var name —
  # the same value/precedence goRunDefaultsPreamble used to bake as
  # `VAR="${VAR:-<baked>}"` bash, now carried as JSON instead of env.
  documentSettings = lib.mapAttrs' (
    key: entry: lib.nameValuePair entry.env (toString mergedDefaults.${key})
  ) flakeOptionEntries;

  # The document's `run`/`build` artifacts sections (ADR 0020): the
  # nix-computed plumbing (image refs, agent files, driver name, ...) the
  # pre-#625 goRunPreamble/goBuildPreamble used to export as bash env.
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
      # nixStoreWritable is inherited straight -- it always renders the
      # Consumer's raw knob value (issue #2665); the AND-gate with
      # NixConfigFile lives in bwrap.go, not here.
      nixStoreWritable
      ;
    driverEntry = imageDriver.driverEntry;
    prefetch = imageKnobs.prefetch;
    imageName = imageKnobs.imageName;
    boxEnvVars = preambles.renderBoxEnvVarsList schema;
    # bwrap-only (issue #2664): omitted entirely (renders as "") when the
    # Consumer has nixInBox off, matching how the OCI branch never gets this
    # key at all -- the ephemeral overlay store's nix.conf is only relevant
    # when the Box actually gets in-box nix.
    nixConfigPath = if nixInBox then nixConfigFilePath else "";
    # Mirrors the nixConfigPath line above -- see buildArtifacts' own
    # nixConfigDrv call below for the same nixInBox-off empty-string default.
    nixConfigDrv = if nixInBox then nixConfigFileDrv else "";
    # Unlike nixConfigPath above, the syscall filter is a bwrap-hardening
    # concern orthogonal to nix-in-box -- it always builds and always
    # renders its real path, on or off.
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
    # See runArtifacts' nixConfigPath comment above for the nixInBox-off
    # empty-string default.
    nixConfigDrv = if nixInBox then nixConfigFileDrv else "";
    # See runArtifacts' syscallFilterPath comment above -- unconditional.
    inherit syscallFilterDrv;
  };

  # The rendered documents as host store-path JSON files. The generated
  # wrapper passes exactly one nix-computed argument, `--input <path>`,
  # instead of the per-var env exports the pre-#625 preambles emitted.
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
  # vendorHash policy:
  #   null  — stdlib-only; no go.sum / vendor dir required.
  #   "<hash>" — set once cmd/launcher/go.mod carries an external dependency
  #             (charmbracelet/bubbletea, issue #784, was the first). To
  #             recompute after a go.mod/go.sum change, run:
  #               nix build --impure --expr \
  #                 'let flake = builtins.getFlake (toString ./.); \
  #                  pkgs = import flake.inputs.nixpkgs { system = builtins.currentSystem; }; \
  #                  in pkgs.buildGoModule { pname="x"; version="0"; \
  #                  src = ./cmd/launcher; \
  #                  vendorHash = pkgs.lib.fakeHash; }'
  #             and set the recomputed hash in lib/build-constants.nix's
  #             launcherVendorHash. Commit go.sum and the updated vendorHash
  #             together. launcherCurrencyBin below vendors the same
  #             go.mod/go.sum against a narrower fileset (src =
  #             launcherCurrencyFileset, not ./cmd/launcher), so a go.mod/
  #             go.sum change also needs a second recompute of that recipe's
  #             `src` against launcherCurrencyFileset, set into
  #             launcherCurrencyVendorHash -- the two hashes are not
  #             interchangeable (#784, issue #2677). driverExecVendorHash
  #             (below) needs the same treatment, off driverExecBin's own
  #             src.
  launcherBin = hostPkgs.buildGoModule {
    pname = "spindrift-launcher";
    version = spindriftVersion;
    src = launcherSrc;
    modRoot = "cmd/launcher";
    vendorHash = buildConstants.launcherVendorHash;
    subPackages = [ "." ]; # build only the launcher; driver-exec is in-box only
    # go test ./... already runs, vendored and offline, as the
    # launcher-go-test check (nix/checks/go.nix) against the same source —
    # running it again here is redundant (issue #1142). hostPkgs.git was
    # only ever needed for that checkPhase run (issue #769); drop it too.
    doCheck = false;
    ldflags = [
      "-X main.version=${spindriftVersion}"
      "-X main.revision=${revision}"
    ];
    meta.license = lib.licenses.mit;
  };

  # A revision-independent sibling of launcherBin (issue #2677 slice 1):
  # launcherBin's ldflags bake `-X main.revision=${revision}`, which moves
  # its store path on every commit -- even docs-only ones, since `revision`
  # (flake.nix) tracks `self.shortRev`. Callers that only need to detect
  # launcher *staleness* (issue #1364) want a hash that is stable across
  # revision-only changes -- a sibling derivation over the same source with
  # the revision normalized out (ADR 0043). This binary is never invoked,
  # only its store hash is read, so its ldflags intentionally drop
  # `-X main.revision=...` entirely; `main.version` is kept for symmetry
  # with launcherBin. `spindriftVersion` (above) reads
  # .release-please-manifest.json, a file outside cmd/launcher, so this
  # hash still moves on a release-only commit -- but only once per
  # release, not once per commit like `revision` did, so it doesn't
  # reintroduce the per-commit churn this derivation exists to avoid.
  #
  # src is scoped with lib.fileset, NOT launcherSrc (unlike launcherBin)
  # -- launcherSrc's runCommand copies ../docs alongside cmd/launcher so
  # launcherBin's checkPhase can resolve a docs-relative test path (#611),
  # but doCheck is false here and pulling docs in would make a docs-only
  # commit move this derivation's hash too, defeating the point.
  #
  # The fileset below is a directory-level approximation of the launcher's
  # import graph, not the graph itself: it takes go.mod, go.sum, and every
  # non-test .go file under cmd/launcher, then subtracts the driver-exec,
  # orchestrator, and quickstart subtrees (each of those is an independent
  # `package main`, unreachable from the launcher's imports). That keeps
  # this derivation's hash from moving on a commit to those three sibling
  # trees or a _test.go file, neither of which subPackages = [ "." ] ever
  # compiles into the launcher binary -- but it is only an approximation:
  # a reviewer diffing `go list -deps .` against this fileset found 13
  # directories included here that are outside the launcher's real import
  # graph (e.g. internal/passmachine is orchestrator-only, internal/testutil
  # is test-support-only), so perturbing those still moves this
  # derivation's outPath too (issue #2677 review fix).
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
    # No modRoot here (unlike launcherBin): the fileset's `root` above is
    # already ../cmd/launcher, so the resulting src's top level IS
    # cmd/launcher's contents -- mirroring driverExecBin, which also omits
    # modRoot for the same reason. launcherBin sets modRoot = "cmd/launcher"
    # because its launcherSrc nests a copy under $out/cmd/launcher instead.
    # Same go.mod/go.sum as launcherBin, but a narrower fileset (see above)
    # -- like driverExecBin (#784), `go mod vendor` prunes to packages
    # actually present in the source tree, so the narrower fileset vendors
    # differently even off identical go.mod/go.sum, hence its own
    # buildConstants.launcherCurrencyVendorHash rather than reusing
    # launcherVendorHash.
    vendorHash = buildConstants.launcherCurrencyVendorHash;
    subPackages = [ "." ];
    doCheck = false;
    ldflags = [
      "-X main.version=${spindriftVersion}"
      # main.revision intentionally omitted -- see comment above.
    ];
    meta.license = lib.licenses.mit;
  };

  # launcherCurrencyBin's store path as PLAIN TEXT (context discarded), same
  # trick as imagePath above -- nix derivation output paths are computed from
  # the derivation's hash at eval time, so reading this does NOT force a
  # build.
  launcherCurrencyPath = builtins.unsafeDiscardStringContext (toString launcherCurrencyBin);

  # The nix store hash extracted from launcherCurrencyPath, via the same
  # storeHashOf helper imageHash above uses. Used by the freshness probe to
  # compare the loaded launcher's store hash against the one the current
  # flake would produce.
  launcherCurrencyHash = storeHashOf launcherCurrencyPath;

  # Single-verb wrapper execing `launcher build`. The `apps.build`/
  # `packages.build` flake outputs that once forwarded to this were removed
  # in issue #613; this derivation lives on, off the flake surface, only as
  # a bats/equivalence test fixture for the build-time preamble baking.
  build =
    (hostPkgs.writeShellApplication {
      name = "build";
      # sqlite3 backs `launcher build`'s bwrap+nixInBox store-DB snapshot
      # step (ADR 0042, cmd/launcher/internal/runner/bwrap.go
      # snapshotStoreDB) -- this fixture mirrors spindriftBin's real
      # runtimeInputs.
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

  # Shared shell body used by both the spindrift CLI and the `run` test
  # fixture: sources harness.env (secrets, gitignored, read from $PWD since
  # the harness is a store path with no working tree) before execing the Go
  # binary (ADR 0007). No knob or artifact env export lives here any more —
  # those flow via the --input document (ADR 0020); GIT_USER_NAME/
  # GIT_USER_EMAIL's host-git-config fallback moved in-process too
  # (cmd/launcher gitIdentityField), so the wrapper bakes nothing per-knob.
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

  # Fish completion script rendered from the schema (issue #553), same
  # build-time-only pattern as the bash completion above: no committed copy,
  # out of `nix run .#regen`, coverage-guarded by nix/checks/schema-drift.nix.
  fishCompletionScript = renderers.renderFishCompletion schema subcommandRegistry;

  fishCompletion = hostPkgs.runCommand "spindrift-fish-completion" { } ''
    install -Dm644 ${hostPkgs.writeText "spindrift.fish" fishCompletionScript} \
      "$out/share/fish/vendor_completions.d/spindrift.fish"
  '';

  # Zsh completion script rendered from the schema (issue #552), same
  # build-time-only pattern as the bash completion and man page: no
  # committed copy, out of `nix run .#regen`, coverage-guarded by
  # nix/checks/schema-drift.nix.
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
      # sqlite3 backs `launcher build`'s bwrap+nixInBox store-DB snapshot
      # step (ADR 0042, cmd/launcher/internal/runner/bwrap.go
      # snapshotStoreDB). spindriftBin is the single generic CLI package
      # every Consumer's build/run/dispatch commands run through -- which
      # commands actually need sqlite3 is a runtime decision (the Consumer's
      # nixInBox knob, read from the input document), not something this nix
      # derivation can gate per-Consumer, so it carries sqlite3
      # unconditionally. The `run` derivation below is a separate,
      # dispatch-only wrapper (always execs `launcher dispatch`) that never
      # runs `build`, so it alone can omit sqlite3.
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

  # Single-verb wrapper execing `launcher dispatch`. The `apps.run`/
  # `packages.run` flake outputs that once forwarded to this were removed
  # in issue #613; this derivation lives on, off the flake surface, only as
  # a bats/equivalence test fixture for the dispatch-time preamble baking.
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

  # Deprecation warning (issue #264): the four per-agent model knobs are
  # superseded by `roster` above. Checked against the Consumer's own
  # `defaults` arg (not mergedDefaults, which always carries every schema
  # key via schemaDefaults) so the warning fires only when the Consumer
  # actually set one of these knobs, never merely because the schema has
  # defaults for them. stderr-only (nix's builtins.trace/warnIf), so it never
  # changes a derivation's output hash -- a Consumer on the legacy knobs and
  # one on an equivalent `roster` still produce byte-identical images.
  legacyKnobsSet = lib.filter (k: defaults ? ${k}) [
    "scoutModel"
    "reviewModel"
    "filerModel"
    "workerModel"
  ];
  deprecationMsg = "spindrift: the per-agent model knobs (${lib.concatStringsSep ", " legacyKnobsSet}) are deprecated and will be removed; migrate to the `roster` option (see docs/reference.md).";

  # Forces buildTimeRejectVerdicts' evaluation (issue #2250): builtins.all
  # must evaluate every element to a bool to decide its own result, so a
  # `throw` raised while evaluating one element's "reject" branch propagates
  # through builtins.all and then through the `assert` below -- there is no
  # lazy element `assert` skips past. A "reject" verdict throws v.message
  # (an unrecoverable build failure); an "advise" verdict is a non-fatal
  # builtins.trace nudge to stderr; "ok" is silent.
  buildTimeRejectOk = builtins.all (
    v:
    if v.verdict == "reject" then
      throw v.message
    else if v.verdict == "advise" then
      builtins.trace v.message true
    else
      true
  ) buildTimeRejectVerdicts;

  # Eval-time coherence assert (issue #2527 slice 1): REPO_SLUG is
  # deliberately runtime-optional at the Nix layer (even though
  # `repository.repoSlug`/`forge.repoSlug` are live flake options today,
  # nothing requires a Consumer to set either), so this must NOT throw just
  # because mergedDefaults.repoSlug is "" -- that's the overwhelmingly common case
  # (most Consumers, including this repo's own dogfood config, never set
  # `defaults.repoSlug` at all, supplying it only via `--repo-slug`/
  # REPO_SLUG at actual dispatch time) and nix/checks/equivalence.nix's
  # flakemodule-widen-operator-knobs check pins `mkRun {}` baking
  # `"REPO_SLUG":""` as a MUST-succeed case precisely so runtime
  # required-validation isn't masked.
  #
  # What genuinely is eval-decidable: a Consumer flake that EXPLICITLY
  # writes `repoSlug = "";` (detected via attribute-presence on the raw
  # `defaults` argument, not the schema-defaulted mergedDefaults) while also
  # selecting a non-fully-local CODE_FORGE/ISSUE_TRACKER pairing -- a real,
  # if narrow, foot-gun (e.g. a copy-pasted template placeholder) that would
  # otherwise bake an image that dies at launcher startup instead of at
  # eval time (spec #2517's Problem Statement).
  #
  # This is the intentional reading of issue #2527 AC3 ("a missing repo slug
  # on a non-fully-local cell throws at eval"), not an unmet AC: "missing"
  # here means a Consumer flake that never set `repoSlug` at all -- and that
  # case is provably required to keep succeeding, by the pre-existing (main-
  # branch) nix/checks/equivalence.nix `defaultRun`/`mkRun {}` pin, which
  # asserts the resulting document bakes `"REPO_SLUG":""` rather than
  # throwing. The only "missing" that's eval-decidable at all is the
  # EXPLICIT `repoSlug = "";` case this assert actually catches; a Consumer
  # that both omits `repoSlug` in Nix AND never supplies REPO_SLUG at
  # dispatch runtime is genuinely runtime-missing, and is instead caught by
  # cmd/launcher/main.go's validate() at run time (see its REPO_SLUG check
  # around line 329) -- these two checks are deliberately complementary,
  # covering eval-time and runtime respectively, not overlapping.
  repoSlugCoherenceOk =
    if (defaults ? repoSlug) && defaults.repoSlug == "" && !fullyLocal then
      throw "mkHarness: repoSlug is explicitly set to an empty string, but CODE_FORGE=${mergedDefaults.codeForge}/ISSUE_TRACKER=${mergedDefaults.issueTracker} is not fully-local (CODE_FORGE=local and ISSUE_TRACKER=local) -- either supply a real repoSlug or omit the key entirely so REPO_SLUG is supplied at dispatch runtime instead"
    else
      true;

  # Eval-time capability-coherence assert (issue #2526, slice 2 of 3):
  # BOX_FORGE_AND_ISSUE_ACCESS=read-only denies the Box a write token on both
  # axes, so every write it would otherwise make must instead be host-
  # mediated. The selected CODE_FORGE row must be relayCapable (bundle-relay,
  # and draft-PR-create/commit-subjects when PR-shaped) and the selected
  # ISSUE_TRACKER row must be hostPostingCapable (host-posted comments and
  # issue-filing) -- lib/backends/default.nix's `relayCapable` /
  # `hostPostingCapable` bits, the static single source of truth for both
  # facts (mirrors cmd/launcher/main.go's checkReadOnlyCapabilityGate, which
  # today re-derives the same facts at runtime via live Go interface
  # assertions on the constructed forge.CodeForge/forge.IssueTracker; that
  # gate is slice 3's concern to shrink to an override-guard once this static
  # check subsumes its coherence half). read-write (the default) is a fast
  # no-op -- it never inspects the selected backends, mirroring how the Go
  # gate short-circuits on c.boxForgeAndIssueAccess != "read-only".
  readOnlyCapabilityOk =
    if mergedDefaults.boxForgeAndIssueAccess != "read-only" then
      true
    else if !(codeForgeRow.relayCapable or false) then
      throw "mkHarness: BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected CODE_FORGE=${mergedDefaults.codeForge} does not implement bundle-relay (forge.BundleRelay) for the Box's finished branch hand-off"
    else if !(issueTrackerRow.hostPostingCapable or false) then
      throw "mkHarness: BOX_FORGE_AND_ISSUE_ACCESS=read-only: the selected ISSUE_TRACKER=${mergedDefaults.issueTracker} does not implement host-posted comments and issue-filing (forge.HostPostedCommenter / forge.HostPostedIssueFiler)"
    else
      true;

  # Eval-time guard for the JIRA_STATUS_MAPPING knob (issue #2539):
  # lib/jira-status-mapping.nix's `parse` mirrors the runtime validation
  # cmd/launcher/internal/forge/jira/jira.go's ParseStatusMapping performs, so
  # an unknown-key mapping fails the build here rather than only surfacing at
  # Box runtime. Gated on ISSUE_TRACKER=jira (mirrors readOnlyCapabilityOk's
  # issueTracker-specific conditional above): backend.go only ever calls
  # ParseStatusMapping on the Jira backend's row, so a non-jira consumer's
  # stale/typoed JIRA_STATUS_MAPPING is dead config the launcher never reads,
  # and must not fail a github/forgejo/local build. `builtins.seq` forces
  # `parse`'s result to WHNF so the `assert` below actually triggers any
  # throw.
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

    # Outputs that checks/fixtures need but that aren't themselves part of
    # the versioned Consumer contract (ADR 0010, scoped to
    # `image`/`spindrift`/`packages`/`apps`) live here (issue #2529). Four of
    # these -- manpage/bashCompletion/fishCompletion/zshCompletion -- are
    # also separately Consumer-reachable below as `packages.spindrift-*`;
    # this attrset is where checks reach them from, not their only surface.
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
      driverEntry = imageDriver.driverEntry;

      # The fully resolved agent roster (issue #2512), after the #392
      # dropOptedOut step and then the reviewEffort post-processing step --
      # exposed purely for eval-level introspection
      # (nix/checks/equivalence.nix), the same reason driverEntry above is
      # exposed. Not part of the settings/CLI surface itself.
      roster = finalRoster;

      # The pre-toSource lib.fileset value backing launcherCurrencySrc,
      # exposed for nix/checks/equivalence.nix's eval-level introspection
      # (issue #2677 review fix) -- a comparison derivation there needs
      # this exact fileset value, not just the realized store path.
      inherit launcherCurrencyFileset;

      # Exposed for nix/checks/prompts.nix's eval-level introspection (issue
      # #2595 review findings A and B): directFileFragmentRows/
      # readOnlyReachableFragmentRows let a check prove the two lists agree
      # on every FILER_FILE_DIRECT*-gated row (finding A) against a real
      # mkHarness build's own computed values, rather than a reimplementation
      # of the predicate that could itself drift from this file;
      # researchPromptContentByName lets a check prove its keys still cover
      # every research*-prompt.md file on disk (finding B).
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
    # The bwrap counterpart: one flake package standing in for the whole
    # agent closure, so freshness (issue #2667) has a single attr to realize.
    // lib.optionalAttrs (isLinux && runtime == "bwrap") { agent-closure = agentClosure; };

    # apps.default (`nix run .`) is the sole app output: the spindrift CLI.
    # The `build`/`run` app-style aliases were removed (issue #613); the
    # `build`/`run` derivations themselves live on as bats/equivalence test
    # fixtures (see `internals.build`/`internals.run` above), just off the
    # flake surface.
    apps.default = {
      type = "app";
      program = "${spindrift}/bin/spindrift";
    };
  }
