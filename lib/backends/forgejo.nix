# The forgejo backend row: data plus the env knobs it owns (issues #5633,
# #5634). The registry (./default.nix) documents the fields and fixes this
# row's order; lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
{
  name = "forgejo";
  goVar = "Forgejo";
  displayName = "Forgejo";
  validAsTracker = true;
  validAsCodeForge = true;
  # A bare `Closes #N` in a PR closes this tracker's own issue #N (ADR 0075).
  bareRefClosesIssues = true;
  # ADR 0075: a label route narrows every list and is stamped on filing.
  routeKinds = [ "label" ];
  connectorOnly = true;
  tokenEnvVar = "FORGEJO_TOKEN";
  consumerStanza = {
    connectorKnobs = [ ];
    flakeInput = null;
    moduleImport = null;
  };
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
      doc = "Forgejo/Gitea API token (Bearer/token scheme), the one credential of a forgejo run: the host attaches it to the forgejo Connector's requests and git uses it; required when ISSUE_TRACKER=forgejo or CODE_FORGE=forgejo (needs read access to the repository, its pull requests, commit statuses and branches, and write access to open, edit, merge and comment on pull requests and to delete branches)";
      boxEnv = false;
    };
  };
}
