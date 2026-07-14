# The Conditional fragment registry (issue #622, CONTEXT.md "Conditional
# fragment"): one row per opt-in prompt step. agent/entrypoint.sh's single
# fragment loop and its `_subst` substitution allowlist are both rendered
# from these rows (via lib/mkHarness.nix's fragmentRegistryPreamble), so a
# fragment can never reference a variable the substitution step doesn't know
# about, and a forgotten allowlist entry is impossible by construction.
#
# Each row:
#   gate     - bash variable name tested for non-emptiness to include the
#              step. The three knob-gated steps (auto-format, auto-lint,
#              CI-failure summary) name their launcher-delivered env var
#              directly; the computed-gate steps (skills discovery, the
#              per-skill caveman/tdd/commit deferrals, filer-enabled) name a
#              variable a short precompute line sets before the loop runs
#              (agent/entrypoint.sh, phase_prompt_assembly).
#   fragment - basename under prompts/fragments/ (templates/default or a
#              SPINDRIFT_PROMPT_DIR override) to render via `_subst` when the
#              gate is non-empty.
#   var      - bash variable the rendered (or, when the gate is off, empty)
#              fragment text is assigned to; substituted into the outer
#              prompt templates (issue-prompt.md / fix-prompt.md) via envsubst
#              and so always part of the substitution allowlist.
#   extraSubstVars - additional envsubst allowlist entries the fragment's own
#              body references (default []). Only skill-preamble.md and
#              ci-failure.md interpolate a variable inside their own text
#              (SKILLS_FOUND, CI_FAILURE_SUMMARY respectively); every other
#              fragment is static prose once its step is on.
[
  {
    gate = "SKILLS_FOUND";
    fragment = "skill-preamble.md";
    var = "SKILL_PREAMBLE";
    extraSubstVars = [ "SKILLS_FOUND" ];
  }
  {
    gate = "CAVEMAN_BAKED";
    fragment = "caveman-default.md";
    var = "CAVEMAN_STEP";
  }
  {
    gate = "TDD_BAKED";
    fragment = "tdd-default.md";
    var = "TDD_STEP";
  }
  {
    gate = "COMMIT_BAKED";
    fragment = "commit-default.md";
    var = "COMMIT_STEP";
  }
  {
    gate = "FILER_ENABLED";
    fragment = "file-issues.md";
    var = "FILE_ISSUES_STEP";
  }
  {
    gate = "AUTO_FORMAT";
    fragment = "auto-format.md";
    var = "AUTO_FORMAT_STEP";
  }
  {
    gate = "AUTO_LINT";
    fragment = "auto-lint.md";
    var = "AUTO_LINT_STEP";
  }
  {
    gate = "CI_FAILURE_SUMMARY";
    fragment = "ci-failure.md";
    var = "CI_FAILURE_STEP";
    extraSubstVars = [ "CI_FAILURE_SUMMARY" ];
  }
]
