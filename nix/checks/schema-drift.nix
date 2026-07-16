# Schema drift guards: every committed generated artifact (Driver name table,
# harness.env.example, launcher flag table, flake-options doc, template
# settings example, man page) must stay in sync with its schema source.
# Shares its renderers with `nix run .#regen` via lib/renderers.nix so the
# guard and the regenerator can never drift from each other (issue #402).
{ pkgs, fixtures, ... }:
let
  inherit (fixtures) harness;
  renderers = import ../../lib/renderers.nix;
in
{
  # cmd/launcher/internal/driver/drivernames_gen.go must match the key list
  # derived from lib/drivers/default.nix. Fails when a Driver is added to the
  # Nix registry but the committed generated file is not regenerated. Shares
  # its renderer with `nix run .#regen` via lib/renderers.nix (issue #436).
  driver-names-gen =
    let
      driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
      generated = pkgs.writeText "drivernames_gen.go.generated" (
        renderers.renderDriverNamesGo driverRegistry.entries
      );
    in
    pkgs.runCommand "driver-names-gen"
      {
        inherit generated;
        committed = ../../cmd/launcher/internal/driver/drivernames_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/internal/driver/drivernames_gen.go is out of sync with lib/drivers/default.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # harness.env.example must match the content generated from env-schema.nix.
  # Fails when a new schema knob is added but the committed file is not
  # regenerated (golden-file drift; resolves issue #109). Shares its renderer
  # with `nix run .#regen` (nix/regen.nix) via lib/renderers.nix — the guard
  # and the regenerator cannot drift from each other (issue #402).
  harness-env-example =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "harness.env.example.generated" (
        renderers.renderHarnessEnvExample schema
      );
    in
    pkgs.runCommand "harness-env-example"
      {
        inherit generated;
        committed = ../../templates/default/harness.env.example;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "templates/default/harness.env.example is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # Every env-var string literal in cmd/launcher/main.go must have a
  # matching entry in lib/env-schema.nix, and vice-versa (presence-only;
  # value-level pinning would be refactor-brittle). The document's artifact
  # keys (lib/preambles.nix documentArtifactKeys — derived from what
  # runArtifacts/buildArtifacts actually render into the Launcher input
  # document's `artifacts` section, ADR 0020, issue #810) are the schema for
  # what main.go may read outside lib/env-schema.nix, read via
  # getenvArtifact instead of os.Getenv/getenv.
  launcher-env-coverage =
    let
      schema = import ../../lib/env-schema.nix;
      preambles = import ../../lib/preambles.nix;
      inherit (pkgs.lib)
        attrValues
        concatStringsSep
        filter
        subtractLists
        ;
      mainGoSrc = builtins.readFile ../../cmd/launcher/main.go;
      # Document artifact keys: nix-computed plumbing main.go reads via
      # getenvArtifact, not user-facing knobs. Derived from
      # lib/preambles.nix documentArtifactKeys, not hand-maintained here.
      documentArtifacts = preambles.documentArtifactKeys;
      schemaEnvNames = map (e: e.env) (attrValues schema);
      # Schema knobs forwarded to containers via BOX_ENV_VARS only — the Go
      # binary never reads them directly, so they need no os.Getenv call.
      # Derived from each entry's boxEnvOnly field (lib/env-schema.nix) so a
      # new such knob needs no matching edit here.
      boxEnvOnly = map (e: e.env) (filter (e: e.boxEnvOnly or false) (attrValues schema));
      # Forward: every schema name (that Go reads directly) must appear as a
      # string literal in main.go.
      missingFromGo = filter (name: !pkgs.lib.hasInfix ''"${name}"'' mainGoSrc) (
        subtractLists boxEnvOnly schemaEnvNames
      );
      # Reverse: extract names from os.Getenv/getenv (1-arg) and
      # getenvArtifact (2-arg) calls in main.go.
      parts = builtins.split ''(os\.Getenv|getenv|getenvArtifact)\("([A-Z_][A-Z0-9_]*)"[,)]'' mainGoSrc;
      goEnvNames = map (m: builtins.elemAt m 1) (filter builtins.isList parts);
      extraInGo = subtractLists (schemaEnvNames ++ documentArtifacts) goEnvNames;
    in
    assert pkgs.lib.assertMsg (
      missingFromGo == [ ]
    ) "schema knobs absent from main.go: ${concatStringsSep ", " missingFromGo}";
    assert pkgs.lib.assertMsg (extraInGo == [ ])
      "main.go reads env vars absent from schema/documentArtifactKeys: ${concatStringsSep ", " extraInGo}";
    pkgs.runCommand "launcher-env-coverage" { } "touch $out";

  # lib/env-schema.nix's optional `choices` field (issue #554) must be a
  # non-empty list of strings, and a knob's `default` (if any) must be a
  # member of its own `choices` — a knob completing values it can never
  # legally hold would silently mislead a user tab-completing it. Also pins
  # the exact value set for the four knobs the issue names by name, so a
  # typo or dropped value fails here instead of silently narrowing/widening
  # what `spindrift --merge-mode <TAB>` etc. offer.
  schema-choices =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        attrValues
        assertMsg
        concatStringsSep
        filter
        ;
      withChoices = filter (e: e ? choices) (attrValues schema);
      badShape = filter (
        e: !(builtins.isList e.choices) || e.choices == [ ] || !(builtins.all builtins.isString e.choices)
      ) withChoices;
      badDefault = filter (e: (e ? default) && !(builtins.elem e.default e.choices)) withChoices;
    in
    assert assertMsg (badShape == [ ])
      "lib/env-schema.nix: choices must be a non-empty list of strings for: ${
        concatStringsSep ", " (map (e: e.env) badShape)
      }";
    assert assertMsg (badDefault == [ ])
      "lib/env-schema.nix: default is not a member of choices for: ${
        concatStringsSep ", " (map (e: e.env) badDefault)
      }";
    assert assertMsg (
      schema.mergeMode.choices or [ ] == [
        "immediate"
        "auto"
        "manual"
      ]
    ) "lib/env-schema.nix: mergeMode.choices must be [ immediate auto manual ]";
    assert assertMsg (
      schema.codeForge.choices or [ ] == [
        "github"
        "git"
      ]
    ) "lib/env-schema.nix: codeForge.choices must be [ github git ]";
    assert assertMsg (
      schema.issueTracker.choices or [ ] == [
        "github"
        "local"
        "jira"
      ]
    ) "lib/env-schema.nix: issueTracker.choices must be [ github local jira ]";
    assert assertMsg (
      schema.overlapGate.choices or [ ] == [
        "defer"
        "off"
      ]
    ) "lib/env-schema.nix: overlapGate.choices must be [ defer off ]";
    pkgs.runCommand "schema-choices" { } "touch $out";

  # tests/helper.bash's set_box_env fixture must export every boxEnv = true
  # schema knob, so the entrypoint-*.bats suites exercise the same defaults the nix
  # preamble bakes into the image at build time (issue #462). Fails when a new
  # boxEnv knob is added to the schema but the committed generated fixture is
  # not regenerated (golden-file drift, same treatment as harness-env-example
  # above). Shares its renderer with `nix run .#regen` via lib/renderers.nix
  # (issue #520).
  box-env-fixture-coverage =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "box_env_gen.bash.generated" (renderers.renderSetBoxEnvFixture schema);
    in
    pkgs.runCommand "box-env-fixture-coverage"
      {
        inherit generated;
        committed = ../../tests/box_env_gen.bash;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "tests/box_env_gen.bash is out of sync with lib/env-schema.nix — regenerate it with \`nix run .#regen\`" >&2; exit 1; }
        touch $out
      '';

  # cmd/launcher/flagtable_gen.go must match the content generated from
  # env-schema.nix by mkHarness.nix renderFlagTableGo.  Fails when a new
  # schema knob is added but the committed generated file is not regenerated.
  # Shares its renderer with `nix run .#regen` via lib/renderers.nix.
  launcher-flag-table =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "flagtable_gen.go.generated" (renderers.renderFlagTableGo schema);
    in
    pkgs.runCommand "launcher-flag-table"
      {
        inherit generated;
        committed = ../../cmd/launcher/flagtable_gen.go;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "cmd/launcher/flagtable_gen.go is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # docs/flake-options.md must match the reference generated from env-schema.nix.
  # Fails when a flakeOption knob is added or removed but the committed file is
  # not regenerated (same treatment as harness.env.example and flagtable_gen.go).
  # Shares its renderer with `nix run .#regen` via lib/renderers.nix.
  flake-options-doc =
    let
      schema = import ../../lib/env-schema.nix;
      generated = pkgs.writeText "flake-options.md.generated" (renderers.renderFlakeOptionsDoc schema);
    in
    pkgs.runCommand "flake-options-doc"
      {
        inherit generated;
        committed = ../../docs/flake-options.md;
      }
      ''
        diff "$generated" "$committed" \
          || { echo "docs/flake-options.md is out of sync with lib/env-schema.nix — regenerate it" >&2; exit 1; }
        touch $out
      '';

  # The generated settings block between templates/default/flake.nix's
  # BEGIN/END GENERATED SETTINGS EXAMPLE markers must match the content
  # rendered from env-schema.nix — every flakeOption knob, exhaustively,
  # with its doc string (issue #520). Shares its renderer with
  # `nix run .#regen` via lib/renderers.nix, so guard and regenerator cannot
  # drift from each other (issue #402).
  template-settings-block =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib) assertMsg;
      generated = renderers.renderTemplateSettingsBlock schema;
      templateSrc = builtins.readFile ../../templates/default/flake.nix;
      beginMarker = "BEGIN GENERATED SETTINGS EXAMPLE -- nix run .#regen -- DO NOT EDIT\n";
      endMarker = "            # END GENERATED SETTINGS EXAMPLE";
      afterBegin =
        let
          parts = builtins.split beginMarker templateSrc;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 2
        else
          throw "templates/default/flake.nix: BEGIN GENERATED SETTINGS EXAMPLE marker not found";
      committed =
        let
          parts = builtins.split endMarker afterBegin;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 0
        else
          throw "templates/default/flake.nix: END GENERATED SETTINGS EXAMPLE marker not found";
    in
    assert assertMsg (committed == generated) ''
      templates/default/flake.nix generated settings block is out of sync with lib/env-schema.nix — regenerate it with `nix run .#regen`
        got:  ${committed}
        want: ${generated}'';
    pkgs.runCommand "template-settings-block" { } "touch $out";

  # cmd/launcher/flags.go's groupOrder must list the same groups, in the same
  # order, as lib/renderers.nix groupOrder. Go stays hand-written (issue #105:
  # generation was rejected) — this pins the copy instead of replacing it.
  launcher-grouporder =
    let
      inherit (pkgs.lib) concatStringsSep assertMsg;
      flagsSrc = builtins.readFile ../../cmd/launcher/flags.go;
      markerParts = builtins.split "var groupOrder = " flagsSrc;
      afterMarker =
        if builtins.length markerParts >= 3 then
          builtins.elemAt markerParts 2
        else
          throw "cmd/launcher/flags.go: `var groupOrder = ` declaration not found";
      body = builtins.elemAt (builtins.split "\n}\n" afterMarker) 0 + "\n";
      entryMatches = builtins.split "\"([^\"]*)\",?\n" body;
      goGroupOrder = builtins.filter builtins.isString (
        builtins.concatMap (x: if builtins.isList x then x else [ ]) entryMatches
      );
    in
    assert assertMsg (goGroupOrder == renderers.groupOrder) ''
      cmd/launcher/flags.go groupOrder is out of sync with lib/renderers.nix groupOrder.
        got:  [${concatStringsSep ", " goGroupOrder}]
        want: [${concatStringsSep ", " renderers.groupOrder}]'';
    pkgs.runCommand "launcher-grouporder" { } "touch $out";

  # The generated man page must render (mandoc parses it) and totally cover the
  # schema: every SH section, every OPTIONS group, every non-secret flag, and
  # every secret env var. A new knob with no man-page presence fails here.
  launcher-manpage =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        replaceStrings
        unique
        ;
      # Roff renders the flag as \-\- with every hyphen escaped; match that
      # form. toKebab comes from lib/renderers.nix — the same helper the man
      # page itself is rendered through.
      roffFlag = e: "\\-\\-" + replaceStrings [ "-" ] [ "\\-" ] (renderers.toKebab e.env);
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      groups = unique (map (e: e.group) nonSecret);
      groupChecks = concatMapStrings (g: "need -F '.SS ${g}'\n") groups;
      flagChecks = concatMapStrings (e: "need -F '${roffFlag e}'\n") nonSecret;
      secretChecks = concatMapStrings (e: "need -F '${e.env}'\n") secretEntries;
    in
    pkgs.runCommand "launcher-manpage"
      {
        nativeBuildInputs = [ pkgs.mandoc ];
        man = "${harness.manpage}/share/man/man1/spindrift.1";
      }
      ''
        need() { grep -q "$@" "$man" || { echo "man page missing: $*" >&2; exit 1; }; }
        # Renders without a fatal parse error.
        mandoc -man -Tascii "$man" >/dev/null
        for s in NAME SYNOPSIS DESCRIPTION SUBCOMMANDS OPTIONS ENVIRONMENT FILES EXAMPLES; do
          grep -Eq "^\.SH \"?$s" "$man" || { echo "man page missing .SH $s" >&2; exit 1; }
        done
        ${groupChecks}
        ${flagChecks}
        ${secretChecks}
        touch $out
      '';

  # Pure-eval pin on renderZshCompletion's shape (issue #552): a schema flag,
  # its alias, and a secret file flag must each carry a `[description]` zsh
  # completion annotation sourced from the schema's `doc` string, and a
  # secret file flag's argument must complete via `_files`. Complements
  # launcher-zsh-completion below, which covers the built artifact end to
  # end; this one pins the renderer's output shape without a store build.
  renderer-zsh-completion-shape =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib) assertMsg hasInfix;
      out = renderers.renderZshCompletion schema;
    in
    assert assertMsg (hasInfix "#compdef spindrift" out)
      "renderZshCompletion must emit a #compdef spindrift header, got: ${out}";
    assert assertMsg (hasInfix "'--issue-number:${schema.issueNumber.doc}'" out)
      "renderZshCompletion must annotate --issue-number with its schema doc string, got: ${out}";
    assert assertMsg (hasInfix "'--issue:${schema.issueNumber.doc}'" out)
      "renderZshCompletion must complete the --issue alias with a description, got: ${out}";
    assert assertMsg (hasInfix "'--gh-token-file:${schema.ghToken.doc}'" out)
      "renderZshCompletion must annotate --gh-token-file with its schema doc string, got: ${out}";
    assert assertMsg (hasInfix ''case "$prev" in'' out && hasInfix "--gh-token-file" out)
      "renderZshCompletion must complete a --*-file flag's argument via a case \"$prev\" branch, got: ${out}";
    assert assertMsg (hasInfix "_files" out)
      "renderZshCompletion must complete a --*-file flag's argument via _files, got: ${out}";
    # Regression pin for issue #552's review round 1: an `_arguments -C
    # ... '*::arg:->args'` state machine with no `args)` case arm silently
    # swallows every word after the subcommand, so flags never complete
    # post-subcommand even though the flags array itself looks complete.
    # Pin the flag-prefix branch to an unconditional, reachable _describe
    # call on the flags array instead of a case-dispatched state.
    assert assertMsg (hasInfix ''if [[ "$cur" == -* ]]'' out)
      "renderZshCompletion must branch on a literal cur/prev flag-prefix check, not an _arguments state machine, got: ${out}";
    assert assertMsg (hasInfix "_describe -t options 'spindrift flag' flags" out)
      "renderZshCompletion's flag-prefix branch must _describe the flags array directly (reachable, unconditional), got: ${out}";
    assert assertMsg (!hasInfix "_arguments" out)
      "renderZshCompletion must not use _arguments' '*::state:->state' catch-all — issue #552 review found it swallows post-subcommand words with no matching case arm, got: ${out}";
    pkgs.runCommand "renderer-zsh-completion-shape" { } "touch $out";

  # The generated bash completion script must totally cover the schema and the
  # launcher's hardcoded subcommand set: every non-secret flag, the --issue
  # alias, every secret --*-file flag, and all six subcommands. A new knob or
  # subcommand with no completion presence fails here. Mirrors launcher-manpage.
  launcher-bash-completion =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        concatStringsSep
        ;
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      choicesKnobs = filter (e: e ? choices) nonSecret;
      subcommands = [
        "dispatch"
        "research"
        "preview"
        "build"
        "recover"
        "doctor"
      ];
      # Token-boundary match (quote or whitespace on both sides): a plain
      # substring grep would let e.g. `--issue` pass as "covered" merely
      # because `--issue-number` contains it as a prefix.
      flagChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}'\n") nonSecret;
      aliasChecks = concatMapStrings (e: if e ? alias then "need '--${e.alias}'\n" else "") nonSecret;
      secretChecks = concatMapStrings (e: "need '--${renderers.toKebab e.env}-file'\n") secretEntries;
      # Subcommand names are plain English words that can legitimately show
      # up in a comment (e.g. "rendered at build time"); a per-word boundary
      # check would pass even with a subcommand missing. Require the exact
      # assembled list the renderer emits for the first-word case, so a
      # dropped/renamed/reordered subcommand fails here.
      subcommandLine = concatStringsSep " " subcommands;
      # A choices-bearing knob must complete to exactly its own value list
      # (issue #554): pin the exact `compgen -W "..."` string the renderer
      # emits for that flag, not a per-word substring check, so a value
      # attached to the wrong flag (or dropped) fails here.
      choicesChecks = concatMapStrings (
        e:
        "grep -qF -- 'compgen -W \"${concatStringsSep " " e.choices}\"' \"$completion\" "
        + "|| { echo 'bash completion missing choices for --${renderers.toKebab e.env}' >&2; exit 1; }\n"
      ) choicesKnobs;
    in
    pkgs.runCommand "launcher-bash-completion"
      {
        nativeBuildInputs = [
          pkgs.bash
          pkgs.shellcheck
        ];
        completion = "${harness.bashCompletion}/share/bash-completion/completions/spindrift";
      }
      ''
        need() {
          grep -Eq -- "(^|[\"[:space:]])$1([\"[:space:]]|\$)" "$completion" \
            || { echo "bash completion missing: $1" >&2; exit 1; }
        }
        bash -n "$completion"
        shellcheck --shell=bash "$completion"
        ${flagChecks}
        ${aliasChecks}
        ${secretChecks}
        grep -qF -- '${subcommandLine}' "$completion" \
          || { echo "bash completion missing subcommand list: ${subcommandLine}" >&2; exit 1; }
        ${choicesChecks}
        touch $out
      '';

  # The generated fish completion script must totally cover the schema and the
  # launcher's hardcoded subcommand set: every non-secret flag, the --issue
  # alias, every secret --*-file flag, and all six subcommands. Mirrors
  # launcher-bash-completion above.
  launcher-fish-completion =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        ;
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      choicesKnobs = filter (e: e ? choices) nonSecret;
      subcommands = [
        "dispatch"
        "research"
        "preview"
        "build"
        "recover"
        "doctor"
      ];
      # fish's `-l LONG_OPTION` takes the flag name without its leading `--`,
      # so the needle is `-l <name>` (still boundary-checked on both sides:
      # `-l issue` must not match inside `-l issue-number`).
      flagChecks = concatMapStrings (e: "need '-l ${renderers.toKebab e.env}'\n") nonSecret;
      aliasChecks = concatMapStrings (e: if e ? alias then "need '-l ${e.alias}'\n" else "") nonSecret;
      secretChecks = concatMapStrings (e: "need '-l ${renderers.toKebab e.env}-file'\n") secretEntries;
      # Subcommands render one per line as `-a '<name>'`; that exact quoted
      # token can't appear incidentally in a comment (unlike the bare word),
      # so a plain fixed-string search is enough — no boundary check needed.
      subcommandChecks = concatMapStrings (s: "needF \"-a '${s}'\"\n") subcommands;
      # Pin the exact `-a '...'` argument list the renderer emits for each
      # choices-bearing flag (issue #554): an exact quoted token, like the
      # subcommand check above, so a value attached to the wrong flag (or
      # dropped) fails here.
      choicesChecks = concatMapStrings (
        e: "needF \"-a '${builtins.concatStringsSep " " e.choices}'\"\n"
      ) choicesKnobs;
    in
    pkgs.runCommand "launcher-fish-completion"
      {
        nativeBuildInputs = [ pkgs.fish ];
        completion = "${harness.fishCompletion}/share/fish/vendor_completions.d/spindrift.fish";
      }
      ''
        need() {
          grep -Eq -- "(^|[\"'[:space:]])$1([\"'[:space:]]|\$)" "$completion" \
            || { echo "fish completion missing: $1" >&2; exit 1; }
        }
        needF() {
          grep -qF -- "$1" "$completion" \
            || { echo "fish completion missing: $1" >&2; exit 1; }
        }
        fish -n "$completion"
        ${flagChecks}
        ${aliasChecks}
        ${secretChecks}
        ${subcommandChecks}
        ${choicesChecks}
        touch $out
      '';

  # zsh equivalent of launcher-bash-completion: every non-secret flag, the
  # --issue alias, every secret --*-file flag, and all six subcommands must
  # appear in the rendered zsh completion function. renderZshCompletion emits
  # each as a single-quoted `_describe` entry `'--flag:description'` (or
  # `'name:description'` for a subcommand), so the flag/subcommand name
  # immediately followed by `:` inside its opening quote is itself an
  # unambiguous token boundary — a substring check suffices (no --issue vs
  # --issue-number collision, since the colon only follows the exact name).
  launcher-zsh-completion =
    let
      schema = import ../../lib/env-schema.nix;
      inherit (pkgs.lib)
        filter
        attrValues
        concatMapStrings
        ;
      nonSecret = filter (e: !(e.secret or false)) (attrValues schema);
      secretEntries = filter (e: e.secret or false) (attrValues schema);
      choicesKnobs = filter (e: e ? choices) nonSecret;
      subcommands = [
        "dispatch"
        "research"
        "preview"
        "build"
        "recover"
        "doctor"
      ];
      flagChecks = concatMapStrings (e: "need \"'--${renderers.toKebab e.env}:\"\n") nonSecret;
      aliasChecks = concatMapStrings (e: if e ? alias then "need \"'--${e.alias}:\"\n" else "") nonSecret;
      secretChecks = concatMapStrings (e: "need \"'--${renderers.toKebab e.env}-file:\"\n") secretEntries;
      subcommandChecks = concatMapStrings (s: "need \"'${s}:\"\n") subcommands;
      # Pin the exact `compadd -- ...` argument list the renderer emits for
      # each choices-bearing flag (issue #554), not a per-word substring
      # check, so a value attached to the wrong flag (or dropped) fails here.
      choicesChecks = concatMapStrings (
        e: "need 'compadd -- ${builtins.concatStringsSep " " e.choices}'\n"
      ) choicesKnobs;
    in
    pkgs.runCommand "launcher-zsh-completion"
      {
        nativeBuildInputs = [ pkgs.zsh ];
        completion = "${harness.zshCompletion}/share/zsh/site-functions/_spindrift";
      }
      ''
        need() {
          grep -qF -- "$1" "$completion" \
            || { echo "zsh completion missing: $1" >&2; exit 1; }
        }
        zsh -n "$completion"
        ${flagChecks}
        ${aliasChecks}
        ${secretChecks}
        ${subcommandChecks}
        ${choicesChecks}
        touch $out
      '';
}
