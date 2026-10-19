# The git backend row: data plus the env knobs it owns (issues #5633,
# #5635). The registry (./default.nix) documents the fields and fixes this
# row's order; lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
{
  name = "git";
  goVar = "Git";
  displayName = "Git";
  validAsCodeForge = true;
  inBoxGHCredentialHelper = true;
  knobs = {
    codeForgeRemoteURL = {
      env = "CODE_FORGE_REMOTE_URL";
      group = "forge";
      flag = "remote-url";
      doc = "plain git remote URL to clone from and push to (self-hosted git, gitea, GitLab-without-MRs, a bare server repo); required when CODE_FORGE=git, unused otherwise";
      flakeOption = true;
      nixSubPath = "remoteURL";
      boxEnv = true;
    };
  };
}
