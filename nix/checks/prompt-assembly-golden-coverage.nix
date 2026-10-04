# Every lib/fragments.nix fragment must be rendered by some prompt-assembly
# golden cell (its basename in a tests/testdata/prompt-assembly-golden/
# <cell>.fragments.txt sidecar, issue #3838) or sit on the allowlist below,
# so a new fragment cannot ship with no pinned rendering.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    escapeShellArg
    filter
    hasSuffix
    optionalString
    removeSuffix
    splitString
    unique
    ;

  goldenDir = ../../tests/testdata/prompt-assembly-golden;

  registry = unique (map (r: r.fragment) (import ../../lib/fragments.nix));

  sidecars = filter (hasSuffix ".fragments.txt") (builtins.attrNames (builtins.readDir goldenDir));

  readLines = path: filter (l: l != "") (splitString "\n" (builtins.readFile path));

  covered = unique (builtins.concatLists (map (n: readLines (goldenDir + "/${n}")) sidecars));

  # A sidecar left behind by a deleted cell would keep claiming coverage, so
  # every sidecar must name a cell the Go golden test still runs.
  cells = builtins.concatLists (
    filter (m: m != null) (
      map (builtins.match ''.*\{name: "([^"]+)".*'') (
        splitString "\n" (
          builtins.readFile ../../cmd/launcher/internal/promptassembly/golden_integration_test.go
        )
      )
    )
  );

  orphansOf = sidecarCells: liveCells: filter (c: !(builtins.elem c liveCells)) sidecarCells;

  orphanSidecars = orphansOf (map (removeSuffix ".fragments.txt") sidecars) cells;

  # Fragments no golden cell renders, each asserted by another test instead.
  # Name test functions, not line numbers, in the reasons: lines rot.
  allowlist = {
    "auto-format.md" =
      "assemble_test.go TestAssembleAutoFormatGate; fragment_rendering_test.go TestAssembleStepRendering/AUTO-FORMAT_on_points_at_the_skill,_no_inline_nix_fmt_rationale";
    "auto-lint.md" =
      "assemble_test.go TestAssembleAutoLintGate; fragment_rendering_test.go TestAssembleStepRendering/AUTO-LINT_on_points_at_the_skill,_no_inline_linter_procedure";
    "ci-failure.md" = "assemble_test.go TestAssembleCIFailureSummaryGate";
    "issue-blocked-comment-forgejo-readonly.md" =
      "fragment_rendering_test.go TestAssembleFragmentRendering/blocked-comment_forgejo_read-only_never_runs_fj_issue_comment";
    "research-verdict-forgejo.md" =
      "fragment_rendering_test.go TestAssembleFragmentRendering/research_verdict_forgejo_read-write_keeps_fj_issue_comment";
    "research-verdict-forgejo-readonly.md" =
      "fragment_rendering_test.go TestAssembleFragmentRendering/research_verdict_forgejo_read-only_relays,_never_fj_issue_comment; orchestrator TestMarkerFragmentParity";
    "research-verdict-local.md" =
      "assemble_test.go TestAssembleResearchPromptCavemanLocalTracker; orchestrator TestMarkerFragmentParity";
  };

  goldenCoverage =
    {
      registry,
      covered,
      allowlist,
    }:
    let
      allowed = builtins.attrNames allowlist;
    in
    {
      uncovered = filter (f: !(builtins.elem f covered) && !(builtins.elem f allowed)) registry;
      staleCovered = filter (f: builtins.elem f covered) allowed;
      staleUnregistered = filter (f: !(builtins.elem f registry)) allowed;
    };

  clean = {
    uncovered = [ ];
    staleCovered = [ ];
    staleUnregistered = [ ];
  };

  fixtureOk = goldenCoverage {
    registry = [
      "a.md"
      "b.md"
    ];
    covered = [ "a.md" ];
    allowlist = {
      "b.md" = "tested elsewhere";
    };
  };
  fixtureUncovered = goldenCoverage {
    registry = [
      "a.md"
      "b.md"
    ];
    covered = [ "a.md" ];
    allowlist = { };
  };
  fixtureStaleCovered = goldenCoverage {
    registry = [ "a.md" ];
    covered = [ "a.md" ];
    allowlist = {
      "a.md" = "now covered";
    };
  };
  fixtureStaleUnregistered = goldenCoverage {
    registry = [ "a.md" ];
    covered = [ "a.md" ];
    allowlist = {
      "gone.md" = "fragment removed";
    };
  };

  real = goldenCoverage { inherit registry covered allowlist; };

  section =
    heading: hint: names:
    optionalString (names != [ ]) ''
      echo ${escapeShellArg heading}
      ${concatStringsSep "\n" (map (n: "echo ${escapeShellArg "  ${n}"}") names)}
      echo ${escapeShellArg "  -> ${hint}"}
      fail=1
    '';
in
{
  prompt-assembly-golden-coverage =
    assert assertMsg (fixtureOk == clean)
      "prompt-assembly-golden-coverage: a covered + allowlisted registry must yield no findings, got: ${builtins.toJSON fixtureOk}";
    assert assertMsg (fixtureUncovered == clean // { uncovered = [ "b.md" ]; })
      "prompt-assembly-golden-coverage: a registry row with neither golden nor allowlist entry must land in uncovered, got: ${builtins.toJSON fixtureUncovered}";
    assert assertMsg (fixtureStaleCovered == clean // { staleCovered = [ "a.md" ]; })
      "prompt-assembly-golden-coverage: an allowlisted-but-covered entry must land in staleCovered, got: ${builtins.toJSON fixtureStaleCovered}";
    assert assertMsg (fixtureStaleUnregistered == clean // { staleUnregistered = [ "gone.md" ]; })
      "prompt-assembly-golden-coverage: an allowlisted name absent from the registry must land in staleUnregistered, got: ${builtins.toJSON fixtureStaleUnregistered}";
    assert assertMsg (orphansOf [ "kept" "deleted" ] [ "kept" ] == [ "deleted" ])
      "prompt-assembly-golden-coverage: a sidecar whose cell no longer runs must land in orphanSidecars";
    assert assertMsg (registry != [ ] && covered != [ ] && cells != [ ])
      "prompt-assembly-golden-coverage: expected a non-empty lib/fragments.nix registry, at least one *.fragments.txt golden sidecar and at least one goldenCells() cell -- check is vacuous";
    pkgs.runCommand "prompt-assembly-golden-coverage" { } ''
      fail=0
      ${section "fragments with no golden cell and no allowlist entry:"
        "add a golden cell to cmd/launcher/internal/promptassembly/golden_integration_test.go and run nix run .#regen-goldens, or add an allowlist entry whose value names the covering test in nix/checks/prompt-assembly-golden-coverage.nix"
        real.uncovered
      }
      ${section "allowlisted fragments that a golden cell now covers:"
        "remove the allowlist entry from nix/checks/prompt-assembly-golden-coverage.nix"
        real.staleCovered
      }
      ${section "allowlisted names absent from lib/fragments.nix:"
        "remove the allowlist entry from nix/checks/prompt-assembly-golden-coverage.nix"
        real.staleUnregistered
      }
      ${section "fragments.txt sidecars with no cell in goldenCells() (golden_integration_test.go):"
        "delete the stale sidecar, or restore its cell"
        orphanSidecars
      }
      [ "$fail" = 0 ] || exit 1
      touch $out
    '';
}
