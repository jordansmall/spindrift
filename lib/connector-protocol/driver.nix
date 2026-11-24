# The driver axis's methods, in the order docs/reference.md's "Connector
# protocol" table lists them. Params and results follow
# cmd/launcher/internal/connectorhost/driver.go (what core sends and decodes) and
# sdk/driver (what a guest serves). Retry: a driver call goes straight through
# Connector.Call, which never retries; `safe` records only whether repeating a
# call is harmless. The handle methods allocate or free guest state, so they are
# not.
{ t }:
let
  stateless = {
    safe = true;
    doneOn = [ ];
  };
  stateful = {
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
  handle = name: t.object { ${name} = t.integer; };
in
[
  (method "driver.describe" "driver" (t.object { })
    (t.open {
      name = t.string;
      transientPatterns = t.list (t.ref "pattern");
      terminalPatterns = t.list (t.ref "pattern");
      writeTool = t.string;
      classify = t.optional (
        t.open {
          chunkOversized = t.optional t.boolean;
          firstMatch = t.optional t.boolean;
        }
      );
      totals = t.optional (
        t.enum [
          "perCall"
          "session"
        ]
      );
      defaultTopLevelRole = t.optional t.string;
      result = t.optional (
        t.enum [
          "events"
          "lastMessage"
        ]
      );
      kindOrder = t.optional (t.list t.string);
      kindPlurals = t.optional (t.map t.string);
    })
    stateless
    "describe"
    "The Driver's identity and classification tables; the only call made while building the Strategy."
  )
  (method "driver.newDecoder" "driver" (t.object { now = t.optional t.string; })
    (t.open { decoder = t.integer; })
    stateful
    "decode"
    "Opens a guest decoder; `now` is the host's wall clock as RFC 3339, which a relative time such as \"resets 3pm\" resolves against."
  )
  (method "driver.decode" "driver"
    (t.object {
      decoder = t.integer;
      line = t.string;
    })
    (t.list (t.ref "event"))
    stateful
    "guestEvents"
    "Decodes one Pass-log line into canonical events on a stateful decoder."
  )
  (method "driver.freeDecoder" "driver" (handle "decoder") t.null stateful "decode"
    "Frees a guest decoder."
  )
  (method "driver.resultEvent" "driver" (t.object { text = t.string; }) (t.open {
    event = t.string;
  }) stateless "decode" "Renders a result text as the one Pass-log line the CLI would have written.")
  (method "driver.sessionFlags" "driver" (t.object {
    mode = t.string;
    repoSlug = t.string;
    issue = t.string;
    home = t.string;
  }) (t.open { flags = t.string; }) stateless "decode" "The CLI flags that pin or resume a session.")
  (method "driver.resolveExit" "driver.resolveExit"
    (t.object {
      log = t.string;
      exitCode = t.integer;
    })
    (t.open { exitCode = t.integer; })
    stateless
    "resolveExitGuard"
    "Resolves the CLI's exit code from the Pass log's content (the newest whole lines that fit the request cap)."
  )
  (method "driver.toolPhase" "driver.toolPhase"
    (t.object {
      tool = t.string;
      input = t.any;
    })
    (t.open { phase = t.string; })
    stateless
    "decode"
    "The heartbeat phase a tool call belongs to; `input` is the call's raw JSON."
  )
  (method "driver.toolKind" "driver.toolKind" (t.object { tool = t.string; }) (t.open {
    kind = t.string;
  }) stateless "decode" "The heartbeat kind a tool belongs to.")
  (method "driver.modelFamily" "driver.modelFamily" (t.object { model = t.string; }) (t.open {
    label = t.string;
    rank = t.integer;
  }) stateless "decode" "A model id's family label and sort rank for the heartbeat.")
  (method "driver.decodeBatch" "driver.decodeBatch"
    (t.object {
      decoder = t.integer;
      lines = t.list t.string;
    })
    (t.list (t.list (t.ref "event")))
    stateful
    "batchEvents"
    "Decodes many lines in order on one decoder: one event array per line."
  )
  (
    method "driver.decodeRaw" "driver.decodeRaw"
      (t.raw "The bytes \"driver.decodeRaw\\n<decoder>\\n\" then each Pass-log line followed by \"\\n\"; a line holds no newline, and nothing is JSON-escaped.")
      (t.list (t.list (t.ref "event")))
      stateful
      "batchEvents"
      "driver.decodeBatch with the lines sent as raw bytes."
    // {
      raw = true;
    }
  )
  (method "driver.newStreamFilter" "driver.streamFilter" (t.object { }) (t.open {
    filter = t.integer;
  }) stateful "decode" "Opens a guest filter over the CLI's stdout stream.")
  (method "driver.streamWrite" "driver.streamFilter"
    (t.object {
      filter = t.integer;
      data = t.string;
    })
    (t.open { data = t.string; })
    stateful
    "decode"
    "Feeds one piece (base64) through the filter; the result is what it wrote during the call (base64)."
  )
  (method "driver.streamFlush" "driver.streamFilter" (handle "filter") (t.open {
    data = t.string;
  }) stateful "decode" "Flushes the filter's held bytes (base64).")
  (method "driver.freeStreamFilter" "driver.streamFilter" (handle "filter") t.null stateful "decode"
    "Frees a guest filter."
  )
]
