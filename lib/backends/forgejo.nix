# The forgejo backend row: data plus the env knobs it owns (issues #5633,
# #5634). The registry (./default.nix) documents the fields and fixes this
# row's order; lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
{
  name = "forgejo";
  goVar = "Forgejo";
  validAsTracker = true;
  validAsCodeForge = true;
  tokenEnvVar = "FORGEJO_TOKEN";
  doctorTokenHint = "FORGEJO_TOKEN";
  doctorSlugHint = "FORGEJO_BASE_URL";
  outboxRelayCapable = true;
  relayCapable = true;
  hostPostingCapable = true;
  labelRegistry = true;
  trackerAxisRead = "FORGEJO";
  trackerAxisWrite = "FORGEJO";
  trackerAxisFiler = "FORGEJO";
  forgeBackend = "FORGEJO";

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
      doc = "opt-in two-actor separation (ADR 0016 analog): a second machine user's Forgejo PAT for the Box only — the launcher keeps using its own FORGEJO_TOKEN for merges, labels, and all host-side forge calls, while the Box receives this value as its FORGEJO_TOKEN instead; empty (default) leaves the single-token flow unchanged. Pair with a Forgejo branch-protection rule that bars this user from updating the base branch and whitelists only the launcher's user for push and merge — see docs/reference.md's Forgejo two-actor separation recipe";
      boxEnv = false;
    };
  };
}
