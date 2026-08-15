# Linux-gated image-layer inspection: assertions that realize the OCI image
# and inspect its layers/config, so they are omitted from `nix flake check`
# on darwin (see the optionalAttrs pkgs.stdenv.isLinux wrapping this module's
# import in nix/checks/default.nix).
{ pkgs, fixtures, ... }:
let
  inherit (fixtures)
    nonRustHarness
    customHarness
    scoutOnlyHarness
    reviewerOnlyHarness
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
  fragmentRows = import ../../lib/fragments.nix;
  fragmentBasenames = map (row: pkgs.lib.removeSuffix ".md" row.fragment) fragmentRows;
  # The shared prompt-block registry (issue #2245) lib/mkHarness.nix's
  # injectOutcomeContract/injectComms/injectCheckCommit/
  # injectResearchOutcomeContract now derive their markers from -- the
  # marker-parity checks below assert against this registry instead of a
  # hand-wired `lib/mkHarness.nix` literal (issue #2246 slice 1).
  promptContract = import ../../lib/prompt-contract.nix;
  inherit (promptContract) byId;
  # Single source of truth for the literal asserted below (issue #2433):
  # read reviewModel's default straight from the schema instead of
  # restating it by hand, so a future bump only edits lib/env-schema.nix.
  reviewModelSchemaDefault = (import ../../lib/env-schema.nix).reviewModel.default;
  # Single source of truth for the per-agent effort literals asserted below
  # (issue #2506): read them from lib/roster-schema-defaults.nix instead of
  # restating them by hand. Deliberately does NOT extend to the model
  # literals nearby (issue #2435 AC2) -- see that fixture's own comments.
  rosterDefaults =
    (import ../../lib/roster-schema-defaults.nix { inherit (pkgs) lib; }).rosterDefaults;
