# The tracker axis's methods, in the order docs/reference.md's "Tracker
# capabilities" table lists them. Params follow the structs connectortracker
# sends and results the ones it decodes (wireIssue, wireComment, ...);
# sdk/tracker serves the same. Retry follows connectortracker.CallPolicy, which
# every tracker call goes through: comment, fileIssue, createLabel and
# transition are called once, since a lost response would repeat the write or
# meet a state that no longer offers it. editLabels and closeIssue are retried,
# as the policy's own comment says, though docs/reference.md does not list them
# among the writes called once. The policy has no OnRetryFailure, so no row has a
# doneOn.
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
  target = t.ref "trackerTarget";
  route = t.optional (t.list (t.ref "route"));
  targeted = t.object { inherit target; };
  numbered = t.object {
    inherit target;
    number = t.string;
  };
  # What a write answers: an empty object.
  done = t.open { };
  ids = t.open { ids = t.list t.string; };
  issues = t.open { issues = t.list (t.ref "issue"); };
in
[
  (method "tracker.listIssues" "tracker"
    (t.object {
      inherit target route;
      label = t.string;
      status = t.string;
      limit = t.integer;
    })
    issues
    retryable
    "issueShape"
    "The open issues carrying `label` or in `status` (an empty one ignored, both empty for every open issue), narrowed by `route`; core asks for `limit` and does not trust the guest to honour it."
  )
  (method "tracker.issue" "tracker" numbered (t.ref "issue") retryable "issueAnswer"
    "One issue by number."
  )
  (method "tracker.comments" "tracker.comments" numbered (t.open {
    comments = t.list (t.ref "comment");
  }) retryable "commentShape" "The issue's comments, oldest first.")
  (method "tracker.nativeDeps" "tracker.nativeDeps" numbered ids retryable "idList"
    "The ids of the issues `number` is blocked on, in the API's order."
  )
  (method "tracker.blocks" "tracker.blocks" numbered ids retryable "idList"
    "The ids of the issues `number` blocks, in the API's order."
  )
  (method "tracker.probe" "tracker.probe" targeted (t.open { status = t.integer; }) retryable
    "probeStatus"
    "The HTTP status of one authenticated read; an error only when the request could not be sent."
  )
  (method "tracker.statuses" "tracker.statuses" targeted
    (t.open {
      statuses = t.list t.string;
      categories = t.optional (t.map t.string);
    })
    retryable
    "decode"
    "The name of every status in the project's workflows, and each one's category where the tracker reports it (`done` marks a terminal one)."
  )
  (method "tracker.createMeta" "tracker.createMeta"
    (t.object {
      inherit target;
      issueType = t.string;
    })
    (t.open {
      issueTypes = t.list t.string;
      fields = t.list (
        t.open {
          fieldId = t.string;
          name = t.string;
          required = t.boolean;
          hasDefaultValue = t.boolean;
        }
      );
    })
    retryable
    "createMetaIssueType"
    "Every issue type the project offers and the create fields of `issueType`, none when it is not offered."
  )
  (method "tracker.count" "tracker.count"
    (t.object {
      inherit target route;
      label = t.string;
      status = t.string;
      limit = t.integer;
      fresh = t.boolean;
    })
    (t.open { count = t.integer; })
    retryable
    "countClamp"
    "How many issues the selection matches, possibly approximate; `fresh` asks the Connector to bypass any conditional-request cache."
  )
  (method "tracker.countInterval" "tracker.count" (t.object { }) (t.open { seconds = t.integer; })
    retryable
    "countInterval"
    "How often core re-asks tracker.count; read once, when the tracker is built."
  )
  (method "tracker.removedLabels" "tracker.removedLabels" numbered (t.open {
    labels = t.list t.string;
  }) retryable "decode" "The names of the labels removed from the issue, oldest first.")
  (method "tracker.searchIssues" "tracker.searchIssues"
    (t.object {
      inherit target;
      text = t.string;
      state = t.enum [
        "open"
        "closed"
      ];
      label = t.string;
      limit = t.integer;
    })
    issues
    retryable
    "issueShape"
    "The issues matching `text` in `state` (and carrying `label` unless it is empty), in match order."
  )
  (method "tracker.labeled" "tracker.labeled"
    (t.object {
      inherit target route;
      label = t.string;
      state = t.enum [
        "open"
        "closed"
      ];
      limit = t.integer;
      cursor = t.optional t.string;
    })
    (t.open {
      issues = t.list (t.ref "issue");
      next = t.optional t.string;
    })
    retryable
    "labeledPaging"
    "One page of the issues carrying `label` in `state`, newest first, closedAt and closedNotPlanned filled for closed ones; `next` is the opaque cursor of the following page, empty at the end."
  )
  (method "tracker.transition" "tracker.transition"
    (t.object {
      inherit target;
      number = t.string;
      state = t.string;
      mapping = t.map (t.ref "statusTarget");
    })
    (t.open {
      outcome = t.enum [
        "moved"
        "unavailable"
        "unmapped"
      ];
      status = t.string;
      offered = t.optional (t.list t.string);
    })
    once
    "transitionOutcome"
    "Moves the issue to the native status core's `mapping` names for `state`; core decides the fallback, the refusal and the label strip."
  )
  (method "tracker.editLabels" "tracker.editLabels" (t.object {
    inherit target;
    number = t.string;
    add = t.list t.string;
    remove = t.list t.string;
  }) done retryable "decode" "One edit that adds and removes the named labels.")
  (method "tracker.comment" "tracker.comment"
    (t.object {
      inherit target;
      number = t.string;
      body = t.string;
      research = t.boolean;
    })
    done
    once
    "decode"
    "Posts one comment: `body` is canonical Markdown without the research marker line, `research` says the comment carries it."
  )
  (method "tracker.fileIssue" "tracker.fileIssue"
    (t.object {
      inherit target route;
      title = t.string;
      body = t.string;
      labels = t.list t.string;
      dedupMarker = t.string;
      issueType = t.string;
    })
    (t.open { url = t.string; })
    once
    "filedURL"
    "Files one issue and answers its browse URL; `body` is canonical Markdown without the dedup marker line, which `dedupMarker` carries verbatim."
  )
  (method "tracker.closeIssue" "tracker.closeIssue" numbered done retryable "decode"
    "Closes the issue as completed, never as not planned; core calls it only on an open issue."
  )
  (method "tracker.listLabels" "tracker.labels" targeted (t.open {
    labels = t.list t.string;
  }) retryable "labelNames" "Every label the tracker knows.")
  (method "tracker.createLabel" "tracker.labels" (t.object {
    inherit target;
    name = t.string;
    description = t.string;
    color = t.string;
  }) done once "decode" "Adds a label to the registry; a no-op where the tracker has none.")
  (method "tracker.matchesRoute" "tracker.route"
    (t.object {
      inherit target;
      number = t.string;
      route = t.list (t.ref "route");
    })
    (t.open { matches = t.boolean; })
    retryable
    "decode"
    "Whether issue `number` matches every selector in `route`; core sends it only for a non-empty route."
  )
]
