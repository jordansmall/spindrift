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
    ISSUE_TRACKER_GITHUB_READONLY = "the tracker writes through the GitHub axis (relayed via the host)";
    ISSUE_TRACKER_LOCAL = "the tracker reads through the local axis";
    ISSUE_TRACKER_FORGEJO_READONLY = "the tracker writes through the Forgejo axis (relayed via the host)";
    PR_FORGE_READ_ONLY = "the Box is on a forge with pull requests";
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

    ISSUE_ROUTED = "an issue route is set (BOX_ISSUE_ROUTED); the fragment names the Target repo from REPO_SLUG";
    ISSUE_TRACKER_GITHUB = "the tracker reads through the GitHub axis";
    ISSUE_TRACKER_FORGEJO = "the tracker reads through the Forgejo axis";
    ISSUE_TRACKER_REACHABLE = "the Box has an in-Box client for the tracker";
    ISSUE_TRACKER_LINKS_REACHABLE = "the host built an issue reader for this dispatch (BOX_SIGNAL_ISSUE_READ), so the Signal socket issue read answers, and the scout step renders";
    ISSUE_TRACKER_LINKS_READABLE = "linked issues are readable: via the Signal socket issue read (BOX_SIGNAL_ISSUE_READ), or local's host-injected text";
    ISSUE_TRACKER_LINKS_UNREADABLE = "no issue reader was built for this dispatch (log carrier, or a tracker with no resolvable scope), so no linked issue is readable";
    ISSUE_TRACKER_GITHUB_LINKS_READABLE = "the GitHub axis (or jira) with BOX_SIGNAL_ISSUE_READ set";
    ISSUE_TRACKER_GITHUB_LINKS_UNREADABLE = "the GitHub axis (or jira) without BOX_SIGNAL_ISSUE_READ";
    ISSUE_TRACKER_FORGEJO_LINKS_READABLE = "the Forgejo axis with BOX_SIGNAL_ISSUE_READ set";
    ISSUE_TRACKER_FORGEJO_LINKS_UNREADABLE = "the Forgejo axis without BOX_SIGNAL_ISSUE_READ";
    ISSUE_TRACKER_SEARCHABLE = "a tracker search reaches the Box: the Signal socket issue-search verb (BOX_SIGNAL_ISSUE_SEARCH), or local's own folder search";
    ISSUE_TRACKER_UNSEARCHABLE = "no tracker search reaches the Box (the host built no issue searcher for this dispatch, or the log carrier)";
    ISSUE_TRACKER_GITHUB_SEARCHABLE = "the GitHub axis with BOX_SIGNAL_ISSUE_SEARCH set";
    ISSUE_TRACKER_GITHUB_UNSEARCHABLE = "the GitHub axis without BOX_SIGNAL_ISSUE_SEARCH";
    ISSUE_TRACKER_FORGEJO_SEARCHABLE = "the Forgejo axis with BOX_SIGNAL_ISSUE_SEARCH set";
    ISSUE_TRACKER_FORGEJO_UNSEARCHABLE = "the Forgejo axis without BOX_SIGNAL_ISSUE_SEARCH";
    ISSUE_TRACKER_WRITABLE = "the Box can write the tracker directly";
    ISSUE_TRACKER_UNWRITABLE = "the tracker takes no in-Box writes (jira)";
    PR_BODY_CLOSES = "the PR body closes the issue with a Closes line";
    PR_BODY_LOCAL_REF = "the PR body cites a local issue by slug (reference opted in)";
    PR_BODY_LOCAL_NOREF = "a local-issue PR body carries no issue reference";
    PR_BODY_ISSUE_REF = "the PR body carries an issue reference (opt-in knob)";
    PR_BODY_NO_ISSUE_REF = "the PR body carries no issue reference (opt-in knob off)";

    BOX_ACCESS_READ_ONLY_LOG = "the signal carrier is log (selects the PR-intent and landing steps)";
    BOX_ACCESS_READ_ONLY_SOCKET = "the signal carrier is socket (selects the PR-intent and landing steps)";
    NO_PR_FORGE = "the forge has no pull requests";
    HOST_MEDIATED_PUSH = "the launcher lands the branch host-side";
    OUTBOX_PUSH_READ_ONLY = "a read-only Box pushes through the outbox";
    FIX_CI_READ_GH = "the fix pass on a github forge works from the host's CI FAILURE";
    FIX_CI_READ_FORGEJO = "the fix pass on a forgejo forge works from the host's CI FAILURE";
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
