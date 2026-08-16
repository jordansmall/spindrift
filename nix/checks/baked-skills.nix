# Baked-skill name list drift guards (issue #2532): lib/baked-skills.nix is
# the single source the skill-baked probe list, driver-exec flags, and
# promptassembly Env fields/Gates map render from -- these guards assert
# each committed generated span still matches lib/renderers.nix's
# renderBakedSkill* output, and that adding a row to the list flows through
# every renderer without touching any consumer file.
{ pkgs, launcherGoModules, ... }:
let
  renderers = import ../../lib/renderers.nix;
  bakedSkills = import ../../lib/baked-skills.nix;
  inherit (import ../../lib/builtins-compat.nix) escapeRegex;

  # Isolates the text strictly between a literal begin/end marker line pair,
  # mirroring nix/checks/schema-drift.nix's assertDefaultModelsDocOk (which
  # itself mirrors nix/regen.nix's write_between) so this guard can never
  # drift from what write_between actually replaces. escapeRegex guards
  # `builtins.split`, which reads its pattern arg as an ERE, not a literal --
  # a future marker containing a regex metacharacter (`(`, `+`, `[`, ...)
  # would otherwise silently split on the wrong text instead of the literal
  # marker line.
  between =
    {
      src,
      file,
      begin,
      end,
    }:
    let
      beginMarker = escapeRegex (begin + "\n");
      afterBegin =
        let
          parts = builtins.split beginMarker src;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 2
        else
          throw "${file}: BEGIN marker not found: ${begin}";
      committed =
        let
          parts = builtins.split (escapeRegex end) afterBegin;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 0
        else
          throw "${file}: END marker not found: ${end}";
    in
    committed;

  assertSpanOk =
    {
      file,
      begin,
      end,
      generated,
    }:
    let
      committed = between {
        src = builtins.readFile file;
        file = toString file;
        inherit begin end;
      };
      inherit (pkgs.lib) assertMsg;
    in
    assert assertMsg (committed == generated) ''
      ${toString file} generated skill-baked span (between "${begin}" / "${end}") is out of sync with lib/baked-skills.nix -- regenerate it with `nix run .#regen`
        got:  ${committed}
        want: ${generated}'';
    true;

  # The awk technique that replaces the text strictly between (and
  # preserving) a literal begin/end marker line pair with the contents of a
  # raw file -- shared, as a single bash function definition interpolated
  # into every runCommand script below that needs it, so
  # assertGoSpanGofmtOk's single-span reconstruction and
  # baked-skills-add-row-guard's multi-file, multi-span reconstruction can't
  # drift into two different splice implementations.
  spliceShellFn = ''
    splice() {
      local committed="$1" beginMarker="$2" endMarker="$3" rawfile="$4" outfile="$5"
      awk -v begin="$beginMarker" -v end="$endMarker" -v rawfile="$rawfile" '
        BEGIN { while ((getline line < rawfile) > 0) content = content line "\n" }
        $0 == begin { print; printf "%s", content; skip=1; next }
        $0 == end { skip=0 }
        skip { next }
        { print }
      ' "$committed" > "$outfile"
    }
  '';

  # Two of the five generated spans (the assembleprompt_cmd.go Env{} literal
  # assignments and the env.go struct fields) sit inside a Go struct
  # literal/struct type whose contiguous field block `gofmt -w` column-
  # aligns (colons, types) across the *whole* run, not just the inserted
  # span -- the same reason renderSchemaConfigGo/renderBackendRegistryGo's
  # own drift checks (above) gofmt-normalize before comparing instead of
  # diffing the raw renderer string directly. This mirrors that: reconstruct
  # the real file with the span replaced by the *raw* (unaligned) renderer
  # output, `gofmt -w` the whole reconstruction (exactly what `nix run
  # .#regen`'s own write_between + gofmt -w pairing does), and diff against
  # the real committed file.
  assertGoSpanGofmtOk =
    {
      name,
      file,
      begin,
      end,
      generated,
    }:
    let
      raw = pkgs.writeText "${name}.raw" generated;
      # The awk reconstruction below only ever *replaces* a span it finds
      # between the begin/end marker lines -- if a marker line is missing
      # entirely, awk just falls through printing the committed file
      # unchanged, so reconstructed.go ends up identical to the committed
      # file and the diff below reports no drift even though the generated
      # content never got inserted. Force the same eval-time marker-presence
      # check assertSpanOk gets via `between` (it throws when a marker is
      # absent) before ever constructing the runCommand derivation below, so
      # a missing marker fails loudly instead of silently passing.
      markersPresent = between {
        src = builtins.readFile file;
        file = toString file;
        inherit begin end;
      };
    in
    builtins.seq markersPresent (
      pkgs.runCommand name
        {
          nativeBuildInputs = [ pkgs.go ];
          committed = file;
          inherit raw;
          beginMarker = begin;
          endMarker = end;
        }
        ''
          ${spliceShellFn}
          splice "$committed" "$beginMarker" "$endMarker" "$raw" reconstructed.go
          gofmt -w reconstructed.go
          diff reconstructed.go "$committed" \
            || { echo "${toString file} generated skill-baked span (between \"${begin}\" / \"${end}\") is out of sync with lib/baked-skills.nix -- regenerate it with \`nix run .#regen\`" >&2; exit 1; }
          touch $out
        ''
    );
