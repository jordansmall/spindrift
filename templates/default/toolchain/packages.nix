# Example scaffold: spindrift's own (Rust) dogfood toolchain, fed to mkHarness
# via `packages` in flake.nix the way any Consumer would. The engine itself is
# language-agnostic (ADR 0003) — a non-Rust project swaps this whole file for
# its own node/go/python tooling.
{ pkgs }:
with pkgs;
[
  # wasm / web build (Leptos + Trunk)
  trunk
  binaryen

  # sqlx offline-metadata + migrations
  sqlx-cli

  # C toolchain for native crates that compile from source
  # (libsqlite3-sys, ring, …)
  gcc
  pkg-config
  openssl
  openssl.dev
]
