# These checks realize the OCI image and inspect its layers, so
# nix/checks/default.nix wraps this import in optionalAttrs
# pkgs.stdenv.isLinux and darwin's `nix flake check` skips them.
{ pkgs, fixtures, ... }:
let
  inherit (fixtures)
    nonRustHarness
    customHarness
    scoutOnlyHarness
    reviewerOnlyHarness
    reviewAxisTracksReviewerHarness
    reviewAxisOptOutHarness
    filerOnlyHarness
    workerOnlyHarness
    promptHarness
    batsHarness
    skillsHarness
    noSkillsHarness
    nixStoreWritableHarness
    extraClosuresHarness
    harness
    opencodeHarness
    forgejoHarness
    ;
  agentPaths = import ../../lib/agent-paths.nix;
  preambles = import ../../lib/preambles.nix;
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  fragmentRows = import ../../lib/fragments.nix;
  fragmentBasenames = map (row: pkgs.lib.removeSuffix ".md" row.fragment) fragmentRows;
  # Read reviewModel's default from the schema so a bump only edits
  # lib/env-schema.nix (issue #2433). Deliberate carve-out from
  # readSchemaDefaults (issue #2506 AC5): this pin needs the raw schema, not
  # the helper that is itself under test elsewhere.
  reviewModelSchemaDefault = (import ../../lib/env-schema.nix).reviewModel.default;
  # Hand-typed anti-vacuity root (issue #2514) for the default-model literals
  # asserted below. lib/default-model-fixture.nix's own header says why it
  # stays hand-typed rather than schema-derived.
  defaultModelFixture = import ../../lib/default-model-fixture.nix;
  # The per-agent effort literals come from lib/roster-schema-defaults.nix
  # rather than being restated here (issue #2506). This deliberately does not
  # extend to the model literals nearby (issue #2435 AC2); see that fixture.
  rosterDefaults =
    (import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; }).rosterDefaults;
  # output-cap-env-marker's bashMaxOutputLength pin is cross-checked here
  # against lib/output-caps.nix's single source, so drift throws at eval
  # time instead of the check silently passing on a stale number (issue
  # #3679). maxMcpOutputTokens below has no single source to cross-check
  # against -- it's hand-typed only in lib/image.nix (issue #3707 tracks
  # the doc claiming otherwise).
  outputCaps = import ../../lib/output-caps.nix;
  pinnedBashMaxOutputLength =
    let
      pin = 8192;
    in
    if pin != outputCaps.bashMaxOutputLength then
      throw "output-cap-env-marker: nix/checks/image.nix's pinnedBashMaxOutputLength (${toString pin}) has drifted from lib/output-caps.nix's bashMaxOutputLength (${toString outputCaps.bashMaxOutputLength}) -- update nix/checks/image.nix's pinnedBashMaxOutputLength to match"
    else
      pin;
  pinnedMaxMcpOutputTokens = 2000;
