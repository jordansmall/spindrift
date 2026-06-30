# Project-specific tools baked into the agent container, on top of the Rust
# toolchain wired in flake.nix. EDIT this list for your target repo's build and
# test needs (swap in node/go/python tooling for a non-Rust project).
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
