{
  description = "A spindrift consumer on core plus the github Connector — headless coding agents, one disposable container per issue";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
    spindrift.url = "github:jordansmall/spindrift?dir=core";
    # Unlike the batteries flake, core carries no Connector: this Consumer
    # takes only the github row. A forgejo Consumer adds the forgejo Connector
    # subflake the same way (`?dir=connectors/forgejo`) and imports its module.
    spindrift-github = {
      url = "github:jordansmall/spindrift?dir=connectors/github";
      inputs.core.follows = "spindrift";
    };
  };

  outputs =
    inputs@{
      flake-parts,
      spindrift,
      spindrift-github,
      ...
    }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];

      imports = [
        spindrift.flakeModules.default
        spindrift-github.flakeModules.default
      ];

      perSystem =
        { config, pkgs, ... }:
        {
          devShells.default = pkgs.mkShell { packages = [ config.packages.spindrift ]; };

          # Unset options under `perSystem.spindrift` keep spindrift's own
          # defaults, including its built-in prompt. Secrets are runtime env
          # (copy harness.env.example to harness.env); forge.repoSlug and the
          # other run defaults are documented in docs/flake-options.md.
          spindrift = {
            # `p` is the image's Linux nixpkgs, not your host's.
            infra.image.packages = p: [ p.go ];

            # Warm any dependency caches after the clone (runs in the work tree).
            infra.image.prefetch = "go mod download || true";
          };
        };
    };
}
