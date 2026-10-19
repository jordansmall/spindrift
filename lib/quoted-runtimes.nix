# The runner rows' runtimes as backtick-quoted doc cells, shared by the runtime
# row of docs/flake-options.md (lib/renderers.nix) and its structural doc
# metadata. A standalone file because lib/structural-options-doc.nix is iterated
# as a set of knobs, so it cannot export a non-knob attr.
map (r: "`\"${r}\"`") (import ./runners/index.nix { }).runtimes
