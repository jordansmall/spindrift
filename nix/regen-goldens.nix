# Update-mode regenerator for the prompt-assembly parity goldens (issue #2951).
# It runs tests/prompt-assembly-parity.bats with UPDATE_GOLDENS=1, which flips
# the assert_golden_*_or_update helpers from fail to overwrite. Separate from
# nix/regen.nix's regen verb because these goldens are the output of running
# the suite, not a pure render of a Nix value.
{ pkgs, fixtures }:
let
  # nix/checks/promptassembly.nix imports the same env wiring, so a regen run
  # writes goldens under the environment that check later verifies against.
  parity = import ./parity-env.nix { inherit pkgs fixtures; };
  inherit (pkgs.lib) escapeShellArg;
  exportEnv = pkgs.lib.concatStrings (
    pkgs.lib.mapAttrsToList (
      name: value: "export ${name}=${escapeShellArg (toString value)}\n"
    ) parity.env
  );
  # Passes each parity.env name across the `env -i` boundary below as
  # NAME="$NAME". Built with `+` concatenation, not `"$${name}"`: Nix's lexer
  # treats a literal "$$" as never starting antiquotation, even before "{", so
  # that form emits the raw characters `$${name}` instead of the resolved name.
  passthroughEnv = pkgs.lib.concatStringsSep " " (
    map (name: name + "=\"\$" + name + "\"") (builtins.attrNames parity.env)
  );
in
pkgs.writeShellApplication {
  name = "regen-goldens";
  runtimeInputs = [
    pkgs.bats
    pkgs.bash
    pkgs.git
    pkgs.gettext
    pkgs.coreutils
    pkgs.gnugrep
    pkgs.gnused
    pkgs.jq
  ];
  text = ''
    root="$(git rev-parse --show-toplevel)"
    if [ ! -f "$root/lib/env-schema.nix" ]; then
      echo "regen-goldens: $root doesn't look like the spindrift repo (no lib/env-schema.nix); refusing to write" >&2
      exit 1
    fi
    cd "$root"

    ${exportEnv}
    export UPDATE_GOLDENS=1

    # substituteInPlace is a stdenv build-time helper, unavailable in this
    # runtime script (`nix run`, not a derivation build) -- sed does the
    # same shebang rewrite here that the promptassembly-parity check does at
    # build time via substituteInPlace.
    scratch="$(mktemp -d)"
    trap 'rm -rf "$scratch"' EXIT

    # The check derivation redirects HOME to a fresh per-build scratch dir
    # before running bats (helper.bash's setup() bakes fixture skill dirs
    # under $HOME/.claude/skills, and some cells rm -rf that path) -- without
    # this, this script would run those same writes/deletes against the
    # invoking user's real home directory instead of an isolated sandbox.
    export HOME="$scratch/home"
    mkdir -p "$HOME"

    cp -r tests/fakes "$scratch/fakes"
    chmod -R +w "$scratch/fakes"
    for f in "$scratch"/fakes/*; do
      sed -i "s|^#!/usr/bin/env bash\$|#!${pkgs.bash}/bin/bash|" "$f"
    done
    export FAKES_DIR="$scratch/fakes"

    # A nix build's env is limited to what the derivation attrs declare --
    # unlike this `nix run` process, which otherwise inherits whatever the
    # invoking shell happens to export. Concretely: an agent Box invoking
    # this app is itself dispatched with BOX_FILER_ENABLED/AGENTS_JSON_TEMPLATE/
    # ORCHESTRATOR_ENABLED and friends already set for its own run, and every
    # one of those is a gate input the bats cells set (or deliberately leave
    # unset) themselves per-cell. Left inherited, they leak past a cell's own
    # setup and produce goldens that don't match what the sandboxed check
    # (which starts from a genuinely empty env) verifies -- so run bats under
    # `env -i`, passing through only PATH plus the vars this script itself
    # just set. This isolates every var but PATH from the caller's env:
    # PATH itself still carries the caller's original tail, since
    # writeShellApplication's generated wrapper appends `:$PATH` ahead of
    # this script even running -- runtimeInputs are always found first
    # regardless.
    env -i \
      PATH="$PATH" \
      HOME="$HOME" \
      TMPDIR="$scratch" \
      UPDATE_GOLDENS="$UPDATE_GOLDENS" \
      FAKES_DIR="$FAKES_DIR" \
      ${passthroughEnv} \
      bats --print-output-on-failure tests/prompt-assembly-parity.bats
  '';
}
