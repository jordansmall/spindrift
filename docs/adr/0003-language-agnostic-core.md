# The harness core is language-agnostic; Rust is only an example

spindrift no longer hardwires Rust. `rust-overlay` is dropped as an input; the
language toolchain is just one entry in the consumer's `packages` list (e.g.
`rust-bin.fromRustupToolchainFile ./rust-toolchain.toml`); and the entrypoint's
old `cargo fetch` warm-up becomes a generic, optional `prefetch` hook (default:
none). The `toolchain/` directory and the Rust prompt survive only as scaffold.

This is a deliberate **no**: nothing language-specific belongs in the core, so a
Go or Python consumer inherits no Rust machinery. Recorded so no one re-adds
`rust-overlay` as a load-bearing input later.
