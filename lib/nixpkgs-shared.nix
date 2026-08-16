# One nixpkgs instantiation shared across mkHarness calls. `import nixpkgs
# { ... }` is not memoized by Nix, so every mkHarness call pays for its own
# fixed-point evaluation — spindrift's own checkset calls mkHarness ~100
# times per `nix flake check`, which made those instantiations the bulk of
# evaluation (140s of a 2m25s warm check on a 32-core host).
#
# Nix has no eval-global cache to hook into, so the sharing is structural:
# `withSharedInstances` wraps a nixpkgs flake input with one lazy
# default-toolset instantiation per known system, and mkHarness.nix consults
# that cache (`sharedInstancesOf`) before instantiating itself. Every
# mkHarness call handed the same wrapped input shares the same thunks; a
# caller handed a bare input just falls back to a per-call instantiation.
# Only the default toolset is shareable — a Consumer passing `overlays` or
# `config` gets its own instantiation, since functions have no stable
# identity to key a cache on.
#
# An earlier attempt shared via import memoization instead — `import
# (builtins.toFile ...)` on a generated file baking in `${nixpkgs}`. That
# works only when the nixpkgs source happens to be materialized in the
# store: under lazy-trees Nix (Determinate, as CI runs) flake inputs never
# are, and the baked literal path fails eval with "path
# '/nix/store/...-source' is not valid". This cache is pure attrset sharing,
# no store paths involved, so it behaves identically on every Nix
# implementation.
rec {
  # OCI images are Linux-only. Maps the Consumer's (possibly darwin) system
  # to its Linux twin for the image. Doubles as the universe of systems
  # `withSharedInstances` pre-caches: the keys are every supported host
  # system, and the values are a subset of the keys.
  linuxTwin = {
    "aarch64-darwin" = "aarch64-linux";
    "x86_64-darwin" = "x86_64-linux";
    "aarch64-linux" = "aarch64-linux";
    "x86_64-linux" = "x86_64-linux";
  };

  # The default-toolset instantiation — no Consumer overlays, no Consumer
  # config. The only place this call is spelled: the cache below and
  # mkHarness.nix's per-call fallback both come here, so the two can never
  # drift apart.
  instantiate =
    nixpkgs: forSystem:
    import nixpkgs {
      system = forSystem;
      overlays = [ ];
      config = {
        allowUnfree = true;
      };
    };

  # listToAttrs builds the per-system values as unforced thunks, so systems
  # nothing asks for cost nothing.
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

  # Total on any nixpkgs a caller may pass: a bare path, a bare flake input,
  # or a withSharedInstances-wrapped input.
  sharedInstancesOf =
    nixpkgs: if builtins.isAttrs nixpkgs then nixpkgs.spindriftSharedInstances or { } else { };
}
