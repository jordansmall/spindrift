# Baked-skill end-to-end regression guards (issue #2532). The five per-span
# drift checks that used to live in this file (probes/flags/env-assign/
# fields/gates) are now generic lib/documented-facts.nix rows, checked by
# nix/checks/schema-drift.nix's documentedFactChecks (issue #2949) under the
# names baked-skills-probes-gen / baked-skills-flags-gen /
# baked-skills-env-assign-gen / baked-skills-fields-gen /
# baked-skills-gates-gen -- this file no longer duplicates that coverage.
# What remains here are the two guards that a generic per-row string-diff
# can't express:
#   - baked-skills-add-row-guard: proves a synthetic seventh
#     lib/baked-skills.nix row flows through every renderer end-to-end --
#     compiles Go, runs it, execs the reconstructed bash probes.
#   - baked-skills-marker-guard: proves a missing BEGIN marker makes the
#     checker throw, not silently pass.
{ pkgs, launcherGoModules, ... }:
let
  renderers = import ../../lib/renderers.nix;
  bakedSkills = import ../../lib/baked-skills.nix;
  # The shared marker-splice + drift-comparison implementation (issue #2949)
  # backing the two guards below -- also imported by nix/checks/schema-drift.nix,
  # so this file no longer hand-mirrors its own copy of the builtins.split-based
  # marker-splitting logic.
  documentedFactChecker = import ../../lib/documented-fact-checker.nix { inherit pkgs; };
  inherit (documentedFactChecker) spliceShellFn;

  # The registry rows for the five skill-baked spans (issue #2949) -- the two
  # guards below source their marker literals from here instead of
  # hand-maintaining a separate copy.
  documentedFacts = import ../../lib/documented-facts.nix { inherit (pkgs) lib; };
  rowByName =
    name:
    let
      matches = builtins.filter (r: r.name == name) documentedFacts;
    in
    if matches == [ ] then
      throw "nix/checks/baked-skills.nix: no documentedFacts row named \"${name}\" (lib/documented-facts.nix row name may have been renamed)"
    else
      builtins.head matches;
  probesRow = rowByName "baked-skills-probes-gen";
  flagsRow = rowByName "baked-skills-flags-gen";
  envRow = rowByName "baked-skills-env-assign-gen";
  fieldsRow = rowByName "baked-skills-fields-gen";
  gatesRow = rowByName "baked-skills-gates-gen";
