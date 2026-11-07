# The Gate vocabulary (CONTEXT.md "Gate vocabulary", issue #6589): the closed
# set of names a fragment row's `gate` may use, each with a one-line meaning.
# The launcher's Gates() computes this set minus SKILLS_FOUND, which the
# assembler adds (promptassembly assemble.go); lib/mkHarness.nix rejects a row
# naming anything else. Pure builtins only, no `pkgs`, so it stays importable
# with no arguments, like lib/fragment-pairs.nix.
let
  bakedSkills = import ./baked-skills.nix;
  dispatchKinds = import ./dispatch-kinds;
  inherit (import ./builtins-compat.nix) mapAttrsToList;

  # Base gates only; the _LOG/_SOCKET variants promptassembly/gates.go derives
  # from each (narrowed by the BOX_SIGNAL_CARRIER knob) are expanded below.
  signalSplit = {
    ISSUE_TRACKER_RELAYED = "every tracker write goes through the host relay";
    ISSUE_TRACKER_GITHUB_READONLY = "the GitHub write axis, read-only Box or forced relay";
    ISSUE_TRACKER_LOCAL = "the tracker reads through the local axis";
    ISSUE_TRACKER_FORGEJO_READONLY = "the Forgejo write axis, read-only Box or forced relay";
    BOX_ACCESS_READ_ONLY = "the Box has no write access";
    PR_FORGE_READ_ONLY = "a read-only Box on a forge with pull requests";
    FILER_FILE_RELAY = "the Filer files issues through the host relay";
    RESEARCH_VERDICT_RELAYED = "a research verdict is posted through the host relay";
  };

  # Derived from lib/baked-skills.nix rows, so no skill gate is hand-listed.
  skillBaked = map (
    s: s // { meaning = "the /${s.name} skill is baked into the image"; }
  ) bakedSkills;

  signalSplitGates = builtins.concatLists (
    mapAttrsToList (base: meaning: [
      {
        name = base;
        value = meaning;
      }
      {
        name = "${base}_LOG";
        value = "${meaning}, and the signal carrier is log";
      }
      {
        name = "${base}_SOCKET";
        value = "${meaning}, and the signal carrier is socket";
      }
    ]) signalSplit
  );

  # One entry per distinct filerRelayGate: work and recover share one, so its
  # meaning names every kind that does.
  filerRelayKindGates = mapAttrsToList (gate: kinds: {
    name = gate;
    value = "the Filer relay is active on a ${
      builtins.concatStringsSep " or " (map (k: k.name) kinds)
    } dispatch (selects its label prose)";
  }) (builtins.groupBy (k: k.filerRelayGate) dispatchKinds);

  literal = {
    TDD_UNBAKED = "the tdd skill is not baked, so the inline red/green/refactor fallback renders";
    COMMIT_UNBAKED = "the commit skill is not baked, so the inline Conventional Commits rules render";
    CODE_REVIEW_UNBAKED = "the code-review skill is not baked, so the hunt runs solo";

    FILER_ENABLED = "the Filer is on the roster";
    FILER_ABSENT = "the Filer is not on the roster";
    WORKER_PROVISIONED = "the roster provisions a worker";
    SCOUT_PROVISIONED = "the roster provisions a scout";
    SCOUT_ABSENT = "the roster provisions no scout";
    COORDINATOR_SCOUT_BRIEF = "the coordinator reads a scout brief (worker and scout provisioned, kind lands code)";
    WORKER_SCOUT_BRIEF = "the worker reads a scout brief (scout provisioned, kind lands code)";
    SIGNAL_CARRIER_SOCKET = "the signal carrier is socket rather than log";
    AUTO_FORMAT = "the Consumer enabled the auto-format knob";
    AUTO_LINT = "the Consumer enabled the auto-lint knob";
    CI_FAILURE_SUMMARY = "a CI failure summary was forwarded (fix pass)";
    CHORE_PATCH_CLASSES = "the Chore has a patch-eligible class allow-list";
    CHORE_INPUT = "a records-scoped Chore has host-rendered input";
    SKILLS_FOUND = "the assembler found skills on disk (set by the assembler, not Gates())";

    ISSUE_TRACKER_GITHUB = "the tracker reads through the GitHub axis";
    ISSUE_TRACKER_FORGEJO = "the tracker reads through the Forgejo axis";
    ISSUE_TRACKER_REACHABLE = "the Box has an in-Box client for the tracker";
    ISSUE_TRACKER_LINKS_REACHABLE = "the Box can fetch parent and linked issues itself";
    ISSUE_TRACKER_LINKS_READABLE = "the tracker is not unwritable, so linked issues are readable";
    ISSUE_TRACKER_LINKS_UNREADABLE = "the tracker is unwritable, so linked issue text is injected host-side";
    ISSUE_TRACKER_GITHUB_LINKS_READABLE = "the GitHub axis with readable linked issues";
    ISSUE_TRACKER_WRITABLE = "the Box can write the tracker directly";
    ISSUE_TRACKER_UNWRITABLE = "the tracker takes no in-Box writes (jira)";
    ISSUE_TRACKER_GITHUB_WRITABLE = "the GitHub axis and the tracker is writable";
    ISSUE_TRACKER_GITHUB_READWRITE = "the GitHub write axis, writable Box, no forced relay";
    ISSUE_TRACKER_UNWRITABLE_READWRITE = "an unwritable tracker on a writable Box, no forced relay (comments relay via note=)";
    ISSUE_TRACKER_FORGEJO_READWRITE = "the Forgejo write axis, writable Box, no forced relay";
    ISSUE_TRACKER_READWRITE = "the Box writes the tracker directly, whatever the backend";
    FILER_FILE_DIRECT_GH = "the Filer files issues directly with gh";
    FILER_FILE_DIRECT_FORGEJO = "the Filer files issues directly with fj";
    FILER_FILE_DIRECT_ANY = "the Filer files issues directly, with either client";
    PR_BODY_CLOSES = "the PR body closes the issue with a Closes line";
    PR_BODY_LOCAL_REF = "the PR body cites a local issue by slug (reference opted in)";
    PR_BODY_LOCAL_NOREF = "a local-issue PR body carries no issue reference";
    PR_BODY_ISSUE_REF = "the PR body carries an issue reference (opt-in knob)";
    PR_BODY_NO_ISSUE_REF = "the PR body carries no issue reference (opt-in knob off)";

    BOX_ACCESS_READ_WRITE = "the Box has write access";
    OPEN_PR_CREATE_RW_GH = "a writable Box opens its PR with gh";
    OPEN_PR_CREATE_RW_FORGEJO = "a writable Box opens its PR with fj";
    PR_FORGE_READ_WRITE = "a writable Box on a forge with pull requests";
    NO_PR_FORGE = "the forge has no pull requests";
    IF_BLOCKED_PR_RW_GH = "a blocked writable Box opens its PR with gh";
    IF_BLOCKED_PR_RW_FORGEJO = "a blocked writable Box opens its PR with fj";
    BOX_PUSH_READ_WRITE = "a writable Box pushes its branch itself";
    HOST_MEDIATED_PUSH = "the launcher lands the branch host-side";
    OUTBOX_PUSH_READ_ONLY = "a read-only Box pushes through the outbox";
    FIX_CI_READ_GH = "the fix pass reads CI with gh";
    FIX_CI_READ_FORGEJO = "the fix pass reads CI with fj";
  };

  entries =
    map (s: {
      name = s.gate;
      value = s.meaning;
    }) skillBaked
    ++ signalSplitGates
    ++ filerRelayKindGates
    ++ mapAttrsToList (name: value: { inherit name value; }) literal;

  # listToAttrs silently keeps the first of a duplicate name, so a gate
  # declared by two sources would lose a meaning unnoticed.
  counts = builtins.mapAttrs (_: builtins.length) (builtins.groupBy (e: e.name) entries);
  duplicates = builtins.filter (n: counts.${n} > 1) (builtins.attrNames counts);
in
{
  # name -> one-line meaning, for every gate.
  gates =
    if duplicates == [ ] then
      builtins.listToAttrs entries
    else
      throw "gate-vocabulary: gate(s) declared more than once: ${builtins.concatStringsSep ", " duplicates}";

  # The baked-skill rows regen renders gates.go's SKILL-BAKED GATES block from.
  inherit skillBaked;
}
