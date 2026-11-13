# The git backend row: data plus the env knobs it owns (issues #5633,
# #5635). The registry (./default.nix) documents the fields and fixes this
# row's order; lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
{
  name = "git";
  goVar = "Git";
  # ADR 0068, decision 7: core keeps this backend; read by nix/checks/go.nix.
  core = true;
  displayName = "Git";
  validAsCodeForge = true;
  inBoxGHCredentialHelper = true;
  # Bundle relay only (issue #6898): git has no pull requests, so no draft-PR
  # seam and no outboxRelayCapable.
  relayCapable = true;
  # Under read-only the Box clones from a per-Pass mirror the launcher seeds
  # from the remote with the host credential (issue #6898).
  hostSeededMirror = true;
  knobs = {
    codeForgeRemoteURL = {
      env = "CODE_FORGE_REMOTE_URL";
      group = "forge";
      flag = "remote-url";
      doc = "plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo); required when CODE_FORGE=git, unused otherwise; under read-only the Box never uses it to reach the remote, the host does (issue #6898)";
      flakeOption = true;
      nixSubPath = "remoteURL";
      boxEnv = true;
    };
  };
}
