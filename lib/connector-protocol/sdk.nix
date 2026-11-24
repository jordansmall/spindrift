# How the SDK spells the Connector protocol's values in Go (issue #7505). It holds
# no protocol value: every key names a value in the description (default.nix), and
# render.nix's renderSDK throws on one that is not there. A name not listed here is
# derived, and unexported: `cap<Suffix>` for a capability, `method<Suffix>` for a
# method, and for the other rows the description's own name in Go case. Only what
# a Connector imports is listed, because the SDK's exported names must not change.
#
# `structs` binds a wire struct to the description shape it is rendered from: a
# `types` row (`type`) or a method's params (`params`). The description governs the
# field set, so renderSDK throws on a field the shape lacks and on one the list
# leaves out; the list gives only what the description cannot: field order, Go
# presentation and docs. A field is its JSON name, or `field` with `go` (Go name,
# else the JSON name in Go case), `type` (Go type, else derived from the
# description's), `omitempty` and `doc`. `embed` splices in another bound struct,
# whose fields count as this one's. `tagless` renders no JSON tags, the Go name
# being the JSON name (driverkit.Event has none).
#
# `enums` binds the SDK's Go value of a description enum to its wire name: `path`
# is the enum's dotted path (connectorprotocol.WireEnums), `type` the Go type, `var`
# the unexported map renderSDK emits, and `names` maps each enum value to the Go
# constant that stands for it. renderSDK throws on a value `names` lacks or adds,
# so the wire spelling is the description's alone.
let
  field = json: extra: { inherit json; } // extra;
  omit = json: field json { omitempty = true; };
