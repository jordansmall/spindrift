# Plain-data doc metadata for the 13 hand-declared structural knobs
# (lib/flakeModule.nix's structuralOptions) plus byNameOption, keyed by the
# same flat (legacy, == mkHarness arg) names as lib/structural-paths.nix and
# "byName". Each value is { doc; docType; docDefault; } -- the prose
# `description` string a structural knob's mkOption declares, plus the two
# machine-readable/human-friendly doc-metadata strings issue #2572 renders
# into docs/flake-options.md's structural section (lib/renderers.nix's
# renderStructuralOptionsDoc).
#
# Factored out of lib/flakeModule.nix (mirroring lib/structural-paths.nix,
# lib/labels.nix, lib/baked-skills.nix) so lib/renderers.nix -- which must
# stay evaluable with a bare `nix eval`, no `pkgs`/locked nixpkgs -- can
# import this doc data directly instead of reaching it through a full
# flake-parts eval of lib/flakeModule.nix's real NixOS options. This is the
# single place doc text/docType/docDefault are authored; lib/flakeModule.nix
# imports `.doc` from here for its mkOption `description` and carries no
# docType/docDefault of its own.
#
# `docDefault` documents the EFFECTIVE default a Consumer observes at
# runtime when a knob is left unset -- for most of these, that differs from
# the mkOption's own literal `default = null;` declaration, since a
# build-time function (lib/mkHarness.nix and friends) fills in the real
# value later (e.g. `driver`'s mkOption default is `null`, but an unset
# driver behaves as `"claude"`). `byName` is the one entry where `null` IS
# the effective default: it has no fallback-injection, so an absent byName
# genuinely stays unset.
{
  driver = {
    doc = "The agent CLI Driver (ADR 0009): a build-time choice selecting one entry from the lib/drivers/ registry, baked into the image and threaded to the launcher as DRIVER. \"claude\" (default) and \"opencode\" are the Drivers today.";
    docType = "string";
    docDefault = "`\"claude\"`";
  };

  prompt = {
    doc = "Agent prompt template baked into the image; changing it requires an image rebuild. Set SPINDRIFT_PROMPT_DIR at runtime to override without a rebuild.";
    docType = "string";
    docDefault = "bundled starter";
  };

  skills = {
    doc = "Skills baked into the image at /home/agent/.claude/skills. Each is baked as a <name>/SKILL.md directory — the only layout Claude Code discovers (a flat <name>.md is ignored). An element is a path to a skill directory, or a { name; src; } content entry (name + SKILL.md body) realized with the image's Linux pkgs (issue #597). SPINDRIFT_SKILLS_DIR at runtime mounts over the same path and takes precedence.";
    docType = "list of path/derivation/`{ name; src; }`";
    docDefault = "`[]`";
  };

  roster = {
    doc = ''
      The first-class N-agent roster (issue #264, lib/roster.nix): a list of
      `{ name; model; effort; mode; description; tools; promptFile; prompt }`
      attrsets that both Drivers render subagents from, replacing the four
      hardcoded scout/reviewer/filer/worker model knobs. An explicit
      `roster` always wins over the legacy per-agent model knobs
      (scoutModel/reviewModel/filerModel/workerModel), the same precedence
      `mkHarness.nix` applies to a raw call. Untyped (`types.attrs`
      elements, not a submodule) so the forwarded list matches the
      Consumer's input verbatim, byte-for-byte, with no default-injection.
      `effort` (issue #2242) is an optional pass-through, driver-specific
      effort/reasoning-level string: the claude Driver emits it as the
      `effort` key in the agent's `--agents` JSON entry, the opencode
      Driver as the `reasoningEffort` key in the agent-file frontmatter;
      omitted entirely when not set.
    '';
    docType = "list of subagent-entry attrs";
    docDefault = "`lib/roster.nix`'s `defaultRoster`";
  };

  runtime = {
    doc = "Runner the launcher commands drive: OCI runtimes (podman/docker/rancher, the last an alias for Rancher Desktop's nerdctl) or the daemonless bubblewrap runner (bwrap, Linux-only).";
    docType = ''`"podman"` | `"docker"` | `"rancher"` | `"bwrap"`'';
    docDefault = ''`"podman"`'';
  };

  packages = {
    doc = "Project tools baked into the image, as a function of the (Linux) pkgs.";
    docType = "`pkgs -> [pkg]`";
    docDefault = "`[]`";
  };

  prefetch = {
    doc = "Shell snippet the entrypoint runs after cloning to warm caches.";
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
      Bake nix (binary + registered store DB + sandbox-off config) into the
      box so `nix flake check` and `nix develop` work inside the container.
      Defaults to true (the nix-centric baseline); set to false for a lean,
      nix-free image.
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
      warning when enabled. OCI-runner only — the bwrap runner keeps its
      read-only store bind.
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
      name a roster entry (scout/reviewer/filer/worker) and each value is a
      closed { model?; effort?; } attrset -- any other field fails eval. Mode,
      tools, and prompt overrides stay roster-only, keeping this a shorthand.
      Unlike the deprecated scoutModel/reviewModel/filerModel/workerModel
      knobs, setting this emits no deprecation warning. Ignored when an
      explicit `roster` is supplied, same precedence the legacy per-agent
      knobs already have.
    '';
    docType = "attrset of `{ model?; effort?; }` keyed by roster entry name";
    docDefault = "`null`";
  };
}
