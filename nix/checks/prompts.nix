# Prompt/outcome-contract behavior: rendering the configured prompt, and the
# SPINDRIFT_OUTCOME contract injection/idempotency rules (issue #419).
{ pkgs, fixtures, ... }:
let
  inherit (fixtures)
    promptHarness
    fixPromptHarness
    researchPromptHarness
    batsHarness
    ;

  # lib/fragments.nix is the gate registry's source of truth (issue #959):
  # derive the fragment-gate-parity gate lists from it instead of a
  # hand-maintained bash `for gate in ...` list that silently drifts when a
  # row is added (issue #959, following the registry #622 -> parity #689).
  fragmentRows = import ../../lib/fragments.nix;
  allGates = map (row: row.gate) fragmentRows;
  # The registry carries no field marking a row computed vs. knob-gated (see
  # lib/fragments.nix:8-15's prose split), so name the knob-gated trio here
  # rather than add a field just for this check. Only computedGates gets the
  # entrypoint.sh assertion below -- a knob-gated gate names a launcher env
  # var directly and is never declared via `local` in entrypoint.sh.
  knobGates = [
    "AUTO_FORMAT"
    "AUTO_LINT"
    "CI_FAILURE_SUMMARY"
  ];
  computedGates =
    assert pkgs.lib.assertMsg (pkgs.lib.all (g: builtins.elem g allGates)
      knobGates
    ) "nix/checks/prompts.nix: knobGates names a gate lib/fragments.nix's registry no longer has";
    builtins.filter (g: !(builtins.elem g knobGates)) allGates;

  # The rendered CHECK section, sliced once here rather than three times
  # across the never-background/vanished-marker/git-add checks below (issue
  # #781) -- a marker rename only needs updating in one place, and the three
  # checks below just grep the shared output.
  checkSectionSlices = pkgs.runCommand "check-section-slices" { } ''
    mkdir -p $out
    awk '/^# CHECK$/{f=1} /^# REVIEW$/{exit} f' \
      ${batsHarness.promptDir}/issue-prompt.md > $out/issue-check.txt
  '';