in
{
  # Limits the SDK publishes.
  connector.limits = [ "maxHTTPResponse" ];

  connector.structs = [
    {
      name = "Request";
      type = "httpRequest";
      doc = ''
        Request is one outbound HTTP request. The host admits it only if a declared
        route matches, and attaches the credential itself; the Connector never holds
        it, and a header the Connector sets cannot override it. It is the host's
        httpRequest wire shape, rendered from the same description.
      '';
      fields = [
        "method"
        "url"
        (omit "headers")
        (omit "body")
      ];
    }
  ];

  event.structs = [
    {
      name = "Event";
      type = "event";
      tagless = true;
      doc = ''
        Event is one canonical fact a decoder derived from a Pass log line. Only the
        fields named in its Kind's doc are meaningful; the rest are zero.
      '';
      fields = [
        (field "Kind" { type = "Kind"; })
        (field "Role" {
          doc = ''
            Role is the role the decoder attributed: a subagent's, or for a
            top-level event the pass role in force (empty = the reader's default). A
            Driver whose CLI has no role attribution always leaves it "". The
            vocabulary carries no parent tool-use id, so a Driver whose grammar
            attributes role by one (claude) resolves Role in its
            stateful decoder.
          '';
        })
        (field "InSubagent" {
          doc = ''
            InSubagent marks an event attributed to a subagent, so Role names it.
            Per-role usage keys on it: a non-InSubagent event is the main loop whatever
            pass Role it carries.
          '';
        })
        (field "Synthetic" {
          doc = ''
            Synthetic marks a turn the CLI wrote itself rather than the model: it is
            not agent progress, so the classifier does not let a Synthetic content
            event (text, reasoning, tool call or result) recover an earlier error.
          '';
        })
        (field "MessageID" {
          doc = ''
            MessageID identifies the model message the event belongs to. It is the
            usage dedup key and the result reader's message boundary; leave it ""
            when the CLI's grammar gives none.
          '';
        })
        (field "Timestamp" {
          type = "int64";
          doc = "Timestamp is epoch milliseconds, 0 when the line carries none.";
        })
        (field "Text" {
          doc = ''
            Text is prose for KindText, KindReasoning and KindResult, and raw text
            for KindError and KindUnparsed.
          '';
        })
        (field "Model" {
          doc = ''
            Model is the model id for KindUsage and KindModel. Leave it "" when the
            grammar does not name one.
          '';
        })
        (field "Effort" {
          doc = ''Effort is the effort level for KindUsage and KindModel, "" when unknown.'';
        })
        (field "ToolID" { doc = "ToolID, ToolName, ToolInput and IsError describe tool events."; })
        "ToolName"
        (field "ToolInput" { type = "json.RawMessage"; })
        "IsError"
        (field "ToolTarget" {
          doc = ''
            ToolTarget is the input field that best identifies what a tool call
            acted on (command, file path, pattern, url, query, subagent type), "" when
            unknown. The decoder picks it, because which field that is belongs to the
            CLI's grammar.
          '';
        })
        (field "ToolOutput" {
          doc = ''
            ToolOutput is a KindToolResult's output text as the CLI reported it, or
            the error message when IsError.
          '';
        })
        (field "Subagent" {
          doc = ''
            Subagent is the role of the subagent whose completed report a
            KindToolResult carries (a task or spawn tool), "" for an ordinary tool.
            Distinct from Role, which stays the loop the event is attributed to.
          '';
        })
        (field "Tokens" {
          doc = ''
            Tokens is the tally for KindUsage, and the totals a KindResult may carry
            (see SessionID for which of them are cumulative).
          '';
        })
        (field "ResetAt" {
          type = "*time.Time";
          doc = "ResetAt is the rate-limit reset a KindError's payload names, else nil.";
        })
        (field "SessionID" {
          doc = ''
            SessionID names the CLI session a KindResult belongs to. A decoder sets
            it only when the CLI reports Tokens.CostUSD, DurationAPIMs and ModelOutput
            cumulatively for that session, carried through a resume: RebaseResults then
            hands the readers those three as per-query increments. A decoder whose
            results are already per query leaves it "".
          '';
        })
        (field "DurationMs" {
          type = "int64";
          doc = ''
            DurationMs, DurationAPIMs and NumTurns are the totals a CLI reports on its
            KindResult. DurationAPIMs is session-cumulative when SessionID is set.
          '';
        })
        (field "DurationAPIMs" { type = "int64"; })
        "NumTurns"
        (field "Models" {
          doc = ''
            Models is the sorted model ids the CLI's session summary names,
            meaningful on KindResult only. It is not a KindModel (which moves the
            heartbeat) nor a KindUsage.Model (which includes synthetic turns).
          '';
        })
        (field "ModelOutput" {
          doc = ''
            ModelOutput is the output tokens the CLI's result attributes to each model
            id (session-cumulative when SessionID is set), meaningful on KindResult
            only. Nil when the result carries no usable per-model figure (absent,
            empty or mistyped), which is distinct from a model whose figure is 0.
          '';
        })
        (field "NoTotals" {
          doc = ''
            NoTotals marks a KindResult whose session totals failed to decode: it
            still carries Text, but the usage reader skips it entirely.
          '';
        })
        (field "NoTurns" {
          doc = ''
            NoTurns marks a KindResult whose turn count failed to decode; the
            heartbeat drops such a result.
          '';
        })
      ];
    }
    {
      name = "Tokens";
      type = "tokens";
      tagless = true;
      doc = ''
        Tokens is one model call's token tally. A field the CLI does not report is
        0; the decoder maps the CLI's own buckets onto these (opencode folds
        reasoning tokens into Output and its single cache-write count into
        CacheWrite5m).
      '';
      fields = [
        "Input"
        "Output"
        "CacheRead"
        "CacheWrite5m"
        "CacheWrite1h"
        "CostUSD"
      ];
    }
  ];

  forge.caps = {
    "forge.branches" = "CapBranches";
    "forge.pushOnly" = "CapPushOnly";
    "forge.prs" = "CapPRs";
    "forge.autoMerge" = "CapAutoMerge";
    "forge.ciRun" = "CapCIRun";
    "forge.branchProtection" = "CapBranchProtection";
    "forge.probe" = "CapProbe";
    "forge.draftPRs" = "CapDraftPRs";
    "forge.tokenGrant" = "CapTokenGrant";
  };

  forge.structs = [
    {
      name = "Target";
      type = "target";
      doc = ''
        Target names the forge repository a call reads from. API is the https base
        URL without a trailing slash, for example https://api.github.com.
      '';
      fields = [
        "api"
        "repo"
      ];
    }
    {
      name = "PR";
      type = "pr";
      doc = ''
        PR is one pull request. State is "open" or "closed" as the forge reports it;
        Merged is true for a merged (hence closed) PR. URL is the PR's web URL.

        The fields after Merged are read by a PR-capable forge (CapPRs); a
        Reader-only Connector may leave them zero. Head is the head branch name.
        Mergeable is the raw "MERGEABLE", "CONFLICTING" or "UNKNOWN"; core fails
        closed on anything else. MergeCommit is the commit a merged PR landed as,
        "" when none.
      '';
      fields = [
        "number"
        "url"
        "state"
        "merged"
        (omit "draft")
        (omit "head")
        (omit "headSha")
        (omit "mergeable")
        (omit "mergeCommit")
        (field "crossRepo" {
          omitempty = true;
          doc = ''
            CrossRepo is true when the head lives in a different repository than
            the base; absent means same-repo.
          '';
        })
      ];
    }
    {
      name = "Check";
      type = "check";
      doc = ''
        Check is one context of a head commit's status-check rollup, in GitHub
        GraphQL's vocabulary (provisional): Kind is "CheckRun" or "StatusContext";
        Conclusion is a CheckRun's conclusion or a StatusContext's state; Summary is
        a CheckRun's summary or a StatusContext's description. Name and Summary are
        untrusted text.
      '';
      fields = [
        "kind"
        "name"
        "conclusion"
        "summary"
        (field "jobId" {
          omitempty = true;
          doc = ''
            JobID names the job whose log JobLog reads; "" when the context has none
            (a StatusContext, or a CheckRun that is not a workflow job).
          '';
        })
      ];
    }
    {
      name = "Rollup";
      type = "rollup";
      doc = ''
        Rollup is a PR head commit's status-check rollup. State is the aggregate
        rollup state (for example "SUCCESS", "PENDING" or "FAILURE"), "" when the
        commit has no checks.
      '';
      fields = [
        "state"
        "checks"
      ];
    }
    {
      name = "Remote";
      type = "remote";
      doc = ''
        Remote is a repository's git identity. CloneURL is what core's git plumbing
        pushes to and WebURL is the repository's web page. Neither may carry
        credentials: core refuses one that does, and one naming a host outside the
        Connector's declared routes.
      '';
      fields = [
        "cloneUrl"
        "webUrl"
      ];
    }
    {
      name = "Grant";
      type = "grant";
      doc = ''
        Grant is what a token may do on a repository, in core's canonical terms. A
        Connector whose forge cannot say sets Introspectable false and gives Reason,
        the operator-facing explanation. WriteCapable is meaningful only when
        Introspectable is true.
      '';
      fields = [
        "introspectable"
        "writeCapable"
        (omit "reason")
        (field "scopes" {
          omitempty = true;
          doc = ''
            Scopes are the OAuth scopes the forge reports for the token, nil when it
            reports none.
          '';
        })
      ];
    }
  ];

  tracker = {
    caps = {
      "tracker.transition" = "CapTransition";
      "tracker.editLabels" = "CapEditLabels";
      "tracker.labels" = "CapLabels";
      "tracker.comment" = "CapComment";
      "tracker.fileIssue" = "CapFileIssue";
      "tracker.closeIssue" = "CapCloseIssue";
    };
    # The methods behind tracker.labels; they ride on it rather than being
    # advertised themselves.
    methods = {
      "tracker.listLabels" = "CapListLabels";
      "tracker.createLabel" = "CapCreateLabel";
    };

    structs = [
      {
        name = "Target";
        type = "trackerTarget";
        doc = ''
          Target names the tracker repository a call reads from. API is the https
          base URL without a trailing slash, for example https://api.github.com.
          Dialect names the tracker deployment's API dialect (a Jira Connector takes
          "cloud" or "server"); it is "" for a tracker with one.
        '';
        fields = [
          "api"
          "repo"
          "dialect"
        ];
      }
      {
        name = "Selector";
        type = "route";
        doc = ''
          Selector is one Route term: Kind names how the tracker matches (a kind the
          tracker declares it supports) and Value is what it matches against.
        '';
        fields = [
          "kind"
          "value"
        ];
      }
      {
        name = "Issue";
        type = "issue";
        doc = ''
          Issue is one tracker issue. State is "open" or "closed"; a listIssues answer
          may leave Body and State empty.
        '';
        fields = [
          "number"
          "title"
          "body"
          "state"
          "labels"
          (field "closedAt" {
            doc = ''
              ClosedAt is RFC3339, "" when the issue is open or the date is unknown.
              Core ignores it and ClosedNotPlanned for an open issue.
            '';
          })
          "closedNotPlanned"
          (field "dedupMarker" {
            doc = ''
              DedupMarker is the raw dedup marker line from the tracker's side store,
              "" when there is none. It is never part of Body.
            '';
          })
          (field "status" {
            doc = ''
              Status is the tracker's workflow status name, "" for a tracker without
              one.
            '';
          })
        ];
      }
      {
        name = "Comment";
        type = "comment";
        doc = ''
          Comment is one issue comment. CreatedAt is RFC3339 as the forge reports it.
        '';
        fields = [
          "author"
          "createdAt"
          "body"
          "minimized"
          "minimizedReason"
          "association"
          (field "researchMarker" {
            doc = ''
              ResearchMarker reports that the comment carries the research marker in
              a side store; Body excludes it.
            '';
          })
        ];
      }
      {
        name = "CreateField";
        type = "createField";
        doc = "CreateField is one field of an issue type's create form.";
        fields = [
          (field "fieldId" { go = "ID"; })
          "name"
          "required"
          "hasDefaultValue"
        ];
      }
      {
        name = "StatusTarget";
        type = "statusTarget";
        doc = ''
          StatusTarget is the tracker status a Dispatch state maps to and the
          resolution to set on entering it ("" for none).
        '';
        fields = [
          "status"
          "resolution"
        ];
      }
      {
        name = "TransitionRequest";
        type = "transitionRequest";
        doc = ''
          TransitionRequest asks a Connector to move an issue to a Dispatch state.
          State and the keys of Mapping are core's canonical state names (dispatchable,
          in-progress, complete, failed, ambiguous, ...). A Connector has no config
          channel, so core ships the whole table on every call and the Connector
          applies Mapping[State], including the rule that only failed and ambiguous
          may share a status. Mapping holds only the states that name a status.
        '';
        fields = [
          "state"
          "mapping"
        ];
      }
      {
        name = "TransitionResult";
        type = "transitionResult";
        doc = ''
          TransitionResult is how a Transition ended. Status is the tracker status the
          Connector resolved State to ("" when unmapped), so core can name it in a
          refusal. Any other failure, such as a transport error or a transition that
          needs a field the request cannot supply, is an error, never an outcome.
        '';
        fields = [
          "outcome"
          "status"
          "offered"
        ];
      }
      {
        name = "CommentRequest";
        type = "commentRequest";
        doc = ''
          CommentRequest is a comment to post. Body is canonical Markdown with the
          research marker line already removed; Research says the comment carries that
          marker, which the Connector stores the way its tracker can (Jira: a comment
          entity property).
        '';
        fields = [
          "body"
          "research"
        ];
      }
      {
        name = "IssueRequest";
        type = "issueRequest";
        doc = ''
          IssueRequest is an issue to file. Body is canonical Markdown without the
          dedup marker line; DedupMarker is that line verbatim ("" for none), stored
          where the tracker can find it on read (Jira: an issue entity property).
          IssueType is the type core files as; a Connector has no config channel, so
          core ships it on each call. Route is the Issue route the filed issue must
          carry (its stamp): a Connector sets every selector, so the issue matches the
          same route's Query; empty stamps nothing.
        '';
        fields = [
          "title"
          "body"
          "labels"
          "dedupMarker"
          "issueType"
          (omit "route")
        ];
      }
      # What each write handler decodes its params into.
      {
        name = "transitionParams";
        params = "tracker.transition";
        fields = [
          "target"
          "number"
          { embed = "TransitionRequest"; }
        ];
      }
      {
        name = "editLabelsParams";
        params = "tracker.editLabels";
        fields = [
          "target"
          "number"
          "add"
          "remove"
        ];
      }
      {
        name = "commentParams";
        params = "tracker.comment";
        fields = [
          "target"
          "number"
          { embed = "CommentRequest"; }
        ];
      }
      {
        name = "fileIssueParams";
        params = "tracker.fileIssue";
        fields = [
          "target"
          { embed = "IssueRequest"; }
        ];
      }
      {
        name = "closeIssueParams";
        params = "tracker.closeIssue";
        fields = [
          "target"
          "number"
        ];
      }
      {
        name = "createLabelParams";
        params = "tracker.createLabel";
        fields = [
          "target"
          "name"
          "description"
          "color"
        ];
      }
    ];
  };

  # The roles are spelled <Role>Role unless listed.
  driver.roles.subagent = "DefaultRole";

  # How a Driver's describe answer names its enums on the wire.
  driver.enums = [
    {
      var = "totalsWire";
      path = "driver.describe.result.totals";
      type = "TotalsSource";
      names = {
        perCall = "PerCallTotals";
        session = "SessionTotals";
      };
    }
    {
      var = "resultWire";
      path = "driver.describe.result.result";
      type = "ResultReader";
      names = {
        events = "ResultEvents";
        lastMessage = "LastMessage";
      };
    }
  ];
}
