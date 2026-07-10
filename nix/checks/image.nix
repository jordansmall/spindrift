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
    promptHarness
    batsHarness
    skillsHarness
    nixStoreWritableHarness
    extraClosuresHarness
    ;
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

    # The reviewer-only mirror.
    reviewer_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${reviewerOnlyHarness.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-reviewer' <<<"$reviewer_line" \
      || { echo "reviewer-only harness missing reviewer model in baked template" >&2; exit 1; }
    ! grep -q '"scout"' <<<"$reviewer_line" \
      || { echo "reviewer-only harness unexpectedly bakes a scout entry" >&2; exit 1; }

    # The filer-only mirror (opt-in, default empty — issue #393): composed
    # independently like scout/reviewer, no scout/reviewer keys alongside it.
    filer_line=$(grep '^AGENTS_JSON_TEMPLATE=' ${filerOnlyHarness.agentFiles}/agent/entrypoint.sh)
    grep -q 'solo-filer' <<<"$filer_line" \
      || { echo "filer-only harness missing filer model in baked template" >&2; exit 1; }
    ! grep -q '"scout"' <<<"$filer_line" \
      || { echo "filer-only harness unexpectedly bakes a scout entry" >&2; exit 1; }
    ! grep -q '"reviewer"' <<<"$filer_line" \
      || { echo "filer-only harness unexpectedly bakes a reviewer entry" >&2; exit 1; }

    touch $out
  '';

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
    # WATCH CI block only ever reaches it via injection (issue #455) — proof
    # the baked image, not just the eval-only promptDir, carries it.
    grep -q 'statusCheckRollup' \
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
  # already must supply filer-prompt.md.
  fragments-baked-into-image = pkgs.runCommand "fragments-baked-into-image" { } ''
    for f in skill-preamble caveman-default file-issues auto-format auto-lint ci-failure; do
      diff ${../../templates/default/prompts/fragments}/"$f".md \
        ${batsHarness.agentFiles}/agent/prompts/fragments/"$f".md
    done
    touch $out
  '';

  # The idempotency check (issue #420) hinges on the entrypoint's marker
  # literal matching the one lib/mkHarness.nix slices the contract on; each is
  # a hardcoded literal in its own language, with nothing else forcing them to
  # agree, so a one-sided edit would silently break injection or duplicate
  # the contract on every run. Compared as plain text (no eval) so this stays
  # cheap and catches the drift at the source-literal level.
  outcome-contract-marker-parity = pkgs.runCommand "outcome-contract-marker-parity" { } ''
    grep -qF 'outcomeContractMarker = "# LAND THE CHANGE";' ${../../lib/mkHarness.nix}
    grep -qF 'OUTCOME_CONTRACT_MARKER="# LAND THE CHANGE"' ${../../agent/entrypoint.sh}
    touch $out
  '';

  # Same drift guard, for the COMMS and CHECK/COMMIT markers (issue #455).
  comms-check-contract-marker-parity = pkgs.runCommand "comms-check-contract-marker-parity" { } ''
    grep -qF 'commsMarker = "# COMMS";' ${../../lib/mkHarness.nix}
    grep -qF 'COMMS_CONTRACT_MARKER="# COMMS"' ${../../agent/entrypoint.sh}
    grep -qF 'checkMarker = "# CHECK";' ${../../lib/mkHarness.nix}
    grep -qF 'CHECK_CONTRACT_MARKER="# CHECK"' ${../../agent/entrypoint.sh}
    touch $out
  '';

  # Skills configured at build time must land in the agent-files layer at the
  # Driver's declared skills dir (ADR 0009) so the Box is self-contained.
  # Derives the expected path from skillsHarness.driverEntry rather than a
  # literal, so the check tracks whichever Driver the image is built with
  # (issue #448). Realizes the agent-files layer; Linux-gated like the other
  # image checks.
  skills-baked-into-image = pkgs.runCommand "skills-baked-into-image" { } ''
    grep -q 'BAKED-SKILL-MARKER' \
      ${skillsHarness.agentFiles}/home/agent/${skillsHarness.driverEntry.skillsDirRelative}/baked-skill.md
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