in
{
  # The configured `prompt` is rendered to a store-path directory and,
  # by default, baked into the image (see agentFiles) rather than
  # mounted — `run` only bind-mounts a dir under the
  # SPINDRIFT_PROMPT_DIR override. Eval/native only (the rendered
  # prompt dir is a host store path; the image bake is checked
  # Linux-side by prompt-baked-into-image below).
  # The conditional prompt mount is handled by the Go launcher binary,
  # so the bats suite verifies runtime behaviour rather than grepping
  # the wrapper's source.
  mkharness-prompt = pkgs.runCommand "mkharness-prompt" { } ''
    # The Consumer's prompt text is what lands in the rendered file.
    grep -q 'CONFIGURED-PROMPT-MARKER' \
      ${promptHarness.promptDir}/issue-prompt.md
    touch $out
  '';

  # A Consumer `prompt` that drops the SPINDRIFT_OUTCOME contract must still
  # ship an agent that emits the outcome line, so the launcher can learn the
  # PR (issue #419) — the harness appends the canonical contract exactly once.
  mkharness-prompt-outcome-injected = pkgs.runCommand "mkharness-prompt-outcome-injected" { } ''
    count=$(grep -c '# LAND THE CHANGE' ${promptHarness.promptDir}/issue-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the outcome contract injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  # The default prompt already contains the contract, so injection must be a
  # no-op: no duplication (issue #419).
  mkharness-prompt-outcome-not-duplicated =
    pkgs.runCommand "mkharness-prompt-outcome-not-duplicated" { }
      ''
        count=$(grep -c '# LAND THE CHANGE' ${batsHarness.promptDir}/issue-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the default prompt's outcome contract to stay single, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # The default box's rendered prompt must be byte-identical to the template
  # on disk — injection must not touch a prompt that already has the
  # contract (issue #419).
  mkharness-prompt-outcome-default-unchanged =
    pkgs.runCommand "mkharness-prompt-outcome-default-unchanged" { }
      ''
        diff ${../../templates/default/prompts/issue-prompt.md} ${batsHarness.promptDir}/issue-prompt.md
        touch $out
      '';

  # The block injected into a prompt lacking the contract must be
  # byte-identical to the default prompt's own contract section — both are
  # sliced from the same marker in the same source file, so they cannot
  # drift apart (issue #419).
  mkharness-prompt-outcome-no-drift = pkgs.runCommand "mkharness-prompt-outcome-no-drift" { } ''
    awk '/# LAND THE CHANGE/{f=1} f' ${promptHarness.promptDir}/issue-prompt.md > injected-contract.txt
    diff ${batsHarness.outcomeContractFile} injected-contract.txt
    touch $out
  '';

  # The no-drift check above only proves the injected block matches the
  # *same-source* contract slice -- it never asserts the slice says the right
  # thing. A source regression from `landing=` back to the pre-#638 `pr=`
  # grammar would still pass that diff, since both sides would drift
  # together. Pin the literal token directly (issue #654). Anchor the token
  # to the SPINDRIFT_OUTCOME line itself (not `^`, since the CODE_FORGE=git
  # example line is indented inside a fenced code block) -- an unanchored
  # grep would still pass if the real outcome line regressed to `pr=` while
  # some unrelated prose in the slice happened to mention "landing="
  # (issue #886).
  #
  # A single `grep -q` only proves *at least one* SPINDRIFT_OUTCOME example
  # line kept `landing=` -- a partial regression, where only one of several
  # example lines reverts to `pr=`, still passes because the surviving lines
  # mask it. Require every SPINDRIFT_OUTCOME line to carry `landing=`: count
  # the lines missing it and fail the build if that count isn't zero (issue
  # #887). A bare `! pipeline` won't do here -- `set -e` explicitly exempts
  # negated commands, so a failing assertion silently wouldn't stop the build.
  mkharness-prompt-outcome-contract-has-landing-token =
    pkgs.runCommand "mkharness-prompt-outcome-contract-has-landing-token" { }
      ''
        # Floor guard: catches the degenerate case where every SPINDRIFT_OUTCOME
        # line -- and thus landing= itself -- vanishes from the contract, which
        # the per-line count below would otherwise wave through as 0 missing.
        grep -qE 'SPINDRIFT_OUTCOME.*landing=' ${batsHarness.outcomeContractFile}
        missing=$(grep 'SPINDRIFT_OUTCOME' ${batsHarness.outcomeContractFile} | grep -vc 'landing=' || true)
        [ "$missing" -eq 0 ] || {
          echo "expected every SPINDRIFT_OUTCOME line to carry landing=, $missing did not" >&2
          exit 1
        }
        touch $out
      '';

  # The #1582 dogfood run printed SPINDRIFT_OUTCOME backtick-wrapped, and the
  # extractor's anchored grep missed it -- the contract only ever *showed* the
  # line inside a fenced example, never told the driver its own output must be
  # raw text (issue #1612). Pin the explicit instruction adjacent to "print
  # exactly one line as your final output" so a future edit can't drop it or
  # relocate it away from that instruction. -z/-P with the (?s) modifier lets
  # "." span the line break the wording wraps across, so the check still
  # matches regardless of exactly where the prose wraps. The {0,60} window is
  # sized for "and stop —"/"—" separators plus one wrapped line (the widest
  # gap the current wording has) -- widen it if a future rewrap pushes the
  # phrase further from the instruction.
  mkharness-prompt-outcome-contract-raw-text =
    pkgs.runCommand "mkharness-prompt-outcome-contract-raw-text" { }
      ''
        grep -Pzoq '(?is)final output.{0,60}raw plain text' ${batsHarness.outcomeContractFile}
        touch $out
      '';

  # fix-prompt.md's default template carries only its fix-specific preamble
  # (issue #455): the rendered prompt must still gain the COMMS, CHECK/COMMIT,
  # and outcome-contract blocks, each exactly once, mirroring the issue
  # prompt's own guard above.
  mkharness-prompt-fix-comms-injected = pkgs.runCommand "mkharness-prompt-fix-comms-injected" { } ''
    count=$(grep -c '# COMMS' ${batsHarness.promptDir}/fix-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the fix prompt's COMMS block injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  mkharness-prompt-fix-check-injected = pkgs.runCommand "mkharness-prompt-fix-check-injected" { } ''
    count=$(grep -c '# CHECK' ${batsHarness.promptDir}/fix-prompt.md)
    [ "$count" -eq 1 ] || {
      echo "expected the fix prompt's CHECK/COMMIT block injected exactly once, got $count" >&2
      exit 1
    }
    touch $out
  '';

  mkharness-prompt-fix-outcome-injected =
    pkgs.runCommand "mkharness-prompt-fix-outcome-injected" { }
      ''
        count=$(grep -c '# LAND THE CHANGE' ${batsHarness.promptDir}/fix-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the fix prompt's outcome contract injected exactly once, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # A Consumer fixPrompt that carries only a fix-specific preamble — no
  # shared-block markers at all — must still gain all three, in COMMS, CHECK,
  # outcome-contract order, the same #420 runtime-override parity the issue
  # prompt already has (proven at the Nix layer here; agent/entrypoint.sh's
  # own runtime injection is covered by tests/entrypoint-outcome-contract.bats).
  mkharness-prompt-fix-consumer-override-injected =
    pkgs.runCommand "mkharness-prompt-fix-consumer-override-injected" { }
      ''
        grep -q 'CONFIGURED-FIX-PROMPT-MARKER' ${fixPromptHarness.promptDir}/fix-prompt.md
        [ "$(grep -c '# COMMS' ${fixPromptHarness.promptDir}/fix-prompt.md)" -eq 1 ]
        [ "$(grep -c '# CHECK' ${fixPromptHarness.promptDir}/fix-prompt.md)" -eq 1 ]
        [ "$(grep -c '# LAND THE CHANGE' ${fixPromptHarness.promptDir}/fix-prompt.md)" -eq 1 ]
        marker_line=$(grep -n 'CONFIGURED-FIX-PROMPT-MARKER' ${fixPromptHarness.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        comms_line=$(grep -n '# COMMS' ${fixPromptHarness.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        check_line=$(grep -n '# CHECK' ${fixPromptHarness.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        outcome_line=$(grep -n '# LAND THE CHANGE' ${fixPromptHarness.promptDir}/fix-prompt.md | head -1 | cut -d: -f1)
        [ "$marker_line" -lt "$comms_line" ]
        [ "$comms_line" -lt "$check_line" ]
        [ "$check_line" -lt "$outcome_line" ]
        touch $out
      '';

  # The injected COMMS and CHECK/COMMIT blocks must be byte-identical to the
  # canonical sections mkHarness slices them from — same source, same bytes,
  # so fix-prompt.md and issue-prompt.md cannot drift apart (issue #455,
  # mirrors mkharness-prompt-outcome-no-drift above).
  mkharness-prompt-fix-comms-no-drift = pkgs.runCommand "mkharness-prompt-fix-comms-no-drift" { } ''
    awk '/^# COMMS$/{f=1} /^# CHECK$/{exit} f' ${fixPromptHarness.promptDir}/fix-prompt.md > injected-comms.txt
    diff ${batsHarness.commsContractFile} injected-comms.txt
    touch $out
  '';

  mkharness-prompt-fix-check-no-drift = pkgs.runCommand "mkharness-prompt-fix-check-no-drift" { } ''
    awk '/^# CHECK$/{f=1} /^# LAND THE CHANGE$/{exit} f' ${fixPromptHarness.promptDir}/fix-prompt.md > injected-check.txt
    diff ${batsHarness.checkContractFile} injected-check.txt
    touch $out
  '';

  # The CHECK-phase never-background / emit-outcome guardrail (issue #592)
  # generalizes WATCH CI's rule to the CHECK phase's own blocking gates
  # (`nix build .#checks-inbox`, test suites). Written once in
  # issue-prompt.md's CHECK section and inherited by fix-prompt.md through
  # the CHECK block injection above. Both greps are scoped to issue-prompt's
  # CHECK section itself (not the whole file) -- WATCH CI carries the same
  # "never background it" phrase further down, so an unscoped grep would
  # keep passing even if the #592 CHECK paragraph were deleted. Fix-prompt
  # side is covered by mkharness-prompt-fix-check-no-drift's byte-for-byte
  # diff, not re-pinned here (issue #1009).
  mkharness-prompt-check-never-background =
    pkgs.runCommand "mkharness-prompt-check-never-background" { }
      ''
        grep -q 'never background it' ${checkSectionSlices}/issue-check.txt
        grep -q 'SPINDRIFT_OUTCOME' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # The defensive fallback for an agent that backgrounds a check gate anyway
  # (issue #713): a build killed outright (OOM, SIGKILL) never writes the
  # exit marker a background+poll loop waits on, so the wait must be bounded
  # and a vanished marker treated as failure, not still-pending. Same
  # CHECK-section scoping as the never-background check above. Fix-prompt
  # side is covered by mkharness-prompt-fix-check-no-drift's byte-for-byte
  # diff, not re-pinned here (issue #725).
  mkharness-prompt-check-vanished-marker-is-failure =
    pkgs.runCommand "mkharness-prompt-check-vanished-marker-is-failure" { }
      ''
        grep -qi 'vanished' ${checkSectionSlices}/issue-check.txt
        grep -qi 'exit marker' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  # Nix flakes only evaluate git-tracked files (issue #714): an agent that
  # creates a new file and runs `nix build` before staging it hits a
  # spurious "not tracked by Git" failure and burns a checks cycle. Same
  # CHECK-section scoping as the never-background/vanished-marker checks
  # above. Fix-prompt side is covered by mkharness-prompt-fix-check-no-drift's
  # byte-for-byte diff, not re-pinned here (issue #1009).
  mkharness-prompt-check-git-add-before-nix-build =
    pkgs.runCommand "mkharness-prompt-check-git-add-before-nix-build" { }
      ''
        grep -qi 'git add' ${checkSectionSlices}/issue-check.txt
        grep -qi 'tracked by' ${checkSectionSlices}/issue-check.txt
        touch $out
      '';

  mkharness-prompt-fix-outcome-no-drift =
    pkgs.runCommand "mkharness-prompt-fix-outcome-no-drift" { }
      ''
        awk '/# LAND THE CHANGE/{f=1} f' ${fixPromptHarness.promptDir}/fix-prompt.md > injected-contract.txt
        diff ${batsHarness.outcomeContractFile} injected-contract.txt
        touch $out
      '';

  # The research dispatch kind's own outcome contract (issue #640): a
  # Consumer researchPrompt that drops "# POST THE VERDICT" must still ship
  # an agent that posts the verdict comment and emits the outcome line --
  # the harness appends the canonical contract exactly once.
  mkharness-prompt-research-outcome-injected =
    pkgs.runCommand "mkharness-prompt-research-outcome-injected" { }
      ''
        count=$(grep -c '# POST THE VERDICT' ${batsHarness.promptDir}/research-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the research prompt's outcome contract injected exactly once, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # The default research prompt already contains the contract, so injection
  # must be a no-op: no duplication (mirrors mkharness-prompt-outcome-not-duplicated).
  mkharness-prompt-research-outcome-not-duplicated =
    pkgs.runCommand "mkharness-prompt-research-outcome-not-duplicated" { }
      ''
        count=$(grep -c '# POST THE VERDICT' ${batsHarness.promptDir}/research-prompt.md)
        [ "$count" -eq 1 ] || {
          echo "expected the default research prompt's outcome contract to stay single, got $count" >&2
          exit 1
        }
        touch $out
      '';

  # The default box's rendered research prompt must be byte-identical to the
  # template on disk -- injection must not touch a prompt that already has
  # the contract (mirrors mkharness-prompt-outcome-default-unchanged).
  mkharness-prompt-research-outcome-default-unchanged =
    pkgs.runCommand "mkharness-prompt-research-outcome-default-unchanged" { }
      ''
        diff ${../../templates/default/prompts/research-prompt.md} ${batsHarness.promptDir}/research-prompt.md
        touch $out
      '';

  # The block injected into a research prompt lacking the contract must be
  # byte-identical to the default research prompt's own contract section --
  # both sliced from the same marker in the same source file (issue #640,
  # mirrors mkharness-prompt-outcome-no-drift).
  mkharness-prompt-research-outcome-no-drift =
    pkgs.runCommand "mkharness-prompt-research-outcome-no-drift" { }
      ''
        awk '/# POST THE VERDICT/{f=1} f' ${researchPromptHarness.promptDir}/research-prompt.md > injected-contract.txt
        diff ${batsHarness.researchOutcomeContractFile} injected-contract.txt
        touch $out
      '';

  # Same gap as mkharness-prompt-outcome-contract-has-landing-token, for the
  # research kind's own contract (issue #654), including the same
  # SPINDRIFT_OUTCOME anchoring fix (issue #886) and the partial-revert
  # strengthening (issue #887).
  mkharness-prompt-research-outcome-contract-has-landing-token =
    pkgs.runCommand "mkharness-prompt-research-outcome-contract-has-landing-token" { }
      ''
        # Floor guard, same reasoning as the issue-side check above.
        grep -qE 'SPINDRIFT_OUTCOME.*landing=' ${batsHarness.researchOutcomeContractFile}
        missing=$(grep 'SPINDRIFT_OUTCOME' ${batsHarness.researchOutcomeContractFile} | grep -vc 'landing=' || true)
        [ "$missing" -eq 0 ] || {
          echo "expected every SPINDRIFT_OUTCOME line to carry landing=, $missing did not" >&2
          exit 1
        }
        touch $out
      '';

  # Same raw-text pin as mkharness-prompt-outcome-contract-raw-text, for the
  # research kind's own contract (issue #1612).
  mkharness-prompt-research-outcome-contract-raw-text =
    pkgs.runCommand "mkharness-prompt-research-outcome-contract-raw-text" { }
      ''
        grep -Pzoq '(?is)final output.{0,60}raw plain text' ${batsHarness.researchOutcomeContractFile}
        touch $out
      '';

  # A Consumer researchPrompt carrying only a research-specific preamble --
  # no "# POST THE VERDICT" marker at all -- must still gain the contract,
  # and survive the round trip byte-identical to what a runtime
  # SPINDRIFT_PROMPT_DIR override receives (issue #640, mirrors
  # mkharness-prompt-fix-consumer-override-injected; agent/entrypoint.sh's
  # own runtime injection is covered by tests/entrypoint-research-kind.bats).
  mkharness-prompt-research-consumer-override-injected =
    pkgs.runCommand "mkharness-prompt-research-consumer-override-injected" { }
      ''
        grep -q 'CONFIGURED-RESEARCH-PROMPT-MARKER' ${researchPromptHarness.promptDir}/research-prompt.md
        [ "$(grep -c '# POST THE VERDICT' ${researchPromptHarness.promptDir}/research-prompt.md)" -eq 1 ]
        marker_line=$(grep -n 'CONFIGURED-RESEARCH-PROMPT-MARKER' ${researchPromptHarness.promptDir}/research-prompt.md | head -1 | cut -d: -f1)
        contract_line=$(grep -n '# POST THE VERDICT' ${researchPromptHarness.promptDir}/research-prompt.md | head -1 | cut -d: -f1)
        [ "$marker_line" -lt "$contract_line" ]
        touch $out
      '';

  # Grep pin (issue #455 acceptance criteria): the WATCH CI GraphQL query
  # literal must appear in exactly one prompt *source* file on disk.
  # fix-prompt.md's CONTEXT section legitimately references the unrelated
  # `statusCheckRollup` JSON field name via `gh pr view --json`, so the query
  # body itself -- distinctive to the shared WATCH CI block -- is the pin, not
  # the field name alone. fix-prompt.md gets the WATCH CI block by injection
  # now, never as hand-copied source text -- a regression here means someone
  # pasted the block back in.
  prompt-source-statusCheckRollup-query-appears-once =
    pkgs.runCommand "prompt-source-statusCheckRollup-query-appears-once" { }
      ''
        count=$(grep -rlF 'query($owner:String!' ${../../templates/default/prompts} | wc -l)
        [ "$count" -eq 1 ] || {
          echo "expected the WATCH CI GraphQL query in exactly one prompt source file, got $count" >&2
          grep -rlF 'query($owner:String!' ${../../templates/default/prompts} >&2
          exit 1
        }
        touch $out
      '';

  # The Conditional fragment registry's computed-gate rows (lib/fragments.nix,
  # issue #622) name a bash variable entrypoint.sh's precompute block sets;
  # nothing else forces the two to agree, so a typo in either would leave the
  # fragment loop's `"${!_fgate}"` indirection silently reading an unset
  # variable -- the row just never renders, no error (issue #689). Same
  # drift-guard shape as outcome-contract-marker-parity in nix/checks/image.nix,
  # grep-based and eval-only so it belongs in checks-inbox, not the
  # image-realizing checks.
  #
  # The gate list is derived from the registry (allGates/computedGates above)
  # rather than hand-copied, so a new row is covered automatically (issue
  # #959; a hand-copied list had silently missed 3 of 9 rows). Every gate
  # gets the fragments.nix assertion; only computed gates get the
  # entrypoint.sh assertion, since the knob-gated trio is never declared via
  # `local` there by design (see the knobGates comment above).
  fragment-gate-parity = pkgs.runCommand "fragment-gate-parity" { } ''
    for gate in ${pkgs.lib.concatStringsSep " " allGates}; do
      grep -qF "gate = \"$gate\";" ${../../lib/fragments.nix}
    done
    for gate in ${pkgs.lib.concatStringsSep " " computedGates}; do
      grep -qF "local $gate=" ${../../agent/entrypoint.sh}
    done
    touch $out
  '';

  # Grep pin (issue #908 acceptance criteria): the filer's dedup step must
  # search open issues beyond the `agent-review-finding` label -- a
  # regression back to the old `--label agent-review-finding --state all`
  # query would silently stop catching human-filed/ready-for-agent/
  # /to-tickets duplicates. Neither pin above catches a *narrower* regression:
  # re-adding a `--label` flag to the `--state open` line itself (e.g.
  # `--label agent-review-finding --state open`) still contains the literal
  # substring `--state open` and never matches the old `--state all` string,
  # so both pins stay green while the dedup silently narrows back to only
  # `agent-review-finding`-labeled issues (issue #921). Extract the line
  # carrying `--state open` and count how many of its occurrences also carry
  # `--label` -- must be zero. All assertions below use the explicit
  # `[ "$n" -eq 0 ] || exit 1` shape, not a bare `! pipeline`, since `set -e`
  # exempts negated commands (issue #887).
  filer-prompt-dedup-searches-all-open-issues =
    pkgs.runCommand "filer-prompt-dedup-searches-all-open-issues" { }
      ''
        grep -q -- '--state open' ${../../templates/default/prompts/filer-prompt.md}
        old=$(grep -c -- '--label agent-review-finding --state all' \
          ${../../templates/default/prompts/filer-prompt.md} || true)
        [ "$old" -eq 0 ] || {
          echo "expected the old --label agent-review-finding --state all query gone, found $old occurrence(s)" >&2
          exit 1
        }
        bad=$(grep -- '--state open' ${../../templates/default/prompts/filer-prompt.md} \
          | grep -c -- '--label' || true)
        [ "$bad" -eq 0 ] || {
          echo "expected the --state open dedup search to carry no --label flag, $bad line(s) did" >&2
          exit 1
        }
        touch $out
      '';

  # Grep pin (issue #781 acceptance criteria): the CHECK-section awk slice
  # used by the never-background/vanished-marker/git-add checks above must
  # be defined once, not copy-pasted -- a marker rename applied to one copy
  # and forgotten in the others would leave those checks silently reading
  # stale content. Extended (issue #1154) to also pin the fix-prompt half
  # of the same slice pattern (`# LAND THE CHANGE` exit instead of
  # `# REVIEW`), used solely by mkharness-prompt-fix-check-no-drift above --
  # the original check only ever guarded the issue-prompt half.
  prompts-nix-check-section-awk-defined-once =
    pkgs.runCommand "prompts-nix-check-section-awk-defined-once" { }
      ''
        # Split so this line's own source text never contains the
        # contiguous target pattern -- else this check would count itself.
        half1='/^# CHECK$/{f=1}'
        half2=' /^# REVIEW$/{exit} f'
        count=$(grep -cF "$half1$half2" ${./prompts.nix})
        [ "$count" -le 1 ] || {
          echo "expected the CHECK-section awk slice defined at most once in prompts.nix, got $count" >&2
          exit 1
        }
        fix_half2=' /^# LAND THE CHANGE$/{exit} f'
        fix_count=$(grep -cF "$half1$fix_half2" ${./prompts.nix})
        [ "$fix_count" -le 1 ] || {
          echo "expected the fix-prompt CHECK-section awk slice defined at most once in prompts.nix, got $fix_count" >&2
          exit 1
        }
        touch $out
      '';

  # Grep pin (issue #908 acceptance criteria): the filer's dedup step must
  # also treat closed `agent-research-reject` issues -- a research pass's
  # deliberate false-positive/not-worth-doing/duplicate verdict -- as
  # suppressing matches, the same triage-decision class as a closed
  # `agent-review-finding`. Anchored to the full `--label ... --state closed`
  # search command, not the bare label token -- a bare-token match would
  # still pass if the closed-dedup search line lost the label while an
  # unrelated prose mention of `agent-research-reject` survived elsewhere in
  # the file (issue #922), the same class of regression #921 guards against
  # for the sibling `--state open` check above.
  filer-prompt-dedup-names-research-reject =
    pkgs.runCommand "filer-prompt-dedup-names-research-reject" { }
      ''
        grep -q -- '--label agent-research-reject --state closed' ${../../templates/default/prompts/filer-prompt.md}
        touch $out
      '';
}
