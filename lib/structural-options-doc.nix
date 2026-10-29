# Doc metadata for the structural knobs (lib/flakeModule.nix's structuralOptions)
# plus byNameOption, keyed by the flat mkHarness arg names. lib/renderers.nix must
# stay evaluable with a bare `nix eval` and no pkgs, so it imports this plain data
# instead of evaluating flakeModule.nix's real options to render
# docs/flake-options.md (issue #2572). flakeModule.nix imports `.doc` from here.

# docDefault is the effective default a Consumer sees at runtime, which for most
# knobs differs from the mkOption's literal `default = null;` because mkHarness
# fills the real value in later. byName is the exception: null really is unset.
let
  deprecationNote = import ./driver-deprecation-note.nix;
  skillNamePattern = (import ./skills.nix).namePattern;
  quotedRuntimes = import ./quoted-runtimes.nix;
in
{
  driver = {
    doc = "${deprecationNote "perSystem.spindrift.agents.drivers.<name>"} The attribute name is the Driver (see MIGRATING.md). The agent CLI Driver (ADR 0009): a build-time choice selecting one entry from the lib/drivers/ registry, baked into the image and threaded to the launcher as DRIVER. \"claude\" (default), \"opencode\" and \"pi\" are the Drivers today.";
    docType = "string";
    docDefault = "`\"claude\"`";
  };

  prompt = {
    doc = "Agent prompt template baked into the image; changing it requires an image rebuild. Set SPINDRIFT_PROMPT_DIR at runtime to override without a rebuild.";
    docType = "string";
    docDefault = "bundled starter";
  };

  skills = {
    doc = "Skills baked into the image at /agent/skills alongside the harness-owned skills; the box program copies both into the Driver's runtime skills dir at startup. Each is baked as a <name>/SKILL.md directory — the only layout the Drivers discover (a flat <name>.md is ignored). An element is a path to a skill directory, or a { name; src; } content entry (name + SKILL.md body) realized with the image's Linux pkgs (issue #597). A content entry's name must match ${skillNamePattern} and not be \".\" or \"..\", else evaluation fails (issue #5022). SPINDRIFT_SKILLS_DIR (staged at /operator-skills) is copied over the top, so an operator skill wins a name clash without erasing the baked set (issue #2489).";
    docType = "list of path/derivation/`{ name; src; }`";
    docDefault = "`[]`";
  };

  roster = {
    doc = ''
      The first-class N-agent roster (issue #264, lib/roster.nix): a list of
      `{ name; model; effort; mode; description; tools; spawns; promptFile; prompt }`
      attrsets that every Driver renders subagents from, replacing the four
      hardcoded scout/reviewer/filer/worker model knobs. An explicit
      `roster` always wins over the legacy per-agent model knobs
      (scoutModel/reviewModel/filerModel/workerModel), the same precedence
      `mkHarness.nix` applies to a raw call. Untyped (`types.attrs`
      elements, not a submodule) so the forwarded list matches the
      Consumer's input verbatim, byte-for-byte, with no default-injection.
      `effort` (issue #2242) is an optional pass-through, driver-specific
      effort/reasoning-level string: the claude Driver emits it as the
      `effort` key in the agent's `--agents` JSON entry, the opencode
      Driver as the `reasoningEffort` key in the agent-file frontmatter,
      the pi Driver as a `:<level>` suffix on the agent file's `model` plus
      a `reasoningEffort` key for the reviewer handoff (a
      role with an effort needs a model, else the default model, and the
      level must be one of off/minimal/low/medium/high/xhigh/max, or
      evaluation fails); omitted entirely when not set.
    '';
    docType = "list of subagent-entry attrs";
    docDefault = "`lib/roster.nix`'s `defaultRoster`";
    driverValued = true;
  };

  runtime = {
    # The prose names runtimes as Consumer-facing explanation; docType and
    # docDefault are derived from the runner rows.
    doc = "Runner the launcher commands drive: OCI runtimes (podman/docker/rancher, the last an alias for Rancher Desktop's nerdctl) or the daemonless bubblewrap runner (bwrap, Linux-only).";
    docType = builtins.concatStringsSep " | " quotedRuntimes;
    docDefault = builtins.head quotedRuntimes;
  };

  packages = {
    doc = "Project tools baked into the image, as a function of the (Linux) pkgs.";
    docType = "`pkgs -> [pkg]`";
    docDefault = "`[]`";
  };

  prefetch = {
    doc = "Shell snippet `box` runs after cloning to warm caches; failures are non-fatal.";
    docType = "shell snippet";
    docDefault = ''`""`'';
  };

  extraClosures = {
    doc = ''
      Extra derivations, as a function of the (Linux) pkgs, whose closures
      are baked into the image contents and registered in the store DB
      alongside the runtime closure — so in-box nix sees them as already
      present instead of cold-substituting the world on every Box.
    '';
    docType = "`pkgs -> [pkg]`";
    docDefault = "`[]`";
  };

  nixInBox = {
    doc = ''
      Bake nix (binary + registered store DB + sandbox-off, cores=4-bounded
      config) into the box so `nix flake check` and `nix develop` work
      inside the container. Defaults to true (the nix-centric baseline);
      set to false for a lean, nix-free image. `storeWritable` requires
      it.
    '';
    docType = "bool";
    docDefault = "`true`";
  };

  nixStoreWritable = {
    doc = ''
      Self-test mode (ADR 0018): make the /nix/store directory writable by
      the agent uid in the built OCI image, so `nix flake check` can
      substitute/build new store paths inside the Box instead of hitting
      EACCES. New paths land only in the container's ephemeral
      copy-on-write layer. Defaults to false; the entrypoint prints a loud
      warning when enabled. Both runtimes support it now (ADR 0042): bwrap
      overlays an ephemeral tmpfs upper on the store at run time instead.
      Requires `infra.nix.inBox`; enabling it without in-box nix is
      rejected at evaluation.
    '';
    docType = "bool";
    docDefault = "`false`";
  };

  nixpkgs = {
    doc = "Locked nixpkgs input the image and host commands build from.";
    docType = "flake input";
    docDefault = "your `nixpkgs`";
  };

  overlays = {
    doc = "Overlays applied to the instantiated nixpkgs.";
    docType = "list";
    docDefault = "`[]`";
  };

  config = {
    doc = "nixpkgs config attrs.";
    docType = "attrs";
    docDefault = "`{ allowUnfree = true; }`";
  };

  byName = {
    doc = ''
      Name-keyed shorthand (issue #2560) for per-agent model/effort overrides
      on the default roster (lib/roster.nix's defaultRoster): each key must
      name a roster entry (scout/reviewer/filer/worker/review-axis) and each
      value is a closed { model?; effort?; } attrset -- any other field fails
      eval. Mode, tools, and prompt overrides stay roster-only, keeping this
      a shorthand.
      Unlike the deprecated scoutModel/reviewModel/filerModel/workerModel
      knobs, setting this emits no deprecation warning. Ignored when an
      explicit `roster` is supplied, same precedence the legacy per-agent
      knobs already have.
    '';
    docType = "attrset of `{ model?; effort?; }` keyed by roster entry name";
    docDefault = "`null`";
    driverValued = true;
  };

  drivers = {
    doc = ''
      Drivers built side by side (ADR 0064, issue #5052), keyed by Driver
      name (a lib/drivers/ registry name: claude, opencode, pi). Each entry
      holds only the Driver-valued settings, at the same agents-relative
      paths as their top-level spelling: models.default, models.effort,
      models.roster, models.byName, the per-agent models.* overrides, and
      bashTimeoutMs. Everything else stays shared at the top level. Setting
      this alongside agents.driver or any top-level Driver-valued setting
      fails evaluation, as does a top-level Driver-valued setting with
      several Drivers declared.
    '';
    docType = "attrset of Driver entries keyed by Driver name";
    docDefault = "`{}`";
  };

  defaultDriver = {
    doc = "The Driver behind `apps.default` and `apps.daemon`, and the one whose Harness keeps the unsuffixed image and closure names. Inferred when a single Driver is declared; required, and must name a declared Driver, when several are.";
    docType = "null or string";
    docDefault = "`null`";
  };
}