in
{
  # The baked entrypoint must carry a store-path shebang, not the source's
  # `#!/usr/bin/env bash`, because the Box has no /usr/bin/env. Catches
  # baking the raw source instead of the writeShellApplication output.
  entrypoint-shebang = pkgs.runCommand "entrypoint-shebang" { } ''
    shebang=$(head -1 ${nonRustHarness.internals.agentFiles}/agent/entrypoint.sh)
    case "$shebang" in
      '#!'/nix/store/*bash*) : ;;
      *) echo "entrypoint shebang is not a store bash: $shebang" >&2
         exit 1 ;;
    esac
    touch $out
  '';

  # AGENTS_JSON_TEMPLATE is baked into the entrypoint by nix (ADR 0007). Each
  # subagent is composed independently by its own model knob (issue #392), so
  # the template carries whichever of scout/reviewer has a model configured,
  # and is empty only when neither does.
  agents-json-baked = pkgs.runCommand "agents-json-baked" { } ''
    # Shared shape for the dogfood/bats per-agent entry checks below: extract
    # the named agent's JSON object out of an AGENTS_JSON_TEMPLATE line,
    # assert it's present, then assert its model field matches exactly --
    # anchored on the full `"model":"<value>"` key:value pair (including the
    # closing quote) so a superstring like claude-opus-5-1 can't false-match
    # a claude-opus-5 expectation. None of these objects nest braces (tools
    # is an array), so `[^}]*` can't overrun into the next top-level key.
    assert_agent_model() {
      local line="$1" name="$2" model="$3" label="$4" mismatch_msg="$5"
      local entry
      entry=$(grep -oE "\"$name\":\{[^}]*\}" <<<"$line" || true)
      [ -n "$entry" ] \
        || { echo "$label missing $name entry in baked template" >&2; exit 1; }
      grep -q "\"model\":\"$model\"" <<<"$entry" \
        || { echo "$label $name entry $mismatch_msg" >&2; exit 1; }
    }

    ep=${customHarness.internals.agentFiles}/agent/entrypoint.sh

    # The custom harness bakes both models — template must contain them.
    grep -q 'custom-scout' "$ep" \
      || { echo "scout model not found in baked entrypoint" >&2; exit 1; }
    grep -q 'custom-reviewer' "$ep" \
      || { echo "reviewer model not found in baked entrypoint" >&2; exit 1; }
    grep -q 'AGENTS_JSON_TEMPLATE=' "$ep" \
      || { echo "AGENTS_JSON_TEMPLATE assignment missing from entrypoint" >&2; exit 1; }

    # Default harness bakes no models → template must not contain JSON content.
    ! grep -q 'AGENTS_JSON_TEMPLATE=.*{' ${nonRustHarness.internals.agentFiles}/agent/entrypoint.sh \
      || { echo "AGENTS_JSON_TEMPLATE is non-empty for no-model harness" >&2; exit 1; }

    # A scout-only harness bakes the scout entry alone — no reviewer key at all.
    scout_line=$(grep '^export AGENTS_JSON_TEMPLATE=' ${scoutOnlyHarness.internals.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-scout' <<<"$scout_line" \
      || { echo "scout-only harness missing scout model in baked template" >&2; exit 1; }
    ! grep -q '"reviewer"' <<<"$scout_line" \
      || { echo "scout-only harness unexpectedly bakes a reviewer entry" >&2; exit 1; }
    # defaultRoster's fixed default effort (issue #2386) must survive the
    # roster==null fallback (lib/mkHarness.nix:326-336) end-to-end: the
    # driver-level checks (nix/checks/drivers.nix) pin the effort table
    # lookup's render but bypass that fallback by feeding a hand-built
    # roster straight to the driver renderer, so only this fixture (no
    # `roster` arg) proves the wiring from lib/roster.nix's rosterDefaults
    # lookup through to the baked template.
    grep -q '"effort":"${rosterDefaults.scout.effort}"' <<<"$scout_line" \
      || { echo "scout-only harness missing default scout effort in baked template" >&2; exit 1; }

    # The reviewer-only mirror.
    reviewer_line=$(grep '^export AGENTS_JSON_TEMPLATE=' ${reviewerOnlyHarness.internals.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-reviewer' <<<"$reviewer_line" \
      || { echo "reviewer-only harness missing reviewer model in baked template" >&2; exit 1; }
    ! grep -q '"scout"' <<<"$reviewer_line" \
      || { echo "reviewer-only harness unexpectedly bakes a scout entry" >&2; exit 1; }
    # See the scout-only comment above: same roster==null fallback proof, for
    # reviewer's default effort.
    grep -q '"effort":"${rosterDefaults.reviewer.effort}"' <<<"$reviewer_line" \
      || { echo "reviewer-only harness missing default reviewer effort in baked template" >&2; exit 1; }

    # review-axis tracks the reviewer entry's resolved model (issue #3447,
    # ADR 0049). The eval-level roster checks (nix/checks/roster.nix) already
    # pin that resolution, but they call defaultRoster directly; only an
    # artifact-level assertion proves the tracked model survives mkHarness's
    # roster==null fallback into the baked AGENTS_JSON_TEMPLATE, which is
    # literally what the driver is handed via --agents -- i.e. the model the
    # axis subagents actually run on.
    axis_line=$(grep '^export AGENTS_JSON_TEMPLATE=' ${reviewAxisTracksReviewerHarness.internals.agentFiles}/agent/entrypoint.sh)
    assert_agent_model "$axis_line" review-axis pinned-reviewer \
      "review-axis tracking harness" "did not track the reviewer's resolved model"
    assert_agent_model "$axis_line" reviewer pinned-reviewer \
      "review-axis tracking harness" "missing the byName-configured model"
    # See the scout-only comment above: same roster==null fallback proof, for
    # review-axis's default effort.
    grep -q '"effort":"${rosterDefaults."review-axis".effort}"' <<<"$axis_line" \
      || { echo "review-axis tracking harness missing default review-axis effort in baked template" >&2; exit 1; }

    # Opting the reviewer out through that same surface drops the fan-out
    # entry with it -- no orphaned review-axis agent on a schema default.
    axis_optout_line=$(grep '^export AGENTS_JSON_TEMPLATE=' ${reviewAxisOptOutHarness.internals.agentFiles}/agent/entrypoint.sh)
    grep -q 'axis-optout-scout' <<<"$axis_optout_line" \
      || { echo "review-axis opt-out harness missing scout model in baked template" >&2; exit 1; }
    ! grep -q '"reviewer"' <<<"$axis_optout_line" \
      || { echo "review-axis opt-out harness unexpectedly bakes a reviewer entry" >&2; exit 1; }
    ! grep -q '"review-axis"' <<<"$axis_optout_line" \
      || { echo "review-axis opt-out harness unexpectedly bakes a review-axis entry" >&2; exit 1; }

    # The filer-only mirror (opt-in, default empty — issue #393): composed
    # independently like scout/reviewer, no scout/reviewer keys alongside it.
    filer_line=$(grep '^export AGENTS_JSON_TEMPLATE=' ${filerOnlyHarness.internals.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-filer' <<<"$filer_line" \
      || { echo "filer-only harness missing filer model in baked template" >&2; exit 1; }
    ! grep -q '"scout"' <<<"$filer_line" \
      || { echo "filer-only harness unexpectedly bakes a scout entry" >&2; exit 1; }
    ! grep -q '"reviewer"' <<<"$filer_line" \
      || { echo "filer-only harness unexpectedly bakes a reviewer entry" >&2; exit 1; }
    # See the scout-only comment above: same roster==null fallback proof, for
    # filer's default effort.
    grep -q '"effort":"${rosterDefaults.filer.effort}"' <<<"$filer_line" \
      || { echo "filer-only harness missing default filer effort in baked template" >&2; exit 1; }

    # The worker-only mirror (issue #2054): composed independently like
    # scout/reviewer/filer, no other agent keys alongside it.
    worker_line=$(grep '^export AGENTS_JSON_TEMPLATE=' ${workerOnlyHarness.internals.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-worker' <<<"$worker_line" \
      || { echo "worker-only harness missing worker model in baked template" >&2; exit 1; }
    ! grep -q '"scout"' <<<"$worker_line" \
      || { echo "worker-only harness unexpectedly bakes a scout entry" >&2; exit 1; }
    ! grep -q '"reviewer"' <<<"$worker_line" \
      || { echo "worker-only harness unexpectedly bakes a reviewer entry" >&2; exit 1; }
    ! grep -q '"filer"' <<<"$worker_line" \
      || { echo "worker-only harness unexpectedly bakes a filer entry" >&2; exit 1; }
    # See the scout-only comment above: same roster==null fallback proof, for
    # worker's default effort.
    grep -q '"effort":"${rosterDefaults.worker.effort}"' <<<"$worker_line" \
      || { echo "worker-only harness missing default worker effort in baked template" >&2; exit 1; }

    # The dogfood harness (issue #2435 AC3): filer is the sole explicit pin
    # (issue #616); scout, reviewer, and worker are all unmentioned in the
    # roster and must still show up in the baked template, inherited from
    # their lib/env-schema.nix schema defaults. Each model literal is matched
    # against its own agent object, not the whole template line -- none of
    # these objects nest braces (tools is an array), so `[^}]*` can't overrun
    # into the next top-level key.
    dogfood_line=$(grep '^export AGENTS_JSON_TEMPLATE=' ${harness.internals.agentFiles}/agent/entrypoint.sh)
    assert_agent_model "$dogfood_line" filer ${defaultModelFixture.dogfoodPins.filer} \
      "dogfood harness" "missing the configured model"

    assert_agent_model "$dogfood_line" scout ${defaultModelFixture.schemaDefaults.scoutModel} \
      "dogfood harness" "missing the inherited model"

    # Anchored to the fixture's literal "claude-opus-5"
    # (defaultModelFixture.schemaDefaults.reviewModel), not
    # reviewModelSchemaDefault -- same rationale as
    # nix/checks/equivalence.nix's dogfood-roster-and-review-effort reviewer
    # assertion: the code-owned review pass binds to this exact model, so the
    # guard must catch a schema-default regression away from it, not just
    # confirm the bake mirrors whatever the schema currently says.
    assert_agent_model "$dogfood_line" reviewer ${defaultModelFixture.schemaDefaults.reviewModel} \
      "dogfood harness" "missing the anchored claude-opus-5 model"

    assert_agent_model "$dogfood_line" worker ${defaultModelFixture.schemaDefaults.workerModel} \
      "dogfood harness" "missing the inherited model"

    # A Consumer that sets no model knobs and passes no roster (bats harness:
    # no `defaults`, no `roster`) must still get a reviewer on the schema
    # default (issue #2433) via the roster==null fallback
    # (lib/mkHarness.nix:317-327) -- reviewModel's default moved to
    # ${reviewModelSchemaDefault} so every Consumer's reviewer runs on the
    # strongest available model without configuring anything.
    bats_line=$(grep '^export AGENTS_JSON_TEMPLATE=' ${batsHarness.internals.agentFiles}/agent/entrypoint.sh)
    assert_agent_model "$bats_line" reviewer '${reviewModelSchemaDefault}' \
      "bats harness" "missing the default ${reviewModelSchemaDefault} model"

    touch $out
  '';

  # opencode has no --agents JSON flag; it discovers subagents from
  # HOME-relative agents/*.md files instead (issue #262 slice 5, AC4), so the
  # agentFiles layer must carry scout.md/reviewer.md/worker.md with baked
  # mode/model frontmatter and must omit filer.md when filerModel is empty.
  # This realizes only agentFiles, not the image, so it stays cheap.
  opencode-agent-files = pkgs.runCommand "opencode-agent-files" { } ''
    scout=${opencodeHarness.internals.agentFiles}/home/agent/.config/opencode/agents/scout.md
    [ -f "$scout" ] || {
      echo "opencode agentFiles missing scout.md" >&2
      exit 1
    }
    grep -q 'mode: "subagent"' "$scout" \
      || { echo "opencode scout.md missing mode: \"subagent\" (JSON-encoded, issue #2152 slice C)" >&2; exit 1; }
    grep -q 'model: "anthropic/claude-x"' "$scout" \
      || { echo "opencode scout.md missing its configured model (JSON-encoded, issue #2152 slice C)" >&2; exit 1; }
    # Mirrors agents-json-baked's new effort assertions (slice 1, same file):
    # proves the roster==null fallback (lib/mkHarness.nix:317-327) bakes the
    # new per-agent effort defaults correctly for the on-disk opencode
    # agent-files mechanism too, not just the --agents JSON one, since
    # nix/checks/drivers.nix's driver-level effort checks bypass that
    # fallback by feeding a hand-built roster straight to the renderer.
    grep -q 'reasoningEffort: "${rosterDefaults.scout.effort}"' "$scout" \
      || { echo "opencode scout.md missing default scout effort in baked frontmatter" >&2; exit 1; }

    reviewer=${opencodeHarness.internals.agentFiles}/home/agent/.config/opencode/agents/reviewer.md
    [ -f "$reviewer" ] || {
      echo "opencode agentFiles missing reviewer.md" >&2
      exit 1
    }
    grep -q 'model: "anthropic/claude-y"' "$reviewer" \
      || { echo "opencode reviewer.md missing its configured model (JSON-encoded, issue #2152 slice C)" >&2; exit 1; }
    # See the scout comment above: same roster==null fallback proof, for
    # reviewer's default effort.
    grep -q 'reasoningEffort: "${rosterDefaults.reviewer.effort}"' "$reviewer" \
      || { echo "opencode reviewer.md missing default reviewer effort in baked frontmatter" >&2; exit 1; }

    worker=${opencodeHarness.internals.agentFiles}/home/agent/.config/opencode/agents/worker.md
    [ -f "$worker" ] || {
      echo "opencode agentFiles missing worker.md" >&2
      exit 1
    }
    grep -q 'model: "anthropic/claude-z"' "$worker" \
      || { echo "opencode worker.md missing its configured model (JSON-encoded, issue #2152 slice C)" >&2; exit 1; }
    # See the scout comment above: same roster==null fallback proof, for
    # worker's default effort.
    grep -q 'reasoningEffort: "${rosterDefaults.worker.effort}"' "$worker" \
      || { echo "opencode worker.md missing default worker effort in baked frontmatter" >&2; exit 1; }

    # The on-disk mirror of agents-json-baked's review-axis assertions: the
    # tracked model must reach the baked frontmatter too, since opencode
    # discovers its subagents from these files rather than from --agents.
    axis=${opencodeHarness.internals.agentFiles}/home/agent/.config/opencode/agents/review-axis.md
    [ -f "$axis" ] || {
      echo "opencode agentFiles missing review-axis.md" >&2
      exit 1
    }
    grep -q 'model: "anthropic/claude-y"' "$axis" \
      || { echo "opencode review-axis.md did not track the reviewer's model" >&2; exit 1; }
    grep -q 'reasoningEffort: "${rosterDefaults."review-axis".effort}"' "$axis" \
      || { echo "opencode review-axis.md missing default review-axis effort in baked frontmatter" >&2; exit 1; }

    filer=${opencodeHarness.internals.agentFiles}/home/agent/.config/opencode/agents/filer.md
    [ ! -e "$filer" ] || {
      echo "opencode agentFiles unexpectedly bakes filer.md (filerModel is empty)" >&2
      exit 1
    }

    touch $out
  '';

  # Regression check for issue #2843: opencode dispatch failed "permission
  # denied" because home/agent's files, though chowned to uid 1000 by
  # fakeRootCommands, kept the read-only mode bits `cp` preserved from the
  # store. It resolves through the symlinks contents builds, so the assertion
  # holds whichever layer physically carries scout.md's bytes.
  opencode-agent-files-writable-in-image =
    let
      bakedPath = "home/agent/.config/opencode/agents/scout.md";
    in
    pkgs.runCommand "opencode-agent-files-writable-in-image" { nativeBuildInputs = [ pkgs.jq ]; } ''
      mkdir img && tar -xf ${opencodeHarness.image} -C img
      layer="$(jq -r '.[0].Layers[-1]' img/manifest.json)"
      line="$(tar -tvf "img/$layer" | grep -F '${bakedPath}' | head -1 || true)"
      [ -n "$line" ] || {
        echo "${bakedPath} not found in the image's top (customisation) layer" >&2
        exit 1
      }

      case "$line" in
        *' -> '*)
          target="''${line##* -> }"
          relTarget="''${target#/}"
          mode=""
          for l in $(jq -r '.[0].Layers[]' img/manifest.json); do
            found_line="$(tar -tvf "img/$l" | grep -F "$relTarget" | head -1 || true)"
            if [ -n "$found_line" ]; then
              mode="$(awk '{ print $1 }' <<<"$found_line")"
              break
            fi
          done
          [ -n "$mode" ] || {
            echo "${bakedPath} symlinks to $target, but that target is not in any image layer" >&2
            exit 1
          }
          ;;
        *)
          mode="$(awk '{ print $1 }' <<<"$line")"
          ;;
      esac

      owner_write="''${mode:2:1}"
      [ "$owner_write" = "w" ] || {
        echo "expected ${bakedPath} (resolved through any symlink) to be owner-writable in the built image, but its effective mode is '$mode' (owner write bit missing -- fakeRootCommands chowns but never chmods, issue #2843)" >&2
        exit 1
      }
      touch $out
    '';

  # Issue #262 AC1: `driver = "opencode"` builds a distinct, driver-named
  # image. Read off buildLayeredImage's `.imageName` passthru, so it needs no
  # image realization.
  drivers-image-name-scoped-by-driver =
    assert pkgs.lib.assertMsg (nonRustHarness.image.imageName == "spindrift")
      "the claude Driver image must keep the historical name spindrift, got: ${nonRustHarness.image.imageName}";
    assert pkgs.lib.assertMsg (opencodeHarness.image.imageName == "spindrift-opencode")
      "the opencode Driver image must be named spindrift-opencode, got: ${opencodeHarness.image.imageName}";
    pkgs.runCommand "drivers-image-name-scoped-by-driver" { } "touch $out";

  # The Box must run unprivileged: Claude Code refuses
  # --dangerously-skip-permissions under root.
  box-runs-as-non-root =
    pkgs.runCommand "box-runs-as-non-root" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir img && tar -xf ${nonRustHarness.image} -C img
        cfg=$(jq -r '.[0].Config' img/manifest.json)
        user=$(jq -r '.config.User // ""' "img/$cfg")
        echo "image config User = '$user'"
        [ "$user" = "agent" ] || {
          echo "expected the Box to run as non-root 'agent', got '$user'" >&2
          exit 1
        }
        touch $out
      '';

  # The rendered prompt must be baked into the agent-files layer at
  # /agent/prompts so the Box needs no host /nix/store mount, which a macOS
  # podman VM cannot provide.
  prompt-baked-into-image = pkgs.runCommand "prompt-baked-into-image" { } ''
    grep -q 'CONFIGURED-PROMPT-MARKER' \
      ${promptHarness.internals.agentFiles}${agentPaths.PROMPTS_DIR}/issue-prompt.md
    grep -q 'git rebase' \
      ${promptHarness.internals.agentFiles}${agentPaths.PROMPTS_DIR}/conflict-resolve-prompt.md
    grep -q 'Fix box for GitHub issue' \
      ${promptHarness.internals.agentFiles}${agentPaths.PROMPTS_DIR}/fix-prompt.md
    # fix-prompt.md's fix-specific preamble is baked as-is, but the shared
    # outcome contract (LAND THE CHANGE onward) only ever reaches it via
    # injection (issue #455) — proof the baked image, not just the eval-only
    # promptDir, carries it. OPEN_PR_CREATE_READ_WRITE_STEP is OPEN A PULL
    # REQUEST's own reference (issue #1919: the `gh pr create --draft`
    # invocation itself moved into a fragment file, rendered at runtime, so
    # the un-rendered baked template carries the placeholder, not the
    # literal command), distinct from fix-prompt.md's hand-written preamble
    # (which explicitly skips `gh pr create` on a fix pass).
    grep -q 'OPEN_PR_CREATE_READ_WRITE_STEP' \
      ${promptHarness.internals.agentFiles}${agentPaths.PROMPTS_DIR}/fix-prompt.md
    touch $out
  '';

  # The SPINDRIFT_OUTCOME contract is baked at /agent, a sibling of
  # /agent/prompts, so a SPINDRIFT_PROMPT_DIR mount shadowing /agent/prompts
  # never hides it at run time (issue #420). It must be byte-identical to the
  # source #419 exports, so build-time and run-time injection cannot drift.
  outcome-contract-baked-into-image = pkgs.runCommand "outcome-contract-baked-into-image" { } ''
    diff ${batsHarness.internals.outcomeContractFile} \
      ${batsHarness.internals.agentFiles}${agentPaths.OUTCOME_CONTRACT_FILE}
    touch $out
  '';

  # The COMMS and CHECK/COMMIT blocks fix-prompt.md shares with
  # issue-prompt.md (issue #455) are baked at /agent for the same reason:
  # byte-identical to the source, so the two injections cannot drift.
  comms-contract-baked-into-image = pkgs.runCommand "comms-contract-baked-into-image" { } ''
    diff ${batsHarness.internals.commsContractFile} \
      ${batsHarness.internals.agentFiles}${agentPaths.COMMS_CONTRACT_FILE}
    touch $out
  '';

  check-contract-baked-into-image = pkgs.runCommand "check-contract-baked-into-image" { } ''
    diff ${batsHarness.internals.checkContractFile} \
      ${batsHarness.internals.agentFiles}${agentPaths.CHECK_CONTRACT_FILE}
    touch $out
  '';

  # The conditional prompt fragments (issue #463) bake inside the overridable
  # prompt directory, unlike the contracts above, so a SPINDRIFT_PROMPT_DIR
  # override must supply its own fragment. fragmentBasenames comes from
  # lib/fragments.nix rather than a hardcoded list, so a new row cannot drop
  # out of coverage (issue #957, and issue #956 for the bats mirror).
  fragments-baked-into-image = pkgs.runCommand "fragments-baked-into-image" { } ''
    for f in ${pkgs.lib.concatStringsSep " " fragmentBasenames}; do
      diff ${../../templates/default/prompts/fragments}/"$f".md \
        ${batsHarness.internals.agentFiles}${agentPaths.PROMPTS_DIR}/fragments/"$f".md
    done
    touch $out
  '';

  # Issue #2531: the entrypoint preamble (lib/preambles.nix) and agentFiles'
  # cp destinations (lib/image.nix) render from the same agent-paths binding,
  # so this proves the real built image agrees with it. Its "diverges" arm
  # cannot fire from a rename, since both sides move together;
  # agent-paths-preamble-detects-divergence proves the grep shape instead.
  agent-paths-preamble-baked-into-image =
    pkgs.runCommand "agent-paths-preamble-baked-into-image" { }
      ''
        ep=${batsHarness.internals.agentFiles}/agent/entrypoint.sh
        af=${batsHarness.internals.agentFiles}
        ${pkgs.lib.concatStrings (
          pkgs.lib.mapAttrsToList (
            var: path:
            let
              # Called on a singleton attrset so the check exercises
              # renderAgentPathsPreamble's own escapeShellArg treatment
              # instead of restating it by hand.
              pattern = pkgs.lib.removeSuffix "\n" (preambles.renderAgentPathsPreamble { ${var} = path; });
            in
            ''
              grep -qF ${pkgs.lib.escapeShellArg pattern} "$ep" || {
                echo ${pkgs.lib.escapeShellArg "entrypoint preamble is missing or diverges from ${pattern} -- rename not caught?"} >&2
                exit 1
              }
              [ -e "$af${path}" ] || {
                echo "${var} names ${path} but nothing is baked there" >&2
                exit 1
              }
            ''
          ) agentPaths
        )}
        touch $out
      '';

  # Issue #2531 (review fix-pass, AC2): proves the `grep -qF <line> <file> ||
  # exit 1` shape used above really rejects a mismatch, which that check
  # cannot show itself. Both candidate lines go through the real
  # renderAgentPathsPreamble, so this also pins that grep -F treats the
  # renderer's output as a literal rather than a glob or a regex.
  agent-paths-preamble-detects-divergence =
    let
      var = "PROMPTS_DIR";
      expectedPattern = pkgs.lib.removeSuffix "\n" (
        preambles.renderAgentPathsPreamble { ${var} = agentPaths.${var}; }
      );
      wrongLine = preambles.renderAgentPathsPreamble { ${var} = "/agent/WRONG_PATH"; };
      wrongCandidate = pkgs.writeText "fake-entrypoint-with-wrong-prompts-dir.sh" ''
        #!/usr/bin/env bash
        ${wrongLine}
      '';
    in
    pkgs.runCommand "agent-paths-preamble-detects-divergence" { } ''
      if grep -qF ${pkgs.lib.escapeShellArg expectedPattern} ${wrongCandidate}; then
        echo "expected the assertion to catch this deliberate mismatch, but it didn't" >&2
        exit 1
      fi
      touch $out
    '';

  # Issue #2531 (review fix-pass): driverPreamble and agentPathsPreamble are
  # separate halves of lib/image.nix's entrypoint `text`, and the agent-paths
  # check greps a disjoint set of lines, so only this check catches
  # driverPreamble being dropped whole, which would ship a Box that dies on
  # an unbound DRIVER_* variable at run time.
  driver-preamble-baked-into-image =
    let
      driverEntry = driverRegistry.entries.claude;
      # Filtered out of the full rendered text, which also carries envCommon
      # exports and function bodies this check does not cover. These lines
      # come from the same driverEntry renderPreamble reads, so only an
      # omission, never a value mismatch, can fail here.
      driverPreambleLines = builtins.filter (pkgs.lib.hasPrefix "DRIVER_") (
        pkgs.lib.splitString "\n" (driverRegistry.renderPreamble driverEntry)
      );
    in
    pkgs.runCommand "driver-preamble-baked-into-image" { } ''
      ep=${batsHarness.internals.agentFiles}/agent/entrypoint.sh
      ${pkgs.lib.concatStrings (
        map (line: ''
          grep -qF ${pkgs.lib.escapeShellArg line} "$ep" || {
            echo ${pkgs.lib.escapeShellArg "entrypoint is missing or diverges from ${line} -- Driver preamble not baked?"} >&2
            exit 1
          }
        '') driverPreambleLines
      )}
      touch $out
    '';

  # Issue #2354 moved shared-block injection out of agent/entrypoint.sh into
  # promptassembly/assemble.go, so this is a static grep pin on that Go call
  # site's contract-file fields. One grep covers the outcome, COMMS and
  # CHECK/COMMIT markers because the call passes all three together (issue
  # #455). CODE COMMENTS left this call site for an anchor (issue #3221).
  outcome-comms-check-contract-marker-parity =
    pkgs.runCommand "outcome-comms-check-contract-marker-parity" { }
      ''
        grep -qF 'e.CommsContractFile, e.CheckContractFile, e.OutcomeContractFile' ${../../cmd/launcher/internal/promptassembly/assemble.go}
        touch $out
      '';

  # The same drift guard for the research-verdict marker (issue #640's
  # "research-verdict" row), which no parity check covered until issue #2246
  # slice 1.
  research-outcome-contract-marker-parity =
    pkgs.runCommand "research-outcome-contract-marker-parity" { }
      ''
        grep -qF 'injectSharedBlockSegments(base, e.ResearchOutcomeContractFile, vars)' ${../../cmd/launcher/internal/promptassembly/assemble.go}
        touch $out
      '';

  # Skills configured at build time must land at the fixed /agent/skills path
  # (issue #2489) alongside the harness-owned skills; agent/entrypoint.sh
  # copies from there into the Driver's runtime skills dir at box startup.
  skills-baked-into-image = pkgs.runCommand "skills-baked-into-image" { } ''
    grep -q 'BAKED-SKILL-MARKER' \
      ${skillsHarness.internals.agentFiles}/agent/skills/baked-skill/SKILL.md
    touch $out
  '';

  # The harness-owned auto-format skill (issue #2489) bakes into every image
  # regardless of the Consumer's own `skills` list, so this builds against
  # noSkillsHarness, which configures none.
  auto-format-skill-baked-into-image = pkgs.runCommand "auto-format-skill-baked-into-image" { } ''
    skill=${noSkillsHarness.internals.agentFiles}/agent/skills/auto-format/SKILL.md
    [ -s "$skill" ]
    grep -q 'Never .nix fmt.' "$skill"
    touch $out
  '';

  # The harness-owned auto-lint skill (issue #2490) bakes into every image
  # regardless of the Consumer's own `skills` list, so this builds against
  # noSkillsHarness, which configures none.
  auto-lint-skill-baked-into-image = pkgs.runCommand "auto-lint-skill-baked-into-image" { } ''
    skill=${noSkillsHarness.internals.agentFiles}/agent/skills/auto-lint/SKILL.md
    [ -s "$skill" ]
    grep -q 'safe auto-fix mode where available' "$skill"
    touch $out
  '';

  # The harness-owned check-hygiene skill (issue #3220) bakes into every
  # image regardless of the Consumer's own `skills` list, so this builds
  # against noSkillsHarness, which configures none.
  check-hygiene-skill-baked-into-image = pkgs.runCommand "check-hygiene-skill-baked-into-image" { } ''
    skill=${noSkillsHarness.internals.agentFiles}/agent/skills/check-hygiene/SKILL.md
    [ -s "$skill" ]
    grep -q 'exit marker' "$skill"
    touch $out
  '';

  # The harness-owned code-comments skill (issue #3221) bakes into every
  # image regardless of the Consumer's own `skills` list, so this builds
  # against noSkillsHarness, which configures none.
  code-comments-skill-baked-into-image = pkgs.runCommand "code-comments-skill-baked-into-image" { } ''
    skill=${noSkillsHarness.internals.agentFiles}/agent/skills/code-comments/SKILL.md
    [ -s "$skill" ]
    grep -q 'non-obvious why' "$skill"
    touch $out
  '';

  # The PreToolUse hook rejecting backgrounded Bash calls needs both the hook
  # script and the settings.json that registers it (issue #1609), so a real
  # Box enforces the restriction and not just the flagsCommon layer
  # (drivers-claude-blocks-loop-background-affordances in drivers.nix).
  reject-background-bash-hook-baked-into-image =
    pkgs.runCommand "reject-background-bash-hook-baked-into-image" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        hook=${nonRustHarness.internals.agentFiles}/home/agent/.claude/hooks/reject-background-bash.sh
        [ -x "$hook" ] || {
          echo "reject-background-bash.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.internals.agentFiles}/home/agent/.claude/settings.json
        jq -e '.hooks.PreToolUse[0].matcher == "Bash"' "$settings" >/dev/null || {
          echo "settings.json does not register a PreToolUse hook matched to Bash" >&2
          exit 1
        }
        jq -e '.hooks.PreToolUse[0].hooks[0].command | endswith("reject-background-bash.sh")' \
          "$settings" >/dev/null || {
          echo "settings.json's PreToolUse hook does not point at reject-background-bash.sh" >&2
          exit 1
        }
        touch $out
      '';

  # The Driver must not reach a credential file even under
  # --dangerously-skip-permissions (issue #1909, spec #1907), so
  # credential-deny.sh is registered for both the Read and Bash matchers: a
  # Bash call can shell-cat a credential path the same way a Read call can
  # open it.
  credential-deny-hook-baked-into-image =
    pkgs.runCommand "credential-deny-hook-baked-into-image" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        hook=${nonRustHarness.internals.agentFiles}/home/agent/.claude/hooks/credential-deny.sh
        [ -x "$hook" ] || {
          echo "credential-deny.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.internals.agentFiles}/home/agent/.claude/settings.json
        for matcher in Read Bash; do
          jq -e --arg matcher "$matcher" \
            'any(.hooks.PreToolUse[]; .matcher == $matcher and (.hooks[0].command | endswith("credential-deny.sh")))' \
            "$settings" >/dev/null || {
            echo "settings.json does not register a $matcher-matched PreToolUse hook pointing at credential-deny.sh" >&2
            exit 1
          }
        done
        touch $out
      '';

  # ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN must not be inherited into
  # a spawned Bash subprocess's environment even under
  # --dangerously-skip-permissions (issue #1927, spec #1907), so
  # env-credential-scrub.sh is registered for the Bash matcher.
  env-credential-scrub-hook-baked-into-image =
    pkgs.runCommand "env-credential-scrub-hook-baked-into-image" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        hook=${nonRustHarness.internals.agentFiles}/home/agent/.claude/hooks/env-credential-scrub.sh
        [ -x "$hook" ] || {
          echo "env-credential-scrub.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.internals.agentFiles}/home/agent/.claude/settings.json
        jq -e \
          'any(.hooks.PreToolUse[]; .matcher == "Bash" and (.hooks[0].command | endswith("env-credential-scrub.sh")))' \
          "$settings" >/dev/null || {
          echo "settings.json does not register a Bash-matched PreToolUse hook pointing at env-credential-scrub.sh" >&2
          exit 1
        }
        touch $out
      '';

  # The Bash command-output interceptor (issue #1988) is a pair:
  # bash-output-tee.sh tees each Bash call's output to a per-command log, and
  # bash-output-summary.sh replaces the tool result with a bounded tail once
  # that log crosses the inline bound.
  bash-output-tee-hook-baked-into-image =
    pkgs.runCommand "bash-output-tee-hook-baked-into-image" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        hook=${nonRustHarness.internals.agentFiles}/home/agent/.claude/hooks/bash-output-tee.sh
        [ -x "$hook" ] || {
          echo "bash-output-tee.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.internals.agentFiles}/home/agent/.claude/settings.json
        jq -e \
          'any(.hooks.PreToolUse[]; .matcher == "Bash" and (.hooks[0].command | endswith("bash-output-tee.sh")))' \
          "$settings" >/dev/null || {
          echo "settings.json does not register a Bash-matched PreToolUse hook pointing at bash-output-tee.sh" >&2
          exit 1
        }
        touch $out
      '';

  bash-output-summary-hook-baked-into-image =
    pkgs.runCommand "bash-output-summary-hook-baked-into-image" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        hook=${nonRustHarness.internals.agentFiles}/home/agent/.claude/hooks/bash-output-summary.sh
        [ -x "$hook" ] || {
          echo "bash-output-summary.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.internals.agentFiles}/home/agent/.claude/settings.json
        jq -e \
          'any(.hooks.PostToolUse[]; .matcher == "Bash" and (.hooks[0].command | endswith("bash-output-summary.sh")))' \
          "$settings" >/dev/null || {
          echo "settings.json does not register a Bash-matched PostToolUse hook pointing at bash-output-summary.sh" >&2
          exit 1
        }
        touch $out
      '';

  # The nix.conf and store DB must be present in the image so
  # `nix flake check` reuses the baked closure instead of re-substituting.
  nix-conf-in-image = pkgs.runCommand "nix-conf-in-image" { nativeBuildInputs = [ pkgs.jq ]; } ''
    # Extract the image ONCE (like box-runs-as-non-root), then read
    # only the top "customisation" layer where extraCommands writes
    # nix.conf. Reading the compressed image more than once exhausts
    # the runner's disk burst credits and wedges CI for minutes;
    # re-reading all ~98 extracted layers is just as slow.
    mkdir img && tar -xf ${nonRustHarness.image} -C img
    layer="$(jq -r '.[0].Layers[-1]' img/manifest.json)"
    # The customisation layer is packed with `tar -cf layer.tar .`, so
    # members carry a leading `./`; match and extract the real name.
    member="$(tar -tf "img/$layer" \
      | grep -E '^(\./)?etc/nix/nix\.conf$' | head -1 || true)"
    [ -n "$member" ] || {
      echo "etc/nix/nix.conf not in the image's top (customisation) layer" >&2
      exit 1
    }
    tar -xOf "img/$layer" "$member" > nix.conf
    grep -q 'experimental-features = nix-command flakes' nix.conf || {
      echo "nix.conf is missing experimental-features" >&2
      exit 1
    }
    grep -q 'sandbox = false' nix.conf || {
      echo "nix.conf is missing sandbox = false" >&2
      exit 1
    }
    grep -q 'cores = 4' nix.conf || {
      echo "nix.conf is missing cores = 4" >&2
      exit 1
    }
    touch $out
  '';

  # The driver-cache mountpoint (the Driver's session-state dir, ADR 0009)
  # must be baked owned by uid 1000 so podman reuses it instead of
  # fabricating root-owned parent dirs at mount time (issue #447). The path
  # comes from driverEntry (issue #448), and only the top layer carries that
  # ownership, because fakeRootCommands' chown runs there.
  projects-mountpoint-baked =
    let
      relPath = nonRustHarness.internals.driverEntry.sessionCacheDirRelative;
      bakedPath = "home/agent/${relPath}";
      awkPattern = pkgs.lib.replaceStrings [ "/" "." ] [ "\\/" "\\." ] bakedPath;
    in
    pkgs.runCommand "projects-mountpoint-baked" { nativeBuildInputs = [ pkgs.jq ]; } ''
      mkdir img && tar -xf ${nonRustHarness.image} -C img
      layer="$(jq -r '.[0].Layers[-1]' img/manifest.json)"
      uid=$(tar --numeric-owner -tvf "img/$layer" \
        | awk '/${awkPattern}\/?$/ { split($2,a,"/"); print a[1]; exit }' \
        || true)
      [ -n "$uid" ] || {
        echo "${bakedPath} not found in the image's top (customisation) layer" >&2
        exit 1
      }
      [ "$uid" = "1000" ] || {
        echo "${bakedPath} is not owned by uid 1000 (got: '$uid')" >&2
        exit 1
      }
      touch $out
    '';

  # nix/var must be owned by uid 1000 so the non-root agent can lock the
  # SQLite store DB inside the unprivileged container (issue #356).
  # --numeric-owner prints the raw uid, so the check does not depend on
  # /etc/passwd names.
  nix-var-owned-by-agent =
    pkgs.runCommand "nix-var-owned-by-agent" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir img && tar -xf ${nonRustHarness.image} -C img
        layer="$(jq -r '.[0].Layers[-1]' img/manifest.json)"
        uid=$(tar --numeric-owner -tvf "img/$layer" \
          | awk '/nix\/var\/nix\/db\/?$/ { split($2,a,"/"); print a[1]; exit }' \
          || true)
        [ "$uid" = "1000" ] || {
          echo "nix/var/nix/db is not owned by uid 1000 (got: '$uid')" >&2
          exit 1
        }
        touch $out
      '';

  # NIX_STORE_WRITABLE is baked into the image Env by mkHarness's
  # nixStoreWritable knob (ADR 0018, issue #469) so the entrypoint's warning
  # comes from the image, not a runtime-only setting. Each harness's image is
  # extracted only once: repeat compressed-image reads exhaust the runner's
  # disk burst credits and stall CI for minutes.
  nix-store-writable-env-marker =
    pkgs.runCommand "nix-store-writable-env-marker" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir off && tar -xf ${nonRustHarness.image} -C off
        cfg=$(jq -r '.[0].Config' off/manifest.json)
        jq -e '.config.Env | any(. == "NIX_STORE_WRITABLE=false")' "off/$cfg" >/dev/null || {
          echo "default harness (nixStoreWritable=false) must bake NIX_STORE_WRITABLE=false" >&2
          exit 1
        }

        mkdir on && tar -xf ${nixStoreWritableHarness.image} -C on
        cfg=$(jq -r '.[0].Config' on/manifest.json)
        jq -e '.config.Env | any(. == "NIX_STORE_WRITABLE=true")' "on/$cfg" >/dev/null || {
          echo "nixStoreWritable=true harness must bake NIX_STORE_WRITABLE=true" >&2
          exit 1
        }
        touch $out
      '';

  # See "Claude Code output caps" in docs/reference.md for these values and
  # why they are set (issue #1987). The literals below are a deliberate
  # independent pin on the *built* image, not derived from lib/output-caps.nix
  # (issue #3679) -- changing a cap means editing lib/output-caps.nix *and*
  # this check.
  output-cap-env-marker =
    pkgs.runCommand "output-cap-env-marker" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir off && tar -xf ${nonRustHarness.image} -C off
        cfg=$(jq -r '.[0].Config' off/manifest.json)
        assert_baked_env() {
          local name=$1 expected=$2 hint=$3 found
          found=$(jq -r --arg n "$name" '(.config.Env // []) | map(select(startswith($n + "="))) | .[0] // "(not set)"' "off/$cfg")
          [ "$found" = "$name=$expected" ] || {
            echo "default harness must bake $name=$expected, found $found -- $hint" >&2
            exit 1
          }
        }
        assert_baked_env BASH_MAX_OUTPUT_LENGTH ${toString pinnedBashMaxOutputLength} \
          "update lib/output-caps.nix and nix/checks/image.nix's pinnedBashMaxOutputLength together"
        assert_baked_env MAX_MCP_OUTPUT_TOKENS ${toString pinnedMaxMcpOutputTokens} \
          "update lib/image.nix and nix/checks/image.nix's pinnedMaxMcpOutputTokens together"
        touch $out
      '';

  # /nix/store itself becomes agent-writable only when nixStoreWritable is
  # opted in, and non-recursively so baked paths stay root-owned. In the
  # default image the directory may be absent from the top layer or present
  # at its original owner; either reads as "not chowned to the agent".
  nix-store-writable-chown =
    pkgs.runCommand "nix-store-writable-chown" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir on && tar -xf ${nixStoreWritableHarness.image} -C on
        layer="$(jq -r '.[0].Layers[-1]' on/manifest.json)"
        uid=$(tar --numeric-owner -tvf "on/$layer" \
          | awk '/(^|\/)nix\/store\/?$/ { split($2,a,"/"); print a[1]; exit }' \
          || true)
        [ "$uid" = "1000" ] || {
          echo "nix/store is not owned by uid 1000 with nixStoreWritable=true (got: '$uid')" >&2
          exit 1
        }

        mkdir off && tar -xf ${nonRustHarness.image} -C off
        layer="$(jq -r '.[0].Layers[-1]' off/manifest.json)"
        uid=$(tar --numeric-owner -tvf "off/$layer" \
          | awk '/(^|\/)nix\/store\/?$/ { split($2,a,"/"); print a[1]; exit }' \
          || true)
        [ "$uid" != "1000" ] || {
          echo "default harness (nixStoreWritable=false) must not chown nix/store to uid 1000" >&2
          exit 1
        }
        touch $out
      '';

  # fj (forgejo-cli) bakes in only for a forgejo-backend Consumer
  # (issue #1963), so a github-backend image never carries an unused CLI.
  forgejo-cli-baked-only-for-forgejo-backend =
    pkgs.runCommand "forgejo-cli-baked-only-for-forgejo-backend" { }
      ''
        test -x ${forgejoHarness.internals.agentEnv}/bin/fj || {
          echo "fj missing from forgejo-backend image" >&2
          exit 1
        }
        ! test -e ${nonRustHarness.internals.agentEnv}/bin/fj || {
          echo "fj leaked into non-forgejo image" >&2
          exit 1
        }
        touch $out
      '';

  # extraClosures derivations must be physically present in the image:
  # contents = [...] ++ extraClosures pulls the closure into the store layers
  # the same way agentEnv and agentFiles do. Listing each already-extracted
  # layer is cheap; only the initial compressed-image read is expensive.
  extra-closure-in-image-contents =
    pkgs.runCommand "extra-closure-in-image-contents" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir img && tar -xf ${extraClosuresHarness.image} -C img
        found=""
        # grep must drain tar's stream (no -q): stdenv runs with pipefail, and
        # grep -q exits on first match, SIGPIPE-ing tar -- whether the pipeline
        # then reports 141 or 0 is a pipe-buffer race, so a match may read as
        # a miss (broke main at 6ec6273).
        for layer in $(jq -r '.[0].Layers[]' img/manifest.json); do
          if tar -tf "img/$layer" | grep 'nix/store/[^/]*-cowsay-' >/dev/null; then
            found=1
            break
          fi
        done
        [ -n "$found" ] || {
          echo "extraClosures (cowsay) not physically present in any image layer" >&2
          exit 1
        }
        touch $out
      '';

  # The extraClosures closure must also be registered in the baked store DB
  # (the same top customisation layer nix-conf-in-image inspects), so in-box
  # nix sees it as already present instead of cold-substituting it.
  extra-closure-registered-in-db =
    pkgs.runCommand "extra-closure-registered-in-db" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir img && tar -xf ${extraClosuresHarness.image} -C img
        layer="$(jq -r '.[0].Layers[-1]' img/manifest.json)"
        member="$(tar -tf "img/$layer" \
          | grep -E '^(\./)?nix/var/nix/db/db\.sqlite$' | head -1 || true)"
        [ -n "$member" ] || {
          echo "nix/var/nix/db/db.sqlite not in the image's top (customisation) layer" >&2
          exit 1
        }
        # no -q: same pipefail/SIGPIPE race as extra-closure-in-image-contents
        tar -xOf "img/$layer" "$member" | grep -a 'cowsay-' >/dev/null || {
          echo "extraClosures (cowsay) not found in the registered store DB" >&2
          exit 1
        }
        touch $out
      '';
}
