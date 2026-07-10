# One-shot regenerator for every schema-generated artifact (issue #402):
# `nix run .#regen` renders templates/default/harness.env.example,
# cmd/launcher/flagtable_gen.go, docs/flake-options.md, and
# cmd/launcher/internal/driver/drivernames_gen.go from their respective Nix
# sources and writes them into the working tree. Calls the exact same renderers
# as the nix/checks.nix drift guards (lib/renderers.nix), so resolving a
# source-edit conflict is: fix the Nix source, run this, commit.
#
# This is spindrift's own dev workflow, not consumer surface — it is not
# wired into env-schema.nix or the generated flake-options reference.
#
# Two schema-derived artifacts are deliberately out of scope:
#   - the man page (lib/mkHarness.nix manpageRoff) is rebuilt fresh from the
#     schema on every `nix flake check` run; there is no committed copy to
#     drift, so there is nothing to regenerate.
#   - templates/default/flake.nix's commented-out `settings` example is
#     hand-curated consumer documentation (its knob order does not follow
#     schema declaration order); nix/checks.nix's template-settings-example
#     check still catches missing sections/knobs and reports exactly what to
#     hand-add.
{ pkgs }:
let
  renderers = import ../lib/renderers.nix;
  schema = import ../lib/env-schema.nix;
  envExample = renderers.renderHarnessEnvExample schema;
  flagTable = renderers.renderFlagTableGo schema;
  flakeOptionsDoc = renderers.renderFlakeOptionsDoc schema;
  driverRegistry = import ../lib/drivers/default.nix { inherit (pkgs) lib; };
  driverNamesFile = renderers.renderDriverNamesGo driverRegistry;
  inherit (pkgs.lib) escapeShellArg;
in
pkgs.writeShellApplication {
  name = "regen";
  runtimeInputs = [ pkgs.git ];
  text = ''
    root="$(git rev-parse --show-toplevel)"
    if [ ! -f "$root/lib/env-schema.nix" ]; then
      echo "regen: $root doesn't look like the spindrift repo (no lib/env-schema.nix); refusing to write" >&2
      exit 1
    fi

    write() {
      printf '%s' "$2" > "$root/$1"
      echo "regenerated $1"
    }

    write templates/default/harness.env.example ${escapeShellArg envExample}
    write cmd/launcher/flagtable_gen.go ${escapeShellArg flagTable}
    write docs/flake-options.md ${escapeShellArg flakeOptionsDoc}
    write cmd/launcher/internal/driver/drivernames_gen.go ${escapeShellArg driverNamesFile}

    echo "note: templates/default/flake.nix's settings example is hand-curated; run 'nix flake check' (template-settings-example) to see if it needs a manual update." >&2
  '';
}
