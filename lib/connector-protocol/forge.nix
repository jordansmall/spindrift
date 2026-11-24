# The forge axis's methods, in the order docs/reference.md's "Forge
# capabilities" lists them. Params follow the structs connectorforge sends and
# results the ones it decodes (wirePR, wireRollup, wireRemote, wireGrant, ...);
# sdk/forge serves the same. Retry follows connectorforge.CallPolicy, which
# every forge call goes through (the composed forge, Probe, BranchProtected,
# AutoMergeAllowed and IntrospectToken alike): merge, openPR and comment are
# called once, since a retry could repeat the write after its first attempt
# landed; a retried deleteBranch that finds the branch gone has succeeded.
{ t }:
let
  retryable = {
    safe = true;
    doneOn = [ ];
  };
  once = {
    safe = false;
    doneOn = [ ];
  };
  method = name: capability: params: result: retry: resultCheck: doc: {
    inherit
      name
      capability
      params
      result
      retry
      resultCheck
      doc
      ;
    raw = false;
  };
  target = t.ref "target";
  targeted = t.object { inherit target; };
  branched = t.object {
    inherit target;
    branch = t.string;
  };
  numbered = t.object {
    inherit target;
    number = t.string;
  };
  # What a write answers: an empty object.
  done = t.open { };
in
[
  (method "forge.prsForBranch" "forge" branched (t.open {
    prs = t.list (t.ref "pr");
  }) retryable "prShape" "Every PR whose head is `branch`, any state, newest first.")
  (method "forge.pr" "forge" numbered (t.ref "pr") retryable "prAnswer" "One PR by number.")
  (method "forge.checks" "forge" numbered (t.ref "rollup") retryable "rollupState"
    "The status-check rollup of the PR's head commit; an empty `state` means it has no checks."
  )
  (method "forge.jobLog" "forge"
    (t.object {
      inherit target;
      jobId = t.string;
      tail = t.integer;
      errorLines = t.integer;
    })
    (t.open { log = t.string; })
    retryable
    "decode"
    "The last `tail` bytes of a job's log, preceded by the error lines of the head it cut, up to `errorLines` bytes; optional, and a failed read only costs failure detail its logs."
  )
  (method "forge.remote" "forge.branches" targeted (t.ref "remote") retryable "remoteURLs"
    "The repository's git identity: the clone URL core's git pushes to and the web URL."
  )
  (method "forge.branchExists" "forge.branches" branched (t.open {
    exists = t.boolean;
  }) retryable "decode" "Whether `branch` exists on the forge.")
  (method "forge.deleteBranch" "forge.branches" branched done
    {
      safe = true;
      doneOn = [ "not-found" ];
    }
    "decode"
    "Deletes `branch` from the forge; core refuses the base branch and an option-shaped name before the call."
  )
  (method "forge.behind" "forge.prs" numbered (t.open {
    behindBy = t.integer;
  }) retryable "behindCount" "Commits the PR's base has that its head lacks.")
  (method "forge.prFiles" "forge.prs" numbered (t.open {
    files = t.list t.string;
  }) retryable "prFiles" "Every path the PR changes.")
  (method "forge.setDraft" "forge.prs" (t.object {
    inherit target;
    number = t.string;
    draft = t.boolean;
  }) done retryable "decode" "Marks the PR as a draft or ready for review.")
  (method "forge.comment" "forge.prs" (t.object {
    inherit target;
    number = t.string;
    body = t.string;
  }) done once "decode" "Posts `body` on the PR.")
  (method "forge.merge" "forge.prs" numbered done once "decode"
    "Merges the PR now; core classifies a failure, and reads the PR only on `not-mergeable`."
  )
  (method "forge.autoMergeAllowed" "forge.autoMerge" targeted (t.open {
    allowed = t.boolean;
  }) retryable "decode" "Whether the repository permits native auto-merge.")
  (method "forge.enqueueAutoMerge" "forge.autoMerge" numbered done retryable "decode"
    "Asks the forge to merge the PR once its requirements are met."
  )
  (method "forge.ciRun" "forge.ciRun" numbered (t.open { url = t.string; }) retryable "runLink"
    "The URL of the CI run for the PR's head commit, empty when there is none yet."
  )
  (method "forge.branchProtected" "forge.branchProtection" branched (t.open {
    protected = t.boolean;
  }) retryable "decode" "Whether `branch` carries protection, a classic rule or a ruleset.")
  (method "forge.probe" "forge.probe" targeted (t.open { repo = t.string; }) retryable "probeRepo"
    "The repository's canonical owner/name, proving the forge answers with the Connector's credential."
  )
  (method "forge.openPR" "forge.draftPRs"
    (t.object {
      inherit target;
      pr = t.object {
        title = t.string;
        body = t.string;
        base = t.string;
        head = t.string;
        draft = t.boolean;
      };
    })
    (t.ref "pr")
    once
    "prShape"
    "Opens a PR; core only ever opens drafts, and adopts the branch's open PR when the create fails."
  )
  (method "forge.tokenGrant" "forge.tokenGrant"
    (t.object {
      inherit target;
      tokenPrefix = t.string;
    })
    (t.ref "grant")
    retryable
    "tokenGrant"
    "What the credential the host attached may do on the repository; the request carries only the token's non-secret prefix."
  )
]
