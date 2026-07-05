# The harness core is language-agnostic; the sample toolchain is only an example

spindrift no longer hardwires Rust. `rust-overlay` is dropped as an input; the
language toolchain is just one entry in the consumer's `packages` list; and the
entrypoint's old `cargo fetch` warm-up becomes a generic, optional `prefetch`
hook (default: none). The starter template ships the simplest possible
illustration — a single `packages = p: [ p.go ]` straight from nixpkgs, no
overlay or extra input — precisely to show the core carries no language
machinery. A stack that needs one (e.g. `rust-overlay` for pinned Rust channels)
adds the overlay and input in the *consumer's* flake, never the core.

This is a deliberate **no**: nothing language-specific belongs in the core, so a
Go or Python consumer inherits no Rust machinery. Recorded so no one re-adds
`rust-overlay` as a load-bearing input later.