in
{
  # The baked entrypoint must carry a store-path shebang, not the
  # source's `#!/usr/bin/env bash` — the Box has no /usr/bin/env. Guards
  # against baking the raw source instead of the writeShellApplication
  # output. Realizes the agent-files layer, so it is gated to a Linux
  # builder and omitted from `nix flake check` on darwin.
  entrypoint-shebang = pkgs.runCommand "entrypoint-shebang" { } ''
    shebang=$(head -1 ${nonRustHarness.agentFiles}/agent/entrypoint.sh)
    case "$shebang" in
      '#!'/nix/store/*bash*) : ;;
      *) echo "entrypoint shebang is not a store bash: $shebang" >&2
         exit 1 ;;
    esac
    touch $out
  '';

  # AGENTS_JSON_TEMPLATE baked into the entrypoint by nix (ADR 0007): each
  # subagent is composed independently by its own model knob (issue #392), so
  # the template carries whichever of scout/reviewer have a model configured,
  # and is the empty string only when neither does.
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

    ep=${customHarness.agentFiles}/agent/entrypoint.sh

    # The custom harness bakes both models — template must contain them.
    grep -q 'custom-scout' "$ep" \
      || { echo "scout model not found in baked entrypoint" >&2; exit 1; }
    grep -q 'custom-reviewer' "$ep" \
      || { echo "reviewer model not found in baked entrypoint" >&2; exit 1; }
    grep -q 'AGENTS_JSON_TEMPLATE=' "$ep" \
      || { echo "AGENTS_JSON_TEMPLATE assignment missing from entrypoint" >&2; exit 1; }

    # Default harness bakes no models → template must not contain JSON content.
    ! grep -q 'AGENTS_JSON_TEMPLATE=.*{' ${nonRustHarness.agentFiles}/agent/entrypoint.sh \
      || { echo "AGENTS_JSON_TEMPLATE is non-empty for no-model harness" >&2; exit 1; }

    # A scout-only harness bakes the scout entry alone — no reviewer key at all.
    scout_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${scoutOnlyHarness.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-scout' <<<"$scout_line" \
      || { echo "scout-only harness missing scout model in baked template" >&2; exit 1; }
    ! grep -q '"reviewer"' <<<"$scout_line" \
      || { echo "scout-only harness unexpectedly bakes a reviewer entry" >&2; exit 1; }
    # defaultRoster's fixed default effort (issue #2386) must survive the
    # roster==null fallback (lib/mkHarness.nix:317-327) end-to-end: the
    # driver-level checks (nix/checks/drivers.nix) pin the effort literal
    # render but bypass that fallback by feeding a hand-built roster straight
    # to the driver renderer, so only this fixture (no `roster` arg) proves
    # the wiring from lib/roster.nix's literal through to the baked template.
    grep -q '"effort":"${rosterDefaults.scout.effort}"' <<<"$scout_line" \
      || { echo "scout-only harness missing default scout effort in baked template" >&2; exit 1; }

    # The reviewer-only mirror.
    reviewer_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${reviewerOnlyHarness.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-reviewer' <<<"$reviewer_line" \
      || { echo "reviewer-only harness missing reviewer model in baked template" >&2; exit 1; }
    ! grep -q '"scout"' <<<"$reviewer_line" \
      || { echo "reviewer-only harness unexpectedly bakes a scout entry" >&2; exit 1; }
    # See the scout-only comment above: same roster==null fallback proof, for
    # reviewer's default effort.
    grep -q '"effort":"${rosterDefaults.reviewer.effort}"' <<<"$reviewer_line" \
      || { echo "reviewer-only harness missing default reviewer effort in baked template" >&2; exit 1; }

    # The filer-only mirror (opt-in, default empty — issue #393): composed
    # independently like scout/reviewer, no scout/reviewer keys alongside it.
    filer_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${filerOnlyHarness.agentFiles}/agent/entrypoint.sh)
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
    worker_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${workerOnlyHarness.agentFiles}/agent/entrypoint.sh)
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
    dogfood_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${harness.agentFiles}/agent/entrypoint.sh)
    assert_agent_model "$dogfood_line" filer claude-haiku-4-5-20251001 \
      "dogfood harness" "missing the configured model"

    assert_agent_model "$dogfood_line" scout claude-haiku-4-5-20251001 \
      "dogfood harness" "missing the inherited model"

    # Anchored to the literal "claude-opus-5", not reviewModelSchemaDefault --
    # same rationale as nix/checks/equivalence.nix's
    # dogfood-roster-and-review-effort reviewer assertion: the code-owned
    # review pass binds to this exact model, so the guard must catch a
    # schema-default regression away from it, not just confirm the bake
    # mirrors whatever the schema currently says.
    assert_agent_model "$dogfood_line" reviewer claude-opus-5 \
      "dogfood harness" "missing the anchored claude-opus-5 model"

    assert_agent_model "$dogfood_line" worker claude-sonnet-5 \
      "dogfood harness" "missing the inherited model"

    # A Consumer that sets no model knobs and passes no roster (bats harness:
    # no `defaults`, no `roster`) must still get a reviewer on the schema
    # default (issue #2433) via the roster==null fallback
    # (lib/mkHarness.nix:317-327) -- reviewModel's default moved to
    # ${reviewModelSchemaDefault} so every Consumer's reviewer runs on the
    # strongest available model without configuring anything.
    bats_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${batsHarness.agentFiles}/agent/entrypoint.sh)
    assert_agent_model "$bats_line" reviewer '${reviewModelSchemaDefault}' \
      "bats harness" "missing the default ${reviewModelSchemaDefault} model"

    touch $out
  '';

  # opencode has no --agents JSON flag; it discovers subagents from
  # HOME-relative agents/*.md files instead (issue #262 slice 5, AC4). The
  # image-baking half of that contract: opencodeHarness's agentFiles layer
  # must carry scout.md/reviewer.md/worker.md (each model-gated model set)
  # with the frontmatter's mode/model fields baked in, and must NOT carry
  # filer.md at all (filerModel left empty), mirroring agents-json-baked's
  # per-agent omission proof above but for the on-disk file mechanism.
  # Realizes the agent-files layer, so it is Linux-gated like the other image
  # checks -- but only agentFiles, not the OCI image itself, so it stays
  # light (no dockerTools.buildLayeredImage).
  opencode-agent-files = pkgs.runCommand "opencode-agent-files" { } ''
    scout=${opencodeHarness.agentFiles}/home/agent/.config/opencode/agents/scout.md
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

    reviewer=${opencodeHarness.agentFiles}/home/agent/.config/opencode/agents/reviewer.md
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

    worker=${opencodeHarness.agentFiles}/home/agent/.config/opencode/agents/worker.md
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

    filer=${opencodeHarness.agentFiles}/home/agent/.config/opencode/agents/filer.md
    [ ! -e "$filer" ] || {
      echo "opencode agentFiles unexpectedly bakes filer.md (filerModel is empty)" >&2
      exit 1
    }

    touch $out
  '';

  # Issue #262 AC1: `driver = "opencode"` builds a distinct, driver-named
  # image. Proven eval-only off buildLayeredImage's `.imageName` passthru (no
  # image realization needed): the default claude Driver keeps the historical
  # `spindrift` name, opencode gets `spindrift-opencode`.
  drivers-image-name-scoped-by-driver =
    assert pkgs.lib.assertMsg (nonRustHarness.image.imageName == "spindrift")
      "the claude Driver image must keep the historical name spindrift, got: ${nonRustHarness.image.imageName}";
    assert pkgs.lib.assertMsg (opencodeHarness.image.imageName == "spindrift-opencode")
      "the opencode Driver image must be named spindrift-opencode, got: ${opencodeHarness.image.imageName}";
    pkgs.runCommand "drivers-image-name-scoped-by-driver" { } "touch $out";

  # The Box must run unprivileged: Claude Code refuses
  # --dangerously-skip-permissions under root. Assert the image config
  # runs as the non-root `agent` user. Realizes the image, so it is
  # Linux-gated like the shebang check.
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
  # /agent/prompts, so the Box is self-contained and needs no host
  # /nix/store mount (which a macOS podman VM cannot provide). Realizes
  # the agent-files layer, so it is Linux-gated like the shebang check.
  prompt-baked-into-image = pkgs.runCommand "prompt-baked-into-image" { } ''
    grep -q 'CONFIGURED-PROMPT-MARKER' \
      ${promptHarness.agentFiles}/agent/prompts/issue-prompt.md
    grep -q 'git rebase' \
      ${promptHarness.agentFiles}/agent/prompts/conflict-resolve-prompt.md
    grep -q 'Fix box for GitHub issue' \
      ${promptHarness.agentFiles}/agent/prompts/fix-prompt.md
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
      ${promptHarness.agentFiles}/agent/prompts/fix-prompt.md
    touch $out
  '';

  # The canonical SPINDRIFT_OUTCOME contract must be baked at /agent, a
  # sibling of /agent/prompts, so a SPINDRIFT_PROMPT_DIR mount (which shadows
  # only /agent/prompts) never hides it from the entrypoint at run time
  # (issue #420) -- and it must be byte-identical to the single source #419
  # already exports, so the build-time and run-time injections cannot drift.
  outcome-contract-baked-into-image = pkgs.runCommand "outcome-contract-baked-into-image" { } ''
    diff ${batsHarness.outcomeContractFile} \
      ${batsHarness.agentFiles}/agent/outcome-contract.md
    touch $out
  '';

  # The COMMS and CHECK/COMMIT blocks fix-prompt.md shares with
  # issue-prompt.md (issue #455) are baked at /agent the same way, for the
  # same reason: byte-identical to the single source, so build-time and
  # run-time injection cannot drift.
  comms-contract-baked-into-image = pkgs.runCommand "comms-contract-baked-into-image" { } ''
    diff ${batsHarness.commsContractFile} \
      ${batsHarness.agentFiles}/agent/comms-contract.md
    touch $out
  '';

  check-contract-baked-into-image = pkgs.runCommand "check-contract-baked-into-image" { } ''
    diff ${batsHarness.checkContractFile} \
      ${batsHarness.agentFiles}/agent/check-contract.md
    touch $out
  '';

  # The conditional prompt fragments (issue #463) must be baked under
  # /agent/prompts/fragments -- inside the overridable prompt surface, unlike
  # the contracts above -- so a SPINDRIFT_PROMPT_DIR override that wants a
  # knob-gated step present must supply its own fragment, exactly like it
  # already must supply filer-prompt.md. fragmentBasenames is derived from
  # lib/fragments.nix rather than hardcoded (issue #957), so a new registry
  # row can't silently drop out of image coverage (same fix as #956's bats
  # mirror of this test).
  fragments-baked-into-image = pkgs.runCommand "fragments-baked-into-image" { } ''
    for f in ${pkgs.lib.concatStringsSep " " fragmentBasenames}; do
      diff ${../../templates/default/prompts/fragments}/"$f".md \
        ${batsHarness.agentFiles}/agent/prompts/fragments/"$f".md
    done
    touch $out
  '';

  # The idempotency check (issue #420) hinges on the entrypoint sourcing its
  # marker from the same registry row lib/mkHarness.nix now looks up from
  # lib/prompt-contract.nix (issue #2246 slice 1). Since issue #2354's flip,
  # shared-block injection no longer lives in agent/entrypoint.sh's
  # `_inject_shared_block` (deleted, along with the rest of the inline gate/
  # fragment/injection precompute) -- it lives in
  # cmd/launcher/internal/promptassembly/assemble.go's `injectSharedBlock`,
  # called with each contract file's Env field directly (no id lookup at all;
  # Go derives the marker from the block's own first line rather than
  # resolving an id against a registry). So the runtime-wiring half of this
  # check now confirms the Go verb's shared-block-injection call site still
  # references the right contract-file field, instead of grepping the
  # (now-removed) bash call site.
  outcome-contract-marker-parity =
    let
      row = byId "outcome";
    in
    assert pkgs.lib.assertMsg (
      row.marker == "# LAND THE CHANGE"
    ) "prompt-contract.nix outcome row's marker must be '# LAND THE CHANGE', got: ${row.marker}";
    pkgs.runCommand "outcome-contract-marker-parity" { } ''
      grep -qF 'e.CommsContractFile, e.CheckContractFile, e.OutcomeContractFile' ${../../cmd/launcher/internal/promptassembly/assemble.go}
      touch $out
    '';

  # Same drift guard, for the COMMS and CHECK/COMMIT markers (issue #455).
  comms-check-contract-marker-parity =
    let
      commsRow = byId "comms";
      checkRow = byId "check";
    in
    assert pkgs.lib.assertMsg (
      commsRow.marker == "# COMMS"
    ) "prompt-contract.nix comms row's marker must be '# COMMS', got: ${commsRow.marker}";
    assert pkgs.lib.assertMsg (
      checkRow.marker == "# CHECK"
    ) "prompt-contract.nix check row's marker must be '# CHECK', got: ${checkRow.marker}";
    pkgs.runCommand "comms-check-contract-marker-parity" { } ''
      grep -qF 'e.CommsContractFile, e.CheckContractFile, e.OutcomeContractFile' ${../../cmd/launcher/internal/promptassembly/assemble.go}
      touch $out
    '';

  # Same drift guard, for the research-verdict marker (issue #640's
  # "research-verdict" row) -- previously uncovered by any parity check
  # (issue #2246 slice 1 coverage gap fix).
  research-outcome-contract-marker-parity =
    let
      row = byId "research-verdict";
    in
    assert pkgs.lib.assertMsg (row.marker == "# POST THE VERDICT")
      "prompt-contract.nix research-verdict row's marker must be '# POST THE VERDICT', got: ${row.marker}";
    pkgs.runCommand "research-outcome-contract-marker-parity" { } ''
      grep -qF 'injectSharedBlock(promptText, e.ResearchOutcomeContractFile, allowlist)' ${../../cmd/launcher/internal/promptassembly/assemble.go}
      touch $out
    '';

  # Skills configured at build time must land in the agent-files layer at the
  # fixed /agent/skills path (issue #2489), alongside the harness-owned
  # skills, so the Box is self-contained; agent/entrypoint.sh copies from
  # there into the Driver's actual runtime skills dir at box startup.
  # Realizes the agent-files layer; Linux-gated like the other image checks.
  skills-baked-into-image = pkgs.runCommand "skills-baked-into-image" { } ''
    grep -q 'BAKED-SKILL-MARKER' \
      ${skillsHarness.agentFiles}/agent/skills/baked-skill/SKILL.md
    touch $out
  '';

  # The harness-owned auto-format skill (issue #2489) must bake into every
  # image at a fixed /agent/skills path unconditionally -- independent of
  # whatever the Consumer's own `skills` list contains. Built against
  # noSkillsHarness, which configures zero consumer skills, to prove this.
  auto-format-skill-baked-into-image = pkgs.runCommand "auto-format-skill-baked-into-image" { } ''
    skill=${noSkillsHarness.agentFiles}/agent/skills/auto-format/SKILL.md
    [ -s "$skill" ]
    grep -q 'Never .nix fmt.' "$skill"
    touch $out
  '';

  # The Box's agent home ships no settings today (issue #1609); the
  # PreToolUse hook rejecting backgrounded Bash calls must be baked in as
  # both the hook script and the settings.json that registers it, so a real
  # Box actually enforces the restriction and not just the flagsCommon layer
  # (drivers-claude-blocks-loop-background-affordances in drivers.nix).
  # Realizes the agent-files layer; Linux-gated like the other image checks.
  reject-background-bash-hook-baked-into-image =
    pkgs.runCommand "reject-background-bash-hook-baked-into-image" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        hook=${nonRustHarness.agentFiles}/home/agent/.claude/hooks/reject-background-bash.sh
        [ -x "$hook" ] || {
          echo "reject-background-bash.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.agentFiles}/home/agent/.claude/settings.json
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

  # The Driver cannot Read/Bash its own way to a credential file even under
  # --dangerously-skip-permissions (issue #1909, spec #1907): a second
  # PreToolUse hook, credential-deny.sh, must be baked in alongside
  # reject-background-bash.sh and registered for both the Read and Bash
  # matchers (a Bash call can shell-cat a credential path the same way a
  # Read call can open it directly). Realizes the agent-files layer;
  # Linux-gated like the other image checks.
  credential-deny-hook-baked-into-image =
    pkgs.runCommand "credential-deny-hook-baked-into-image" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        hook=${nonRustHarness.agentFiles}/home/agent/.claude/hooks/credential-deny.sh
        [ -x "$hook" ] || {
          echo "credential-deny.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.agentFiles}/home/agent/.claude/settings.json
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

  # The Driver cannot end up with ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN
  # inherited into a spawned Bash subprocess's environment even under
  # --dangerously-skip-permissions (issue #1927, spec #1907): a third
  # PreToolUse hook, env-credential-scrub.sh, must be baked in alongside
  # reject-background-bash.sh and credential-deny.sh and registered for the
  # Bash matcher. Realizes the agent-files layer; Linux-gated like the other
  # image checks.
  env-credential-scrub-hook-baked-into-image =
    pkgs.runCommand "env-credential-scrub-hook-baked-into-image" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        hook=${nonRustHarness.agentFiles}/home/agent/.claude/hooks/env-credential-scrub.sh
        [ -x "$hook" ] || {
          echo "env-credential-scrub.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.agentFiles}/home/agent/.claude/settings.json
        jq -e \
          'any(.hooks.PreToolUse[]; .matcher == "Bash" and (.hooks[0].command | endswith("env-credential-scrub.sh")))' \
          "$settings" >/dev/null || {
          echo "settings.json does not register a Bash-matched PreToolUse hook pointing at env-credential-scrub.sh" >&2
          exit 1
        }
        touch $out
      '';

  # The Bash command-output interceptor (issue #1988) must be baked in as a
  # pair: bash-output-tee.sh (PreToolUse, Bash matcher) tees every Bash
  # call's output to a per-command log file; bash-output-summary.sh
  # (PostToolUse, Bash matcher) replaces the tool result with a bounded tail
  # once that log crosses the inline bound. Realizes the agent-files layer;
  # Linux-gated like the other image checks.
  bash-output-tee-hook-baked-into-image =
    pkgs.runCommand "bash-output-tee-hook-baked-into-image" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        hook=${nonRustHarness.agentFiles}/home/agent/.claude/hooks/bash-output-tee.sh
        [ -x "$hook" ] || {
          echo "bash-output-tee.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.agentFiles}/home/agent/.claude/settings.json
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
        hook=${nonRustHarness.agentFiles}/home/agent/.claude/hooks/bash-output-summary.sh
        [ -x "$hook" ] || {
          echo "bash-output-summary.sh missing or not executable at $hook" >&2
          exit 1
        }
        settings=${nonRustHarness.agentFiles}/home/agent/.claude/settings.json
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
  # Realizes the default image; Linux-gated like the other image checks.
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
    touch $out
  '';

  # The driver-cache mountpoint (the Driver's declared session-state dir,
  # ADR 0009 -- /home/agent/.claude/projects for claude) must be baked into
  # the image owned by uid 1000, so podman reuses the existing directory
  # instead of fabricating root-owned parent dirs when the volume is mounted
  # (issue #447). The expected path is derived from
  # nonRustHarness.driverEntry rather than a literal, so this check tracks
  # whichever Driver the image is built with (issue #448).
  # fakeRootCommands' chown -R 1000:1000 home/agent records the ownership in
  # the top customisation layer (Layers[-1]), the same layer that
  # nix-var-owned-by-agent and nix-conf-in-image inspect.
  projects-mountpoint-baked =
    let
      relPath = nonRustHarness.driverEntry.sessionCacheDirRelative;
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
  # fakeRootCommands records ownership in the tar headers; --numeric-owner
  # surfaces the raw uid so the check does not depend on /etc/passwd names.
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
  # is driven by the image, not a runtime-only setting. Both sides of the
  # knob are asserted here; each harness's image is still extracted only
  # once (see box-runs-as-non-root on why repeat compressed-image reads are
  # expensive).
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

  # BASH_MAX_OUTPUT_LENGTH / MAX_MCP_OUTPUT_TOKENS -- see "Claude Code output
  # caps" in docs/reference.md for the values and rationale (issue #1987).
  output-cap-env-marker =
    pkgs.runCommand "output-cap-env-marker" { nativeBuildInputs = [ pkgs.jq ]; }
      ''
        mkdir off && tar -xf ${nonRustHarness.image} -C off
        cfg=$(jq -r '.[0].Config' off/manifest.json)
        jq -e '.config.Env | any(. == "BASH_MAX_OUTPUT_LENGTH=8192")' "off/$cfg" >/dev/null || {
          echo "default harness must bake BASH_MAX_OUTPUT_LENGTH=8192" >&2
          exit 1
        }
        jq -e '.config.Env | any(. == "MAX_MCP_OUTPUT_TOKENS=2000")' "off/$cfg" >/dev/null || {
          echo "default harness must bake MAX_MCP_OUTPUT_TOKENS=2000" >&2
          exit 1
        }
        touch $out
      '';

  # /nix/store itself (not its existing contents) must become agent-writable
  # -- non-recursively, so baked paths stay root-owned -- only when
  # nixStoreWritable is opted in; the default image must never show uid 1000
  # ownership on it (absent from the top layer entirely, or present at its
  # pre-existing owner -- either reads as "not chowned to the agent").
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

  # fj (forgejo-cli) is baked into the image only for a forgejo-backend
  # Consumer (issue #1963), never for a github-backend one, so a
  # github-backend image never carries an unused CLI. Realizes both
  # harnesses' agentEnv, so it's Linux-gated like the other image checks.
  forgejo-cli-baked-only-for-forgejo-backend =
    pkgs.runCommand "forgejo-cli-baked-only-for-forgejo-backend" { }
      ''
        test -x ${forgejoHarness.agentEnv}/bin/fj || {
          echo "fj missing from forgejo-backend image" >&2
          exit 1
        }
        ! test -e ${nonRustHarness.agentEnv}/bin/fj || {
          echo "fj leaked into non-forgejo image" >&2
          exit 1
        }
        touch $out
      '';

  # extraClosures derivations must be physically present in the image
  # contents -- contents=[...]++extraClosures pulls the closure into the
  # image's store layers the same way agentEnv/agentFiles do. Listing (not
  # extracting) each already-extracted layer once is cheap; only the initial
  # compressed-image read is expensive (see box-runs-as-non-root).
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
