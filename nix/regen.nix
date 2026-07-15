# One-shot regenerator for every schema-generated artifact (issue #402):
# `nix run .#regen` renders templates/default/harness.env.example,
# cmd/launcher/flagtable_gen.go, docs/flake-options.md,
# cmd/launcher/internal/driver/drivernames_gen.go,
# tests/box_env_gen.bash, and the generated section of
# templates/default/flake.nix's commented-out `settings` example, from their
# respective Nix sources, and writes them into the working tree. Calls the
# exact same renderers as the nix/checks.nix drift guards (lib/renderers.nix),
# so resolving a source-edit conflict is: fix the Nix source, run this, commit.
#
# This is spindrift's own dev workflow, not consumer surface — it is not
# wired into env-schema.nix or the generated flake-options reference.
#
# One schema-derived artifact is deliberately out of scope: the man page
# (lib/mkHarness.nix manpageRoff) is rebuilt fresh from the schema on every
# `nix flake check` run; there is no committed copy to drift, so there is
# nothing to regenerate.
#
# templates/default/flake.nix's commented-out `settings` example used to be
# hand-curated (its knob order didn't follow schema declaration order) with
# its own drift check flagging missing sections/knobs to hand-add. As of
# issue #520 it is fully regen-owned and exhaustive (every flakeOption knob,
# with its doc string) between its BEGIN/END GENERATED SETTINGS EXAMPLE
# markers — a new knob needs no hand-edit here, only in lib/env-schema.nix
# and this regen run.
{ pkgs }:
let
  renderers = import ../lib/renderers.nix;
  schema = import ../lib/env-schema.nix;
  envExample = renderers.renderHarnessEnvExample schema;
  flagTable = renderers.renderFlagTableGo schema;
  flakeOptionsDoc = renderers.renderFlakeOptionsDoc schema;
  boxEnvFixture = renderers.renderSetBoxEnvFixture schema;
  templateSettingsBlock = renderers.renderTemplateSettingsBlock schema;
  driverRegistry = import ../lib/drivers/default.nix { inherit (pkgs) lib; };
  driverNamesFile = renderers.renderDriverNamesGo driverRegistry.entries;
  inherit (pkgs.lib) escapeShellArg;
in
pkgs.writeShellApplication {
  name = "regen";
  runtimeInputs = [
    pkgs.git
    pkgs.gawk
  ];
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

    # Replaces the lines strictly between (and preserving) a literal
    # begin/end marker line pair with $4, for a generated section embedded
    # in an otherwise hand-written file.
    write_between() {
      local file="$root/$1" begin="$2" end="$3" content="$4"
      awk -v begin="$begin" -v end="$end" -v content="$content" '
        $0 == begin { print; printf "%s", content; skip=1; next }
        $0 == end { skip=0 }
        skip { next }
        { print }
      ' "$file" > "$file.regen-tmp" && mv "$file.regen-tmp" "$file"
      echo "regenerated $1 (generated section)"
    }

    write templates/default/harness.env.example ${escapeShellArg envExample}
    write cmd/launcher/flagtable_gen.go ${escapeShellArg flagTable}
    write docs/flake-options.md ${escapeShellArg flakeOptionsDoc}
    write cmd/launcher/internal/driver/drivernames_gen.go ${escapeShellArg driverNamesFile}
    write tests/box_env_gen.bash ${escapeShellArg boxEnvFixture}
    write_between templates/default/flake.nix \
      ${escapeShellArg "            # BEGIN GENERATED SETTINGS EXAMPLE -- nix run .#regen -- DO NOT EDIT"} \
      ${escapeShellArg "            # END GENERATED SETTINGS EXAMPLE"} \
      ${escapeShellArg templateSettingsBlock}
  '';
}
