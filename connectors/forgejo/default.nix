# Plain-Nix entry point of the forgejo Connector (ADR 0073): the Forgejo forge
# half (branch primitives, PR reads and PR writes over REST) and the tracker
# half (issue reads, label edits, comments, filing and closing), compiled to a
# wasip1 reactor against the SDK. `core` is handed in, so the Connector
# evaluates against whichever core the caller holds.
{
  lib.connector =
    { core }:
    let
      data = import ./data.nix { inherit core; };
    in
    {
      inherit data;
      flakeModule = core.lib.connectorModule core data;
      # `pkgs` is the caller's, so the build uses its Go toolchain.
      package = pkgs: import ./package.nix { inherit pkgs core; };
      # The Route table the row registers, for a check that exports it.
      routes = data.routes;
    };
}
