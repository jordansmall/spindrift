# The image is built from the Consumer's locked `nixpkgs`

`mkHarness` takes the Consumer's locked `nixpkgs` **input** (plus their `system`,
`overlays`, and `config`) — not an already-instantiated `pkgs` — and
re-instantiates it for the Linux image internally. This is what makes spindrift's
"the agent's environment and your dev shell never drift apart" property *actually
true* in the consumed model: the agent image and the Consumer's dev shell come
from one pin by construction.

## Considered Options

- **spindrift pins its own `nixpkgs`** — simplest, but the agent's toolchain can
  silently diverge from the Consumer's, breaking the no-drift property in exactly
  the case the refactor exists for; rejected.

## Consequences

We take the locked *input* rather than a built `pkgs` specifically so we can map
the Consumer's (possibly darwin) `system` to its Linux twin and re-instantiate —
OCI images are Linux-only. This dissolves the macOS-builder problem's
*evaluation* half (the expression Just Works anywhere); realising the derivation
still needs a Linux builder, which is unavoidable.
