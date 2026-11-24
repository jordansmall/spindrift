# The Connector protocol, described once as data (issue #7504). `nix run .#regen`
# renders it through render.nix into cmd/launcher/internal/connectorprotocol:
# protocol_gen.go (constants and the method table) and protocol.schema.json.
#
# It describes protocol 0.3.0 as it is today; it adds nothing to the wire. Core's
# own hand-written constants stay as they are, because the SDK's parity tests
# evaluate them, and agreement tests (connectorhost/protocol_test.go) fail when
# one drifts from this file: if a value differs, this description is wrong.
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
      (entry "not-mergeable" "A forge refused a merge because the PR is not mergeable as it stands.")
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
    };

    # The canonical event kinds (ADR 0065), in driverkit's order. A guest may emit
    # every kind but the host's own.
    events = [
      {
        name = "start";
        guest = true;
        doc = "A model call (turn) begins.";
      }
      {
        name = "text";
        guest = true;
        doc = "Assistant prose.";
      }
      {
        name = "reasoning";
        guest = true;
        doc = "Assistant reasoning; never narration or result text.";
      }
      {
        name = "tool_call";
        guest = true;
        doc = "The agent invoking a tool.";
      }
      {
        name = "tool_result";
        guest = true;
        doc = "A tool's output coming back.";
      }
      {
        name = "usage";
        guest = true;
        doc = "One model call's token tally.";
      }
      {
        name = "model";
        guest = true;
        doc = "The model in force changed.";
      }
      {
        name = "error";
        guest = true;
        doc = "An error the CLI reported.";
      }
      {
        name = "unparsed";
        guest = true;
        doc = "A line outside the CLI's grammar.";
      }
      {
        name = "result";
        guest = true;
        doc = "The session's final result and totals.";
      }
      {
        name = "host_op";
        guest = false;
        doc = "Core's own spindrift_op line; a guest never emits it.";
      }
    ];

    # What core checks of a method's result beyond decoding it, named for the Go
    # function that does it. A method row names one.
    resultChecks = {
      decode = "Core decodes the result into its Go type and checks nothing more (ErrMalformedOutput otherwise).";
      describe = "connectorhost.DriverStrategy refuses an unknown pattern reason, totals or result name.";
      guestEvents = "connectorhost.guestEvents decodes strictly: an unknown field, an unknown kind or a host_op rejects the answer.";
      batchEvents = "connectorhost.batchEvents requires one element per line, each checked as guestEvents; a bad element fails only its line.";
      resolveExitGuard = "connectorhost.DriverStrategy refuses an answer that resolves a non-zero exit to 0.";
    };

    # Shared JSON types, rendered as the schema's $defs. A result field is
    # required only when core refuses, or cannot act on, an answer that omits it;
    # one core decodes to its zero value when absent is optional, whatever the
    # SDK always emits. Params are all required: core always sends them.
    types = {
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
      ++ import ./tracker.nix { inherit t; };
  };
in
self
