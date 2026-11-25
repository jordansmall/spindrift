# The Connector protocol, described once as data (issue #7504). `nix run .#regen`
# renders it through render.nix into cmd/launcher/internal/connectorprotocol:
# protocol_gen.go (constants and the method table) and protocol.schema.json, and,
# through the binding in sdk.nix, into the SDK's sdk/<pkg>/protocol_gen.go.
#
# It describes protocol 0.3.0 as it is today; it adds nothing to the wire. Core's
# own hand-written constants stay as they are, and agreement tests (the
# protocol_test.go and wirefields_test.go files under cmd/launcher/internal) fail
# when one drifts from this file: if a value differs, this description is wrong.
#
# docs/reference.md's "Connector protocol" section is the prose contract. A method
# row's fields are listed in render.nix, which throws on a missing, unknown or
# inconsistent one.
let
  t = import ./t.nix;
  entry = name: doc: { inherit name doc; };
  # The description refers to its own events (self), so it is a lazy fixpoint.
  self = {
    version = "0.3.0";

    # Capability names a Connector advertises in its handshake, per axis. A
    # capability name is its axis, or the axis and a dot-separated suffix.
    capabilities = {
      tracker = [
        (entry "tracker" "Serves tracker.listIssues and tracker.issue.")
        (entry "tracker.comments" "Serves tracker.comments, and attests each comment's minimized state is faithful.")
        (entry "tracker.nativeDeps" "Serves tracker.nativeDeps: the issues a number is blocked on.")
        (entry "tracker.blocks" "Serves tracker.blocks: the issues a number blocks.")
        (entry "tracker.probe" "Serves tracker.probe: the HTTP status of one authenticated read.")
        (entry "tracker.statuses" "Serves tracker.statuses: every status in the project's workflows.")
        (entry "tracker.createMeta" "Serves tracker.createMeta: the issue types and create fields.")
        (entry "tracker.count" "Serves tracker.count and tracker.countInterval.")
        (entry "tracker.removedLabels" "Serves tracker.removedLabels: the labels removed from an issue.")
        (entry "tracker.searchIssues" "Serves tracker.searchIssues.")
        (entry "tracker.labeled" "Serves tracker.labeled: a paged read by label and state.")
        (entry "tracker.transition" "Serves tracker.transition: a native status move.")
        (entry "tracker.editLabels" "Serves tracker.editLabels: one add-and-remove label edit.")
        (entry "tracker.comment" "Serves tracker.comment: posts one comment.")
        (entry "tracker.fileIssue" "Serves tracker.fileIssue: files one issue.")
        (entry "tracker.closeIssue" "Serves tracker.closeIssue: closes an issue as completed.")
        (entry "tracker.route" "Serves tracker.matchesRoute and accepts route selectors.")
        (entry "tracker.labels" "Serves tracker.listLabels and tracker.createLabel.")
        (entry "tracker.ids.projectKey" "Serves no method: issue ids are project-keyed (ENG-12), not decimal integers.")
      ];
      forge = [
        (entry "forge" "Serves forge.prsForBranch, forge.pr and forge.checks, and optionally forge.jobLog.")
        (entry "forge.branches" "Serves forge.remote, forge.branchExists and forge.deleteBranch.")
        (entry "forge.pushOnly" "The forge has no pull-request concept, so core composes a push-only Code Forge.")
        (entry "forge.prs" "Serves the PR primitives: forge.behind, forge.prFiles, forge.setDraft, forge.comment and forge.merge.")
        (entry "forge.autoMerge" "Serves forge.autoMergeAllowed and forge.enqueueAutoMerge.")
        (entry "forge.ciRun" "Serves forge.ciRun: a PR's CI run link.")
        (entry "forge.branchProtection" "Serves forge.branchProtected: whether the base branch is protected.")
        (entry "forge.probe" "Serves forge.probe: one cheap authenticated read of the repository.")
        (entry "forge.draftPRs" "Serves forge.openPR, which lets core host-mediate a read-only Box's hand-off.")
        (entry "forge.tokenGrant" "Serves forge.tokenGrant: what the credential may do on the repository.")
      ];
      driver = [
        (entry "driver" "Serves the first six driver methods: describe, newDecoder, decode, freeDecoder, resultEvent and sessionFlags.")
        (entry "driver.resolveExit" "Serves driver.resolveExit.")
        (entry "driver.toolPhase" "Serves driver.toolPhase.")
        (entry "driver.toolKind" "Serves driver.toolKind.")
        (entry "driver.modelFamily" "Serves driver.modelFamily.")
        (entry "driver.decodeBatch" "Serves driver.decodeBatch (0.3.0).")
        (entry "driver.decodeRaw" "Serves driver.decodeRaw (0.3.0, additive): batch decode over raw bytes; alone enough for core to decode in batches.")
        (entry "driver.streamFilter" "Serves the four stream methods: newStreamFilter, streamWrite, streamFlush and freeStreamFilter (0.1.1, additive).")
      ];
    };

    # The canonical error classes, in the order connectorhost/class.go declares
    # them. A missing or unknown class reads as permanent.
    errorClasses = [
      (entry "transient" "A retry may clear it: a timeout, a 5xx, a dropped connection.")
      (entry "rate-limited" "Refused for sending too much; core's rate-limit hold handles it, not a retry loop.")
      (entry "not-found" "The subject does not exist.")
      (entry "auth" "The credential was refused or lacks standing.")
      (entry "not-mergeable" "A forge refused a merge because the PR is not mergeable as it stands: a conflict, a missing required check or review, or a draft, closed, or merged state. Core re-reads the PR on this class only.")
      (entry "permanent" "Every other failure, and any answer naming no class or one core does not know.")
    ];

    # Every ceiling that has a hand-written core constant, which the agreement test
    # pins. The unit is the name's suffix or, with none, bytes (a count where the
    # doc says so).
    limits = {
      maxHTTPResponse = {
        value = 4 * 1024 * 1024;
        doc = "Bytes of response body http_request hands a guest.";
      };
      maxHTTPURL = {
        value = 8 * 1024;
        doc = "Bytes of an http_request URL, checked before it is parsed or routed.";
      };
      maxHeaderName = {
        value = 256;
        doc = "Bytes of a header name http_response_header looks up; a longer one is fatal, as for http_request.";
      };
      maxHTTPRequests = {
        value = 128;
        doc = "Count of http_requests one call may make.";
      };
      maxHTTPTimeSeconds = {
        value = 300;
        doc = "Seconds one call may spend in http_requests together.";
      };
      maxHTTPRequestSeconds = {
        value = 60;
        doc = "Seconds one http_request may take end to end.";
      };
      maxRequest = {
        value = 8 * 1024 * 1024;
        doc = "Bytes of one connector_call request, refused before the guest sees it.";
      };
      maxOutput = {
        value = 8 * 1024 * 1024;
        doc = "Base bytes of one result, a call response or the handshake.";
      };
      memoryCapBytes = {
        value = 64 * 1024 * 1024;
        doc = "Base bytes of module memory; also the largest minimum a module may declare.";
      };
      memoryPerRequestByte = {
        value = 16;
        doc = "Bytes of module memory a call adds per request byte.";
      };
      outputPerRequestByte = {
        value = 6;
        doc = "Bytes of response a call adds per request byte.";
      };
      callBudgetSeconds = {
        value = 5;
        doc = "Base seconds of guest work per call, and for the handshake.";
      };
      initBudgetSeconds = {
        value = 30;
        doc = "Seconds of guest work for _initialize.";
      };
      budgetPerMiBSeconds = {
        value = 60;
        doc = "Seconds a call's budget grows per MiB of request, and per MiB of http_request response.";
      };
      maxResponseCredit = {
        value = 32 * 1024 * 1024;
        doc = "Bytes of http_request response one call earns budget for.";
      };
      maxTableEntries = {
        value = 65536;
        doc = "Table entries a module may declare in total, checked at load.";
      };
      maxFrameSlots = {
        value = 2048;
        doc = "Slots in one function's frame, checked at load.";
      };
      maxModuleLocals = {
        value = 1048576;
        doc = "Locals a module may declare in total, checked at load.";
      };
      maxErrorText = {
        value = 256;
        doc = "Bytes of Connector-supplied error text core keeps.";
      };
      resultPageLimit = {
        value = 100;
        doc = "Count of issues core keeps from one tracker list or search page, and the cap on a tracker.count answer.";
      };
      labeledMaxPages = {
        value = 1000;
        doc = "Count of pages one tracker.labeled walk reads before core keeps what it has and warns.";
      };
      labeledMaxBytes = {
        value = 64 * 1024 * 1024;
        doc = "Bytes of answers one tracker.labeled walk holds before core keeps what it has and warns.";
      };
      maxCountIntervalSeconds = {
        value = 86400;
        doc = "Seconds, at most, of the cadence a tracker.countInterval answer may ask for.";
      };
      maxScopeLen = {
        value = 64;
        doc = "Bytes of one scope in a forge.tokenGrant answer.";
      };
      maxReasonLen = {
        value = 200;
        doc = "Runes of the reason in a forge.tokenGrant answer; core truncates a longer one.";
      };
    };

    # The canonical event kinds (ADR 0065), in driverkit's order. A guest may emit
    # every kind but the host's own. A progress kind shows the agent working past an
    # earlier error: an error followed by one was recovered from.
    events = [
      {
        name = "start";
        guest = true;
        progress = true;
        doc = "A model call (turn) begins. Fills Timestamp and MessageID. The classifier counts it as agent progress. A CLI with no per-call start record (claude) emits one per assistant or user turn as its progress marker, even for a line with no decodable content.";
      }
      {
        name = "text";
        guest = true;
        progress = true;
        doc = "Assistant prose. Fills Text (as the CLI wrote it, not trimmed), MessageID, Timestamp, Role. The result reader, transcript renderer and heartbeat writer consume it; the classifier counts it as agent progress.";
      }
      {
        name = "reasoning";
        guest = true;
        progress = true;
        doc = "Assistant reasoning or thinking; never narration, transcript or result text, so decode a CLI's thinking output to this kind, never to text, or it would surface in the verdict scan and heartbeat. It counts as agent progress for the classifier. Fills Text, MessageID, Timestamp, Role.";
      }
      {
        name = "tool_call";
        guest = true;
        progress = true;
        doc = "The agent invoking a tool. Fills ToolID, ToolName, ToolInput, ToolTarget, MessageID, Timestamp, Role. The transcript renderer shows it; it counts as agent progress.";
      }
      {
        name = "tool_result";
        guest = true;
        progress = true;
        doc = "A tool's output coming back. Fills ToolID, ToolName, ToolTarget, ToolOutput, IsError (the tool failed), Subagent (a spawn tool's report), MessageID, Timestamp, Role; Text stays empty. The transcript renderer shows it; it counts as agent progress. A CLI that reports call and result in one record emits both kinds from it, call first, the result repeating the call's ToolInput and ToolTarget.";
      }
      {
        name = "usage";
        guest = true;
        progress = false;
        doc = "One model call's token tally. Fills Tokens, Model, Effort (when known), MessageID, Timestamp, Role. The usage reader sums every usage event for the run totals (so a decoder emits one per call, not a running total) and dedups the per-model breakdown by non-empty MessageID. It is not agent progress.";
      }
      {
        name = "model";
        guest = true;
        progress = false;
        doc = "The model in force changed: a model id observed. Fills Model, MessageID, Timestamp, Role, and Effort when known. The heartbeat writer reports a change of Model and, when Effort is set, shows model:effort in the role header.";
      }
      {
        name = "error";
        guest = true;
        progress = false;
        doc = "An error the CLI reported inside its grammar. Text keeps the error's raw text, because the classifier matches transient markers on it; ResetAt carries a rate-limit reset the payload names. The latest one not followed by agent progress decides the classification, or the first with ClassifyOptions.FirstMatch. A CLI that reports failures as plain text (claude) decodes every non-content line to this kind.";
      }
      {
        name = "unparsed";
        guest = true;
        progress = false;
        doc = "A line outside the CLI's grammar: stderr shares the Pass log, so a Driver sees the CLI's own diagnostics here. Text is the trimmed line. The classifier reads it only for terminal markers; nothing else consumes it.";
      }
      {
        name = "result";
        guest = true;
        progress = false;
        doc = "The CLI's final result text, for a CLI that reports one as its own event. Fills Text, and may carry totals in Tokens, DurationMs, DurationAPIMs and NumTurns, and name the session's Models. The result reader lets it replace the text gathered so far. A CLI whose result is its last message's prose emits no result event.";
      }
      {
        name = "host_op";
        guest = false;
        progress = false;
        doc = "Spindrift's own host-written spindrift_op line, which rides the Pass log in every Driver's stream; a guest never emits it. Fills HostOp and Timestamp. The heartbeat writer renders it via FormatSpindriftOp.";
      }
    ];

    # The Reason a driver Pattern carries (driverkit.AllReasons, in its order).
    reasons = [
      (entry "rateLimit" "API rate limit.")
      (entry "overloaded" "API overload or capacity error.")
      (entry "network" "Transient network failure.")
      (entry "taskFailed" "The agent ran but produced no valid result.")
      (entry "unsupportedFlag" "The driver rejected a CLI option core passed (version skew).")
    ];

    # The host-line grammar a Driver's decoder follows: the host writes
    # {"type":"spindrift_op","spindrift_op":{"op":"pass_start","role":<pass>}}
    # into the Pass log, and a decoder reads the role the pass starts. The roles
    # are the attribution roles core stamps on events; passRoles maps a pass
    # name to the role it starts (any other pass starts none). `type` also names
    # the event's payload member key, which the SDK's HostLineOf matches.
    hostLine = {
      type = "spindrift_op";
      opPassStart = "pass_start";
      roles = [
        (entry "implementor" "Attributes a top-level event in an implement, fix or land pass.")
        (entry "reviewer" "Attributes a top-level event in a review or delta-review pass.")
        (entry "subagent" "Attributes an event whose subagent parent is unknown.")
      ];
      passRoles = {
        review = "reviewer";
        delta-review = "reviewer";
        implement = "implementor";
        fix = "implementor";
        land = "implementor";
      };
    };

    # The categories of a tracker.statuses entry that core reads.
    statusCategories = [
      (entry "done" "The category of a terminal workflow status.")
    ];

    # The outcomes of tracker.transition that are not an error.
    transitionOutcomes = [
      (entry "moved" "The issue now sits in the requested status.")
      (entry "unavailable" "The issue's workflow offers no transition to the resolved status; offered names the statuses it does offer.")
      (entry "unmapped" "The mapping holds no non-empty status for the state.")
    ];

    # What core checks of a method's result beyond decoding it. A method row
    # names one. A forge or tracker check is the body connectorforge's
    # forgeChecks or connectortracker's trackerChecks registers under that name,
    # which connectorcall.Unregistered pins (each axis's
    # Test*ChecksAgreeWithTheMethodTable runs it); a driver check is named for the
    # Go function that does it.
    resultChecks = {
      decode = "Core decodes the result into its Go type and checks nothing more (ErrMalformedOutput otherwise).";
      describe = "connectorhost.DriverStrategy refuses an unknown pattern reason, totals or result name.";
      guestEvents = "connectorhost.guestEvents decodes strictly: an unknown field, an unknown kind or a host_op rejects the answer.";
      batchEvents = "connectorhost.batchEvents requires one element per line, each checked as guestEvents; a bad element fails only its line.";
      resolveExitGuard = "connectorhost.DriverStrategy refuses an answer that resolves a non-zero exit to 0.";
      prShape = "connectorforge's body refuses a PR whose number is not a canonical positive integer, whose URL is not a plain https URL on the forge's web host naming the target repository and that same number, or whose state and merged pair is impossible (anything but open and unmerged, closed and merged, or closed and unmerged).";
      prAnswer = "connectorforge's body applies prShape and refuses a PR other than the one asked for; the callers go on to refuse a mergeable value outside MERGEABLE, CONFLICTING and UNKNOWN (Mergeable), a merge commit that is not a full lowercase hex id (MergeCommit), an empty head commit (HeadCommitSHA), and, before a rebase, a head other than the agent branch core derived (RebasePRHead).";
      rollupState = "connectorforge's body, which runs on every forge.checks answer (CheckState and FailureDetail both read it), fails closed on a rollup state outside SUCCESS, PENDING, EXPECTED, FAILURE and ERROR; an empty state reads as no checks.";
      remoteURLs = "connectorforge's body refuses a clone or web URL that is not https with a host, carries userinfo, or names a host other than the one core derived from the forge's Git remote declaration (checkURL); a refusal is audited.";
      behindCount = "connectorforge's body refuses a negative behindBy.";
      prFiles = "connectorforge's body refuses a missing or empty list, and an entry that is not a clean repository-relative path (cleanRepoPath), because the merge guard reads it.";
      runLink = "connectorforge's body drops, rather than fails on, a URL that is not a plain https URL on the web host under the target repository's path; a backslash is refused.";
      probeRepo = "connectorforge's body refuses an empty repo.";
      tokenGrant = "connectorforge's body refuses writeCapable or scopes without introspectable and a scope that is not a well-formed token (wellFormedScope); cleanReason then sanitises the reason and caps it at maxReasonLen; a scope is capped at maxScopeLen.";
      issueShape = "connectortracker's body refuses an issue whose number is outside the Connector's id grammar (a positive integer, or a project key and number under tracker.ids.projectKey), whose state is not open or closed (a listIssues answer may leave it empty), or that has an empty or control-character label name (checkLabels); a hit tracker.searchIssues returns outside the state or label asked for is dropped, not refused.";
      issueAnswer = "connectortracker's body applies issueShape, then refuses an issue other than the one asked for.";
      commentShape = "connectortracker's body refuses a comment whose author is empty or holds a control character, or whose createdAt is not RFC3339; an association outside forge.KnownAssociation's vocabulary is blanked, not refused.";
      idList = "connectortracker's body refuses an id outside the Connector's id grammar and drops a repeated one.";
      labeledPaging = "connectortracker's body applies issueShape to each issue of a page; the cross-page part lives in walkLabeled, which fails the label closed on a next cursor already returned or a page of only issues already read, and on a walk past labeledMaxPages or labeledMaxBytes keeps what it read and warns.";
      probeStatus = "connectortracker's body reads the status: 2xx proves the repository, 401 and 403 are an auth failure, any other status means the repository is unreachable.";
      createMetaIssueType = "connectortracker's body refuses an answer whose issueTypes omit the issue type core files as (forge.IssueTypeNotOffered).";
      countClamp = "connectortracker's body refuses a negative count and clamps the rest to forge.ResultPageLimit.";
      countInterval = "connectortracker's body refuses seconds outside 1 to maxCountInterval (24 hours).";
      transitionOutcome = "connectortracker's body refuses an outcome other than moved, unavailable and unmapped.";
      labelNames = "connectortracker's body refuses an empty or control-character label name (checkLabels).";
      filedURL = "connectortracker's body refuses a url that is empty, not https, carrying userinfo, off the tracker's web host (Lifecycle.WebHost) or not ending in an issue id (unusableFiledURL); the issue was filed, so the error says so and core must not refile.";
    };

    # Shared JSON types, rendered as the schema's $defs. A result field is
    # required only when core refuses, or cannot act on, an answer that omits it;
    # one core decodes to its zero value when absent is optional, whatever the
    # SDK always emits. Params are all required: core always sends them.
    types = {
      # The repository a forge call reads; api is the https base URL without a
      # trailing slash.
      target = t.object {
        api = t.string;
        repo = t.string;
      };
      # The repository a tracker call reads. Unlike a forge's target it carries
      # the API dialect, "" for a tracker with one ("cloud" or "server" for Jira).
      trackerTarget = t.object {
        api = t.string;
        repo = t.string;
        dialect = t.string;
      };
      # One term of an issue route; core sends a route only to a Connector
      # advertising tracker.route.
      route = t.object {
        kind = t.string;
        value = t.string;
      };
      # connectortracker.StatusTarget: the native status a Dispatch state maps to
      # and the resolution to set on entering it, "" for none.
      statusTarget = t.object {
        status = t.string;
        resolution = t.string;
      };
      # connectortracker.wireIssue. An open issue, a tracker with no side store
      # or one with no workflow leaves closedAt, closedNotPlanned, dedupMarker and
      # status empty.
      issue = t.open {
        number = t.string;
        title = t.optional t.string;
        body = t.optional t.string;
        state = t.optional (
          t.enum [
            ""
            "open"
            "closed"
          ]
        );
        labels = t.optional (t.list t.string);
        closedAt = t.optional t.string;
        closedNotPlanned = t.optional t.boolean;
        dedupMarker = t.optional t.string;
        status = t.optional t.string;
      };
      # One field of an issue type's create form (tracker.createMeta).
      createField = t.open {
        fieldId = t.string;
        name = t.string;
        required = t.boolean;
        hasDefaultValue = t.boolean;
      };
      # The tracker.transition params after target and number, and its result;
      # core ships the whole mapping on every call.
      transitionRequest = t.object {
        state = t.string;
        mapping = t.map (t.ref "statusTarget");
      };
      transitionResult = t.open {
        outcome = t.enum (map (o: o.name) self.transitionOutcomes);
        status = t.string;
        offered = t.optional (t.list t.string);
      };
      # The tracker.comment params after target and number.
      commentRequest = t.object {
        body = t.string;
        research = t.boolean;
      };
      # The tracker.fileIssue params after target.
      issueRequest = t.object {
        title = t.string;
        body = t.string;
        labels = t.list t.string;
        dedupMarker = t.string;
        issueType = t.string;
        route = t.optional (t.list (t.ref "route"));
      };
      # connectortracker.wireComment.
      comment = t.open {
        author = t.string;
        createdAt = t.string;
        body = t.optional t.string;
        minimized = t.optional t.boolean;
        minimizedReason = t.optional t.string;
        association = t.optional t.string;
        researchMarker = t.optional t.boolean;
      };
      # connectorforge.wirePR: the fields after merged are a PR-capable forge's,
      # each omitted when empty.
      pr = t.open {
        number = t.string;
        url = t.string;
        state = t.enum [
          "open"
          "closed"
        ];
        merged = t.optional t.boolean;
        draft = t.optional t.boolean;
        head = t.optional t.string;
        headSha = t.optional t.string;
        mergeable = t.optional (
          t.enum [
            "MERGEABLE"
            "CONFLICTING"
            "UNKNOWN"
          ]
        );
        mergeCommit = t.optional t.string;
        crossRepo = t.optional t.boolean;
      };
      # One context of a head commit's rollup. Core reads kind "CheckRun" and
      # "StatusContext" and ignores any other.
      check = t.open {
        kind = t.string;
        name = t.optional t.string;
        conclusion = t.string;
        summary = t.optional t.string;
        jobId = t.optional t.string;
      };
      # A PR head commit's status-check rollup; state is "" when it has no
      # checks.
      rollup = t.open {
        state = t.enum [
          ""
          "SUCCESS"
          "PENDING"
          "EXPECTED"
          "FAILURE"
          "ERROR"
        ];
        checks = t.list (t.ref "check");
      };
      remote = t.open {
        cloneUrl = t.string;
        webUrl = t.string;
      };
      grant = t.open {
        introspectable = t.optional t.boolean;
        writeCapable = t.optional t.boolean;
        reason = t.optional t.string;
        scopes = t.optional (t.list t.string);
      };
      # The wire form of a guest's http_request Host call (not a method: the host
      # call carries it as one JSON argument).
      httpRequest = t.object {
        method = t.string;
        url = t.string;
        headers = t.optional (t.map t.string);
        body = t.optional t.string;
      };
      pattern = t.open {
        substr = t.string;
        reason = t.string;
      };
      tokens = t.object {
        Input = t.optional t.integer;
        Output = t.optional t.integer;
        CacheRead = t.optional t.integer;
        CacheWrite5m = t.optional t.integer;
        CacheWrite1h = t.optional t.integer;
        CostUSD = t.optional t.number;
      };
      # driverkit.Event, which has no JSON tags: fields are keyed by their Go names.
      # The host decodes it strictly, so only Kind is required and nothing else
      # is admitted. HostOp is core's, not the guest's, and so left out.
      event = t.object {
        Kind = t.enum (map (e: e.name) (builtins.filter (e: e.guest) self.events));
        Role = t.optional t.string;
        InSubagent = t.optional t.boolean;
        Synthetic = t.optional t.boolean;
        MessageID = t.optional t.string;
        Timestamp = t.optional t.integer;
        Text = t.optional t.string;
        Model = t.optional t.string;
        Effort = t.optional t.string;
        ToolID = t.optional t.string;
        ToolName = t.optional t.string;
        ToolInput = t.optional t.any;
        IsError = t.optional t.boolean;
        ToolTarget = t.optional t.string;
        ToolOutput = t.optional t.string;
        Subagent = t.optional t.string;
        Tokens = t.optional (t.ref "tokens");
        ResetAt = t.optional (t.nullable t.string);
        SessionID = t.optional t.string;
        DurationMs = t.optional t.integer;
        DurationAPIMs = t.optional t.integer;
        NumTurns = t.optional t.integer;
        Models = t.optional (t.nullable (t.list t.string));
        ModelOutput = t.optional (t.nullable (t.map t.integer));
        NoTotals = t.optional t.boolean;
        NoTurns = t.optional t.boolean;
      };
    };

    methods =
      import ./driver.nix { inherit t; }
      ++ import ./forge.nix { inherit t; }
      ++ import ./tracker.nix {
        inherit t;
        inherit (self) types;
      };
  };
in
self
