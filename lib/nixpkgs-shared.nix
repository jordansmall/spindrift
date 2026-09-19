# Nix does not memoize `import nixpkgs { ... }`, so without this cache every
# mkHarness call pays its own fixed-point evaluation: ~100 calls per `nix
# flake check`, 140s of a 2m25s warm check. The sharing is plain attrset
# sharing, not a memoized import of a generated file, because under lazy-trees
# Nix (Determinate, as CI runs) a flake input never lands in the store.
rec {
  # OCI images are Linux-only, hence the darwin to linux mapping. The keys are
  # also every system `withSharedInstances` pre-caches, so a value that is not
  # also a key silently misses the cache.
  linuxTwin = {
    "aarch64-darwin" = "aarch64-linux";
    "x86_64-darwin" = "x86_64-linux";
    "aarch64-linux" = "aarch64-linux";
    "x86_64-linux" = "x86_64-linux";
  };

  # The only place this call is spelled: the cache below and mkHarness.nix's
  # per-call fallback both come here, so the two cannot drift apart.
  instantiate =
    nixpkgs: forSystem:
    import nixpkgs {
      system = forSystem;
      overlays = [ ];
      config = {
        allowUnfree = true;
      };
    };

  # listToAttrs leaves the per-system values as unforced thunks, so a system
  # nothing asks for costs nothing. Only the default toolset is cached: a
  # Consumer passing `overlays` or `config` gets its own instantiation, since
  # functions have no stable identity to key a cache on.
  withSharedInstances =
    nixpkgs:
    nixpkgs
    // {
      spindriftSharedInstances = builtins.listToAttrs (
        map (s: {
          name = s;
          value = instantiate nixpkgs s;
        }) (builtins.attrNames linuxTwin)
      );
    };

  # This is total on any nixpkgs a caller may pass: a bare path, a bare flake
  # input, or a withSharedInstances-wrapped input.
  sharedInstancesOf =
    nixpkgs: if builtins.isAttrs nixpkgs then nixpkgs.spindriftSharedInstances or { } else { };
}
