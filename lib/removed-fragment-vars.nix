# Fragment variables the prompt assembler no longer substitutes, or is retiring,
# keyed by bare name, with the issue that did so. The launcher's substitute
# passes an unknown ${...} token through verbatim, so a Consumer prompt override
# still writing a removed one would ship the literal token to the model.
#
# `severity` is "removed" (the default) or "deprecated". A removed var is no
# longer in lib/fragments.nix and makes the build and the launcher reject the
# prompt. A deprecated var is still a live fragments.nix var; it only warns,
# naming `replacement` (a bare var name, required for a deprecated row), at
# eval time and again at run time.
#
# Eval-time scan coverage: see removedFragmentVarHits in lib/mkHarness.nix
# (removed rows throw via removedFragmentVarViolations, deprecated rows warn via
# deprecatedFragmentVarWarnings). The launcher's own runtime check is the hand-kept table in
# cmd/launcher/internal/promptassembly/removed_vars.go, pinned to this file by
# nix/checks/equivalence.nix. A targeted list on purpose, never a reject of
# every unknown token: legitimate pass-through text such as ${FORGEJO_TOKEN}
# in the filer-*-forgejo.md fragments must keep rendering.
{
  CAVEMAN_STEP_WORKER = {
    issue = 4562;
  };
  REVIEW_LOOP_INLINE_STEP = {
    issue = 4291;
  };
  CODE_COMMENTS_STEP = {
    issue = 3505;
  };
  COMMIT_STEP = {
    issue = 3222;
  };
  CODE_REVIEW_STEP = {
    issue = 3222;
  };
  TDD_STEP = {
    issue = 3219;
  };
  ISSUE_READ_GITHUB_STEP = {
    issue = 5646;
    severity = "deprecated";
    replacement = "ISSUE_READ_STEP";
  };
  ISSUE_READ_LOCAL_STEP = {
    issue = 5646;
    severity = "deprecated";
    replacement = "ISSUE_READ_STEP";
  };
  ISSUE_READ_FORGEJO_STEP = {
    issue = 5646;
    severity = "deprecated";
    replacement = "ISSUE_READ_STEP";
  };
}
