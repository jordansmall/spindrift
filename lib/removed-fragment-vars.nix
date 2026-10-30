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
# in the forgejo/filer-*.md fragments must keep rendering.
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
  RESEARCH_ISSUE_READ_GITHUB_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_ISSUE_READ_STEP";
  };
  RESEARCH_ISSUE_READ_LOCAL_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_ISSUE_READ_STEP";
  };
  RESEARCH_ISSUE_READ_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_ISSUE_READ_STEP";
  };
  SCOUT_ISSUE_READ_GITHUB_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "SCOUT_ISSUE_READ_STEP";
  };
  SCOUT_ISSUE_READ_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "SCOUT_ISSUE_READ_STEP";
  };
  FILER_DEDUP_SEARCH_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "FILER_DEDUP_STEP";
  };
  FILER_DEDUP_SEARCH_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "FILER_DEDUP_STEP";
  };
  FILER_DEDUP_SEARCH_LOCAL_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "FILER_DEDUP_STEP";
  };
  RESEARCH_VERDICT_GITHUB_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_VERDICT_STEP";
  };
  RESEARCH_VERDICT_GITHUB_READONLY_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_VERDICT_RELAY_STEP";
  };
  RESEARCH_VERDICT_GITHUB_READONLY_SOCKET_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_VERDICT_RELAY_SOCKET_STEP";
  };
  RESEARCH_VERDICT_LOCAL_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_VERDICT_RELAY_STEP";
  };
  RESEARCH_VERDICT_LOCAL_SOCKET_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_VERDICT_RELAY_SOCKET_STEP";
  };
  RESEARCH_VERDICT_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_VERDICT_STEP";
  };
  RESEARCH_VERDICT_FORGEJO_READONLY_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_VERDICT_RELAY_STEP";
  };
  RESEARCH_VERDICT_FORGEJO_READONLY_SOCKET_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_VERDICT_RELAY_SOCKET_STEP";
  };
  RESEARCH_OUTCOME_LANDING_GITHUB_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_OUTCOME_LANDING_STEP";
  };
  RESEARCH_OUTCOME_LANDING_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "RESEARCH_OUTCOME_LANDING_STEP";
  };
  ISSUE_BLOCKED_COMMENT_GITHUB_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "ISSUE_BLOCKED_COMMENT_STEP";
  };
  ISSUE_BLOCKED_COMMENT_GITHUB_READONLY_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "ISSUE_BLOCKED_COMMENT_RELAY_STEP";
  };
  ISSUE_BLOCKED_COMMENT_LOCAL_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "ISSUE_BLOCKED_COMMENT_RELAY_STEP";
  };
  ISSUE_BLOCKED_COMMENT_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "ISSUE_BLOCKED_COMMENT_STEP";
  };
  ISSUE_BLOCKED_COMMENT_FORGEJO_READONLY_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "ISSUE_BLOCKED_COMMENT_RELAY_STEP";
  };
  FILER_FILE_DIRECT_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "FILER_DIRECT_FILE_STEP";
  };
  FILER_FILE_DIRECT_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "FILER_DIRECT_FILE_STEP";
  };
  FILER_LABEL_DIRECT_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "FILER_DIRECT_LABEL_STEP";
  };
  FILER_LABEL_DIRECT_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "FILER_DIRECT_LABEL_STEP";
  };
  FIX_CI_READ_GITHUB_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "FIX_CI_READ_STEP";
  };
  FIX_CI_READ_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "FIX_CI_READ_STEP";
  };
  OPEN_PR_CREATE_READ_WRITE_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "OPEN_PR_CREATE_DIRECT_STEP";
  };
  OPEN_PR_CREATE_READ_WRITE_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "OPEN_PR_CREATE_DIRECT_STEP";
  };
  IF_BLOCKED_PR_READ_WRITE_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "IF_BLOCKED_PR_DIRECT_STEP";
  };
  IF_BLOCKED_PR_READ_WRITE_FORGEJO_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "IF_BLOCKED_PR_DIRECT_STEP";
  };
  COMMIT_PUSH_LOCAL_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "COMMIT_PUSH_HOST_MEDIATED_STEP";
  };
  IF_BLOCKED_TRIAGE_LOCAL_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "IF_BLOCKED_TRIAGE_HOST_MEDIATED_STEP";
  };
  IF_BLOCKED_PUSH_LOCAL_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "IF_BLOCKED_PUSH_HOST_MEDIATED_STEP";
  };
  PR_BODY_LOCAL_REF_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "PR_BODY_ISSUE_REF_STEP";
  };
  PR_BODY_LOCAL_NOREF_STEP = {
    issue = 5647;
    severity = "deprecated";
    replacement = "PR_BODY_NO_ISSUE_REF_STEP";
  };
}
