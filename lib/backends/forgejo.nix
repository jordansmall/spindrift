# The forgejo backend row: data plus the env knobs it owns (issues #5633,
# #5634). The registry (./default.nix) documents the fields and fixes this
# row's order; lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
rec {
  name = "forgejo";
  goVar = "Forgejo";
  displayName = "Forgejo";
  validAsTracker = true;
  validAsCodeForge = true;
  tokenEnvVar = "FORGEJO_TOKEN";
  boxTokenEnvVar = knobs.boxForgejoToken.env;
  doctorTokenHint = "FORGEJO_TOKEN";
  doctorSlugHint = "FORGEJO_BASE_URL";
  outboxRelayCapable = true;
  relayCapable = true;
  hostPostingCapable = true;
  labelRegistry = true;
  issuesWearLabels = true;
  goTestBuildsOn = "git";
  trackerAxisRead = "FORGEJO";
  trackerAxisWrite = "FORGEJO";
  trackerAxisFiler = "FORGEJO";
  forgeBackend = "FORGEJO";
  quickstart = {
    hosts = [ "codeberg.org" ];
    axes = [
      "tracker"
      "forge"
    ];
  };
  cliPackage = "forgejo-cli";

  knobs = {
    forgejoBaseURL = {
      env = "FORGEJO_BASE_URL";
      group = "issues";
      default = "https://codeberg.org";
      doc = "Forgejo/Gitea instance base URL, defaulting to Codeberg; used when ISSUE_TRACKER=forgejo or CODE_FORGE=forgejo";
      flakeOption = true;
      legacySettingsExempt = true;
      nixSubPath = "forgejo.baseURL";
      boxEnv = true;
    };
    forgejoToken = {
      env = "FORGEJO_TOKEN";
      secret = true;
      hostConfig = true;
      doc = "Forgejo/Gitea API token (Bearer/token scheme); required when ISSUE_TRACKER=forgejo or CODE_FORGE=forgejo";
      boxEnv = true;
    };
    boxForgejoToken = {
      env = "BOX_FORGEJO_TOKEN";
      secret = true;
      doc = "DEPRECATED, no effect, and removed in #6902: no Box receives a forge token (issues #6900 and #6901), so nothing reads it. It was the opt-in two-actor separation PAT (ADR 0016 analog) of the removed read-write Box; see docs/reference.md's retired two-actor separation note";
      boxEnv = false;
    };
  };
}