in
{
  # Regression guard (issue #2532 AC2): adding a row to lib/baked-skills.nix
  # must flow through every renderer -- the probe line, the flag decl, the
  # Env-literal assignment, the struct field, and the gate assignment -- with
  # no edit to any consumer file. This is a genuine end-to-end guard, not a
  # string-diff against a renderer's raw output: it splices a synthetic
  # seventh row's generated spans into *copies of the real committed files*
  # (agent/entrypoint.sh, assembleprompt_cmd.go, env.go, gates.go), compiles
  # that reconstructed tree for real against the real vendored deps, then
  # actually runs it -- a Go test drives the CLI flag all the way through
  # promptassembly.Assemble's fragment-inclusion chain into an assembled
  # prompt, and a bash run of the reconstructed entrypoint.sh probe span
  # proves its skill-file presence check for real. Only ever fails if a
  # renderer's output stops type-checking, stops compiling, or the spliced
  # span's real runtime behavior diverges -- never merely because a renderer
  # stopped emitting some substring.
  baked-skills-add-row-guard =
    let
      inherit (pkgs.lib) removeSuffix;

      extra = {
        name = "test-skill";
        goVar = "testSkillSkillBaked";
        field = "TestSkillSkillBaked";
        gate = "TEST_SKILL_BAKED";
      };
      withExtra = bakedSkills ++ [ extra ];

      # spliceShellFn's `splice` bash function does an exact-line `awk $0 ==
      # begin` match against a bare line (no trailing newline), while rows
      # store beginMarker WITH a trailing "\n" (documentedFactChecker's
      # convention) -- same removeSuffix "\n" as nix/regen.nix's
      # write_between call sites.
      probesBegin = removeSuffix "\n" probesRow.beginMarker;
      probesEnd = probesRow.endMarker;
      flagsBegin = removeSuffix "\n" flagsRow.beginMarker;
      flagsEnd = flagsRow.endMarker;
      envBegin = removeSuffix "\n" envRow.beginMarker;
      envEnd = envRow.endMarker;
      fieldsBegin = removeSuffix "\n" fieldsRow.beginMarker;
      fieldsEnd = fieldsRow.endMarker;
      gatesBegin = removeSuffix "\n" gatesRow.beginMarker;
      gatesEnd = gatesRow.endMarker;

      probesRaw = pkgs.writeText "baked-skills-add-row-guard-probes.raw" (
        renderers.renderBakedSkillProbesShell withExtra
      );
      flagsRaw = pkgs.writeText "baked-skills-add-row-guard-flags.raw" (
        renderers.renderBakedSkillFlagsGo withExtra
      );
      envRaw = pkgs.writeText "baked-skills-add-row-guard-env.raw" (
        renderers.renderBakedSkillEnvAssignGo withExtra
      );
      fieldsRaw = pkgs.writeText "baked-skills-add-row-guard-fields.raw" (
        renderers.renderBakedSkillFieldsGo withExtra
      );
      gatesRaw = pkgs.writeText "baked-skills-add-row-guard-gates.raw" (
        renderers.renderBakedSkillGatesGo withExtra
      );

      # The one synthetic Go test file spliced into the reconstructed tree's
      # driver-exec package (same package as assembleprompt_cmd.go and its
      # own assembleprompt_cmd_test.go, whose coveredCellArgs/replaceArg
      # helpers this test reuses verbatim): drives runAssemblePrompt twice,
      # once with --test-skill-skill-baked=true and once =false (the real
      # CAVEMAN_BAKED gate forced off both times so only the injected
      # TEST_SKILL_BAKED row -- sharing CAVEMAN_BAKED's real fragment/var,
      # caveman-default.md/CAVEMAN_STEP, so its effect is actually observable
      # in the rendered prompt -- can put the fragment's text in the output),
      # and asserts the fragment's sentinel text appears only in the former.
      testFile = pkgs.writeText "bakedskillsaddrowguard_test.go" ''
        package main

        import (
        	"bytes"
        	"encoding/json"
        	"os"
        	"path/filepath"
        	"strings"
        	"testing"
        )

        // TestBakedSkillsAddRowGuard is spliced into a reconstructed, compiled
        // copy of the real launcher tree by nix/checks/baked-skills.nix's
        // baked-skills-add-row-guard check (issue #2532 AC2). It proves a
        // synthetic seventh lib/baked-skills.nix row (test-skill /
        // --test-skill-skill-baked / TestSkillSkillBaked / TEST_SKILL_BAKED),
        // spliced into the real FLAGS/ENV/FIELDS/GATES generated spans and
        // compiled for real, actually reaches promptassembly.Assemble's
        // fragment-inclusion chain end to end: CLI flag ->
        // *testSkillSkillBaked -> Env.TestSkillSkillBaked ->
        // Gates()["TEST_SKILL_BAKED"] -> registry gate lookup -> fragment
        // rendered into the assembled prompt -- not a string comparison
        // against a renderer's raw output.
        func TestBakedSkillsAddRowGuard(t *testing.T) {
        	registryBytes, err := os.ReadFile(registryPathForTest)
        	if err != nil {
        		t.Fatalf("read registry fixture: %v", err)
        	}
        	var rows []map[string]any
        	if err := json.Unmarshal(registryBytes, &rows); err != nil {
        		t.Fatalf("unmarshal registry fixture: %v", err)
        	}
        	// Appends a row sharing CAVEMAN_BAKED's real fragment/var
        	// (caveman-default.md / CAVEMAN_STEP) under the injected gate name
        	// -- reusing a real fragment/var pair (rather than a synthetic,
        	// unused one) means the injected gate's effect is actually
        	// observable in the final assembled prompt text, since Assemble
        	// only surfaces a row's rendered fragment into the prompt when the
        	// base template already references that row's var as a ''${VAR}
        	// placeholder.
        	rows = append(rows, map[string]any{
        		"gate":     "TEST_SKILL_BAKED",
        		"fragment": "caveman-default.md",
        		"var":      "CAVEMAN_STEP",
        	})
        	appendedRegistryBytes, err := json.Marshal(rows)
        	if err != nil {
        		t.Fatalf("marshal appended registry: %v", err)
        	}
        	appendedRegistryPath := filepath.Join(t.TempDir(), "registry-with-test-skill-row.json")
        	if err := os.WriteFile(appendedRegistryPath, appendedRegistryBytes, 0o644); err != nil {
        		t.Fatalf("write appended registry: %v", err)
        	}

        	// distinctiveSentinel is caveman-default.md's first sentence --
        	// unique enough that it never otherwise appears in the
        	// covered-cell prompt.
        	const distinctiveSentinel = "Default to the `/caveman` skill for all narration and prose output this run."

        	run := func(testSkillBaked bool) string {
        		promptOutput := filepath.Join(t.TempDir(), "prompt.txt")
        		agentsJSONOutput := filepath.Join(t.TempDir(), "agents.json")
        		handoffOutput := filepath.Join(t.TempDir(), "handoff.json")
        		args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
        		args = replaceArg(args, "--registry", appendedRegistryPath)
        		// Force the real CAVEMAN_BAKED gate off so only the injected
        		// TEST_SKILL_BAKED row (sharing
        		// caveman-default.md/CAVEMAN_STEP) can put the sentinel in
        		// the prompt.
        		args = replaceArg(args, "--caveman-skill-baked", "false")
        		if testSkillBaked {
        			args = replaceArg(args, "--test-skill-skill-baked", "true")
        		} else {
        			args = replaceArg(args, "--test-skill-skill-baked", "false")
        		}

        		var stdout bytes.Buffer
        		rc := runAssemblePrompt(args, &stdout)
        		if rc != 0 {
        			t.Fatalf("runAssemblePrompt(--test-skill-skill-baked=%v) exit = %d, want 0 (stdout=%q)", testSkillBaked, rc, stdout.String())
        		}
        		promptBytes, err := os.ReadFile(promptOutput)
        		if err != nil {
        			t.Fatalf("read prompt output: %v", err)
        		}
        		return string(promptBytes)
        	}

        	truePrompt := run(true)
        	if !strings.Contains(truePrompt, distinctiveSentinel) {
        		t.Errorf("--test-skill-skill-baked=true prompt missing the caveman-default.md sentinel %q -- the injected TEST_SKILL_BAKED gate never reached Assemble's fragment-inclusion chain", distinctiveSentinel)
        	}

        	falsePrompt := run(false)
        	if strings.Contains(falsePrompt, distinctiveSentinel) {
        		t.Errorf("--test-skill-skill-baked=false prompt contains the caveman-default.md sentinel %q, want it absent", distinctiveSentinel)
        	}
        }
      '';
    in
    pkgs.runCommand "baked-skills-add-row-guard"
      {
        nativeBuildInputs = [ pkgs.go ];
        committedEntrypoint = ../../agent/entrypoint.sh;
        launcherSrc = ../../cmd/launcher;
        promptsDir = ../../templates/default/prompts;
        vendorModules = launcherGoModules;
        inherit
          probesRaw
          flagsRaw
          envRaw
          fieldsRaw
          gatesRaw
          testFile
          probesBegin
          probesEnd
          flagsBegin
          flagsEnd
          envBegin
          envEnd
          fieldsBegin
          fieldsEnd
          gatesBegin
          gatesEnd
          ;
      }
      ''
        ${spliceShellFn}

        # Step 1+2: reconstruct the real committed files with the injected
        # row's generated spans spliced in, inside a real copy of the
        # cmd/launcher module tree, then actually compile it (mirrors
        # nix/checks/go.nix's launcher-go-vet/launcher-go-test vendor
        # pattern: GOPROXY=off against the real vendored deps).
        mkdir -p src/cmd
        cp -r "$launcherSrc" src/cmd/launcher
        cp -r "$promptsDir" ./templates-default-prompts
        mkdir -p src/templates/default
        cp -r ./templates-default-prompts src/templates/default/prompts
        chmod -R +w src
        cp -r "$vendorModules" src/cmd/launcher/vendor

        splice src/cmd/launcher/driver-exec/assembleprompt_cmd.go "$flagsBegin" "$flagsEnd" "$flagsRaw" assembleprompt_cmd.step1.go
        splice assembleprompt_cmd.step1.go "$envBegin" "$envEnd" "$envRaw" src/cmd/launcher/driver-exec/assembleprompt_cmd.go

        splice src/cmd/launcher/internal/promptassembly/env.go "$fieldsBegin" "$fieldsEnd" "$fieldsRaw" env.step1.go
        mv env.step1.go src/cmd/launcher/internal/promptassembly/env.go

        splice src/cmd/launcher/internal/promptassembly/gates.go "$gatesBegin" "$gatesEnd" "$gatesRaw" gates.step1.go
        mv gates.step1.go src/cmd/launcher/internal/promptassembly/gates.go

        gofmt -w \
          src/cmd/launcher/driver-exec/assembleprompt_cmd.go \
          src/cmd/launcher/internal/promptassembly/env.go \
          src/cmd/launcher/internal/promptassembly/gates.go

        cp "$testFile" src/cmd/launcher/driver-exec/bakedskillsaddrowguard_test.go

        export GOPROXY=off
        export GOFLAGS=-mod=vendor
        export GONOSUMCHECK='*'
        export GOMODCACHE="$TMPDIR/gomodcache"
        export GOCACHE="$TMPDIR/gocache"
        export CGO_ENABLED=0
        cd src/cmd/launcher
        go vet ./...
        go build ./...

        # Step 3: actually run the compiled reconstruction -- proves the
        # spliced-in flag/field/gate names carry a real value through
        # promptassembly.Assemble's fragment-inclusion chain, not just that
        # they type-check.
        go test ./driver-exec/... -run TestBakedSkillsAddRowGuard -v
        cd - > /dev/null

        # Step 4: reconstruct agent/entrypoint.sh's spliced probes block and
        # actually execute it, once with the injected row's skill file
        # present and once without, proving its real runtime behavior (not
        # just its rendered text).
        splice "$committedEntrypoint" "$probesBegin" "$probesEnd" "$probesRaw" reconstructed-entrypoint.sh
        awk -v begin="$probesBegin" -v end="$probesEnd" '
          $0 == begin { grab=1; next }
          $0 == end { grab=0 }
          grab { print }
        ' reconstructed-entrypoint.sh > probes-snippet.sh

        mkdir -p present-skills-dir/test-skill absent-skills-dir
        touch present-skills-dir/test-skill/SKILL.md

        check_probes() {
          local skills_dir="$1" want_present="$2" out
          out=$(bash -c '
            _ap_args=()
            DRIVER_SKILLS_DIR="$1"
            # shellcheck disable=SC1090
            source "$2"
            printf "%s\n" "''${_ap_args[@]}"
          ' _ "$skills_dir" "$PWD/probes-snippet.sh")
          if [ "$want_present" = "yes" ]; then
            if ! printf '%s\n' "$out" | grep -qx -- "--test-skill-skill-baked"; then
              echo "reconstructed entrypoint.sh probes span did not add --test-skill-skill-baked when $skills_dir/test-skill/SKILL.md exists" >&2
              exit 1
            fi
          else
            if printf '%s\n' "$out" | grep -qx -- "--test-skill-skill-baked"; then
              echo "reconstructed entrypoint.sh probes span added --test-skill-skill-baked when $skills_dir/test-skill/SKILL.md does not exist" >&2
              exit 1
            fi
          fi
        }
        check_probes "$PWD/present-skills-dir" yes
        check_probes "$PWD/absent-skills-dir" no

        touch $out
      '';

  # Regression guard for the marker-presence bug the two guards above (issue
  # #2532 review) fixed: assertMarkedBlockOk already throws at eval time when
  # a begin/end marker line is missing (via `splitMarkedBlock`), and
  # assertSplicedSpanOk forces that same check before ever diffing its
  # reconstruction -- without this guard, a future edit could silently drop
  # that eval-time check again (exactly how the bug shipped undetected the
  # first time) and no check would complain. Mirrors
  # nix/checks/schema-drift.nix's documented-fact-guard: build a synthetic
  # copy of each real committed file with its BEGIN marker line stripped,
  # run the real assertion function against it inside builtins.tryEval, and
  # assert it throws (!result.success) rather than silently passing.
  baked-skills-marker-guard =
    let
      inherit (pkgs.lib) assertMsg replaceStrings;

      probesSrc = builtins.readFile ../../agent/entrypoint.sh;
      probesSynthetic = pkgs.writeText "baked-skills-marker-guard-probes.sh" (
        replaceStrings [ probesRow.beginMarker ] [ "" ] probesSrc
      );
      spanResult = builtins.tryEval (
        documentedFactChecker.assertMarkedBlockOk {
          inherit (probesRow)
            blockName
            sourceDesc
            beginMarker
            endMarker
            docPath
            generated
            ;
          docSrc = builtins.readFile probesSynthetic;
        }
      );

      fieldsSrc = builtins.readFile ../../cmd/launcher/internal/promptassembly/env.go;
      fieldsSynthetic = pkgs.writeText "baked-skills-marker-guard-env.go" (
        replaceStrings [ fieldsRow.beginMarker ] [ "" ] fieldsSrc
      );
      goSpanResult = builtins.tryEval (
        documentedFactChecker.assertSplicedSpanOk {
          name = "baked-skills-marker-guard-fields-gen";
          file = fieldsSynthetic;
          inherit (fieldsRow)
            blockName
            sourceDesc
            beginMarker
            endMarker
            generated
            ;
          gofmt = true;
        }
      );
    in
    assert assertMsg (!spanResult.success)
      "baked-skills-marker-guard: expected assertMarkedBlockOk to reject a synthetic file whose BEGIN GENERATED SKILL-BAKED PROBES marker is missing, but it evaluated successfully";
    assert assertMsg (!goSpanResult.success)
      "baked-skills-marker-guard: expected assertSplicedSpanOk to reject a synthetic file whose BEGIN GENERATED SKILL-BAKED FIELDS marker is missing, but it evaluated successfully";
    pkgs.runCommand "baked-skills-marker-guard" { } "touch $out";

  # Regression guard (issue #2949 review): rowByName used to call bare
  # builtins.head on the filtered list, so a row name with no match (e.g.
  # after a future lib/documented-facts.nix row rename this file's
  # hand-typed names fall out of sync with) threw Nix's unhelpful "list is
  # empty" with no indication of which row it was looking for. Proves
  # rowByName now throws instead of returning, for an unknown name, the same
  # tryEval + assertMsg pattern as baked-skills-marker-guard above.
  baked-skills-row-by-name-guard =
    let
      inherit (pkgs.lib) assertMsg;
      result = builtins.tryEval (rowByName "no-such-row");
    in
    assert assertMsg (!result.success)
      "baked-skills-row-by-name-guard: expected rowByName to throw for an unknown row name, but it evaluated successfully";
    pkgs.runCommand "baked-skills-row-by-name-guard" { } "touch $out";
}
