# Sample packages fed to `packages` in flake.nix; swap this whole file for
# your stack's own tooling.
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
