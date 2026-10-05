# Fragment variables the prompt assembler no longer substitutes, keyed by
# bare name, with the issue that removed each. The launcher's substitute passes
# an unknown ${...} token through verbatim, so a Consumer prompt override still
# writing one would ship the literal token to the model. Eval-time scan coverage:
# see removedFragmentVarViolations in lib/mkHarness.nix. The launcher's own
# runtime check is the hand-kept table in
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
}