in
{
  baked-skills-probes-gen =
    assert assertSpanOk {
      file = ../../agent/entrypoint.sh;
      begin = "  # BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT";
      end = "  # END GENERATED SKILL-BAKED PROBES";
      generated = renderers.renderBakedSkillProbesShell bakedSkills;
    };
    pkgs.runCommand "baked-skills-probes-gen" { } "touch $out";

  baked-skills-flags-gen =
    assert assertSpanOk {
      file = ../../cmd/launcher/driver-exec/assembleprompt_cmd.go;
      begin = "\t// BEGIN GENERATED SKILL-BAKED FLAGS -- nix run .#regen -- DO NOT EDIT";
      end = "\t// END GENERATED SKILL-BAKED FLAGS";
      generated = renderers.renderBakedSkillFlagsGo bakedSkills;
    };
    pkgs.runCommand "baked-skills-flags-gen" { } "touch $out";

  baked-skills-env-assign-gen = assertGoSpanGofmtOk {
    name = "baked-skills-env-assign-gen";
    file = ../../cmd/launcher/driver-exec/assembleprompt_cmd.go;
    begin = "\t\t// BEGIN GENERATED SKILL-BAKED ENV -- nix run .#regen -- DO NOT EDIT";
    end = "\t\t// END GENERATED SKILL-BAKED ENV";
    generated = renderers.renderBakedSkillEnvAssignGo bakedSkills;
  };

  baked-skills-fields-gen = assertGoSpanGofmtOk {
    name = "baked-skills-fields-gen";
    file = ../../cmd/launcher/internal/promptassembly/env.go;
    begin = "\t// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT";
    end = "\t// END GENERATED SKILL-BAKED FIELDS";
    generated = renderers.renderBakedSkillFieldsGo bakedSkills;
  };

  baked-skills-gates-gen =
    assert assertSpanOk {
      file = ../../cmd/launcher/internal/promptassembly/gates.go;
      begin = "\t// BEGIN GENERATED SKILL-BAKED GATES -- nix run .#regen -- DO NOT EDIT";
      end = "\t// END GENERATED SKILL-BAKED GATES";
      generated = renderers.renderBakedSkillGatesGo bakedSkills;
    };
    pkgs.runCommand "baked-skills-gates-gen" { } "touch $out";

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
      extra = {
        name = "test-skill";
        goVar = "testSkillSkillBaked";
        field = "TestSkillSkillBaked";
        gate = "TEST_SKILL_BAKED";
      };
      withExtra = bakedSkills ++ [ extra ];

      probesBegin = "  # BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT";
      probesEnd = "  # END GENERATED SKILL-BAKED PROBES";
      flagsBegin = "\t// BEGIN GENERATED SKILL-BAKED FLAGS -- nix run .#regen -- DO NOT EDIT";
      flagsEnd = "\t// END GENERATED SKILL-BAKED FLAGS";
      envBegin = "\t\t// BEGIN GENERATED SKILL-BAKED ENV -- nix run .#regen -- DO NOT EDIT";
      envEnd = "\t\t// END GENERATED SKILL-BAKED ENV";
      fieldsBegin = "\t// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT";
      fieldsEnd = "\t// END GENERATED SKILL-BAKED FIELDS";
      gatesBegin = "\t// BEGIN GENERATED SKILL-BAKED GATES -- nix run .#regen -- DO NOT EDIT";
      gatesEnd = "\t// END GENERATED SKILL-BAKED GATES";

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
  # #2532 review) fixed: assertSpanOk already throws at eval time when a
  # begin/end marker line is missing (via `between`), and assertGoSpanGofmtOk
  # now forces that same `between` check before ever diffing its gofmt
  # reconstruction -- without this guard, a future edit could silently drop
  # that eval-time check again (exactly how the bug shipped undetected the
  # first time) and no check would complain. Mirrors
  # nix/checks/schema-drift.nix's default-models-doc-guard: build a synthetic
  # copy of each real committed file with its BEGIN marker line stripped,
  # run the real assertion function against it inside builtins.tryEval, and
  # assert it throws (!result.success) rather than silently passing.
  baked-skills-marker-guard =
    let
      inherit (pkgs.lib) assertMsg replaceStrings;

      probesBegin = "  # BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT";
      probesEnd = "  # END GENERATED SKILL-BAKED PROBES";
      probesSrc = builtins.readFile ../../agent/entrypoint.sh;
      probesSynthetic = pkgs.writeText "baked-skills-marker-guard-probes.sh" (
        replaceStrings [ (probesBegin + "\n") ] [ "" ] probesSrc
      );
      spanResult = builtins.tryEval (assertSpanOk {
        file = probesSynthetic;
        begin = probesBegin;
        end = probesEnd;
        generated = renderers.renderBakedSkillProbesShell bakedSkills;
      });

      fieldsBegin = "\t// BEGIN GENERATED SKILL-BAKED FIELDS -- nix run .#regen -- DO NOT EDIT";
      fieldsEnd = "\t// END GENERATED SKILL-BAKED FIELDS";
      fieldsSrc = builtins.readFile ../../cmd/launcher/internal/promptassembly/env.go;
      fieldsSynthetic = pkgs.writeText "baked-skills-marker-guard-env.go" (
        replaceStrings [ (fieldsBegin + "\n") ] [ "" ] fieldsSrc
      );
      goSpanResult = builtins.tryEval (assertGoSpanGofmtOk {
        name = "baked-skills-marker-guard-fields-gen";
        file = fieldsSynthetic;
        begin = fieldsBegin;
        end = fieldsEnd;
        generated = renderers.renderBakedSkillFieldsGo bakedSkills;
      });
    in
    assert assertMsg (!spanResult.success)
      "baked-skills-marker-guard: expected assertSpanOk to reject a synthetic file whose BEGIN GENERATED SKILL-BAKED PROBES marker is missing, but it evaluated successfully";
    assert assertMsg (!goSpanResult.success)
      "baked-skills-marker-guard: expected assertGoSpanGofmtOk to reject a synthetic file whose BEGIN GENERATED SKILL-BAKED FIELDS marker is missing, but it evaluated successfully";
    pkgs.runCommand "baked-skills-marker-guard" { } "touch $out";
}
