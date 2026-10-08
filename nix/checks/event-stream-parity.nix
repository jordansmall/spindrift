# Issue #3765: docs/reference.md's daemon event-stream table (the `| event |
# fields | when |` one) is hand-maintained against the Go emit sites, and
# nothing tied them together. Commit e4dbef87 dropped `Kind` from the
# awake_close/awake_open literals without touching the table, and the row sat
# stale for four days until #3861.
#
# The rule: every backticked name in a row's fields cell is a field -- prose
# like "`reason` instead of `exit` on the paths with ..." or "`revision` when a
# child was involved" counts, because the comparison is against the UNION of
# fields over an event's emit sites (child_finish's two sites differ on `exit`,
# preflight's on `exit`/`reason`). The pair "`issue` or `chore`" is one field:
# the projected Dispatch key.
#
# Go side, read as text:
#   * the struct-field -> JSON-key map comes from the tag lines of `type Event
#     struct` in events.go, so a new field or a tag rename is picked up. The
#     one `json:"-"` field, Key, is mapped by hand to the docs' "issue or
#     chore" because Event.MarshalJSON projects it onto the wire pair.
#   * `time` is stamped by Emitter.Emit, never in a literal, so every event gets
#     it.
#   * emit sites are `Event{Event: <name>, ...}` (or slice-element `Event{{Event:
#     <name>, ...}}`) literals on ONE line, read from
#     every non-test .go file directly in the two daemon packages. A line
#     holding the key `Event: ` that does not parse, or whose literal does not
#     close at the end of that line (`}` then only `}`/`)`), throws rather than
#     being skipped, so a wrapped literal cannot silently drop a field. Double-quoted
#     and raw (backtick) strings are stripped before fields are split, so a
#     ", Kind: " inside a Reason is not a field; rune literals are not stripped.
#     The rest of cmd/launcher is scanned only to throw on a `<pkg>.Event{Event:`
#     literal, the way another package builds an event (any import alias).
#   * known limit: a field set by assignment after the literal (`ev := Event{...};
#     ev.Revision = r; p.emit(ev)`) is not seen; set it in the literal.
#
# `drift` is pure -- text in, mismatch messages out -- so rejection cases can
# feed it doctored inputs.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    drop
    elem
    filter
    hasInfix
    hasPrefix
    hasSuffix
    length
    optional
    removePrefix
    sort
    subtractLists
    take
    unique
    ;
  inherit (pkgs.lib.lists) findFirstIndex;

  me = "nix/checks/event-stream-parity.nix";
  docsFile = "docs/reference.md";
  eventsFile = "cmd/launcher/internal/daemon/events.go";
  reportFile = "cmd/launcher/internal/report/report.go";
  tableHeader = "| event | fields | when |";
  tableSeparator = "|-------|--------|------|";
  # Any dash run per column, so a markdown formatter re-padding the table is fine.
  tableSeparatorRe = "\\|([[:space:]]*-+[[:space:]]*\\|){3}";
  keyToken = "issue or chore";
  # Top-level only: a subpackage of these is "elsewhere" to emitSites.
  emitDirs = [
    "cmd/launcher/internal/daemon"
    "cmd/launcher/daemon"
  ];

  # Line-by-line rather than whole-file matching: builtins.match stack-overflows
  # on large inputs (see scout-rationale-parity.nix), and docs/reference.md is
  # huge. Row cells are cut with builtins.split on "|" for the same reason --
  # some rows run to ~2KB.
  linesOf = text: filter builtins.isString (builtins.split "\n" text);
  sorted = sort builtins.lessThan;
  setOf = xs: sorted (unique xs);

  # ---- docs side ---------------------------------------------------------

  docsRows =
    docsText:
    let
      lines = linesOf docsText;
      hits = filter (i: builtins.elemAt lines i == tableHeader) (builtins.genList (i: i) (length lines));
      start = builtins.head hits;
      below = drop (start + 2) lines;
      n = findFirstIndex (l: !hasPrefix "|" l) (length below) below;
      cellsOf = row: filter builtins.isString (builtins.split "\\|" row);
      backticked = cell: map builtins.head (filter builtins.isList (builtins.split "`([^`]*)`" cell));
      parseRow =
        row:
        let
          cells = cellsOf row;
          names = backticked (builtins.elemAt cells 1);
        in
        if length cells < 3 || length names != 1 then
          throw "${me}: cannot parse a row of ${docsFile}'s event-stream table: ${row}"
        else
          {
            event = builtins.head names;
            fields = setOf (
              backticked (
                builtins.replaceStrings [ "`issue` or `chore`" ] [ "`${keyToken}`" ] (builtins.elemAt cells 2)
              )
            );
          };
    in
    if hits == [ ] then
      throw "${me}: no line equal to \"${tableHeader}\" in ${docsFile} -- has the event-stream table's header moved or been reworded?"
    else if length hits > 1 then
      throw "${me}: ${toString (length hits)} lines equal to \"${tableHeader}\" in ${docsFile} -- this check can no longer tell which is the event-stream table."
    else if builtins.match tableSeparatorRe (builtins.elemAt lines (start + 1)) == null then
      throw "${me}: the line under the event-stream table's header in ${docsFile} is not a \"${tableSeparator}\"-style separator row."
    else
      map parseRow (take n below);

  # ---- Go side -----------------------------------------------------------

  # Struct field name -> JSON key, from the tag lines of `type Event struct`.
  structMap =
    goSources:
    let
      hits = filter (s: s.file == eventsFile) goSources;
      lines = linesOf (builtins.head hits).text;
      start = findFirstIndex (l: l == "type Event struct {") (-1) lines;
      below = drop (start + 1) lines;
      end = findFirstIndex (l: l == "}") (-1) below;
      body = take end below;
      parsed = filter (m: m != null) (
        map (builtins.match "[[:space:]]*([A-Z][A-Za-z]*)[[:space:]]+[^[:space:]]+[[:space:]]+`json:\"([^\",]*)[^\"]*\"`.*") body
      );
      toEntry =
        m:
        let
          name = builtins.elemAt m 0;
          json = builtins.elemAt m 1;
        in
        {
          inherit name;
          value =
            if name == "Key" && json == "-" then
              keyToken
            else if json == "-" then
              throw "${me}: ${eventsFile}'s Event.${name} is tagged json:\"-\" -- only Key (projected onto issue/chore by MarshalJSON) is mapped; teach this check about ${name}."
            else
              json;
        };
    in
    if hits == [ ] then
      throw "${me}: ${eventsFile} is not among the scanned Go sources."
    else if start < 0 then
      throw "${me}: no `type Event struct {` line in ${eventsFile}."
    else if end < 0 then
      throw "${me}: no closing `}` line for `type Event struct {` in ${eventsFile}."
    else if parsed == [ ] then
      throw "${me}: found no field tags under `type Event struct {` in ${eventsFile}."
    else
      builtins.listToAttrs (map toEntry parsed);

  # `X = "y"` lines of report.go, for `Event: report.X` emit sites.
  reportConsts =
    reportText:
    builtins.listToAttrs (
      map
        (m: {
          name = builtins.elemAt m 0;
          value = builtins.elemAt m 1;
        })
        (
          filter (m: m != null) (
            map (builtins.match "[[:space:]]*([A-Za-z]+)[[:space:]]*=[[:space:]]*\"([a-z_]+)\"") (
              linesOf reportText
            )
          )
        )
    );

  # One entry per emit-site line: { event; file; fields; }.
  emitSites =
    goSources:
    let
      fieldOf = structMap goSources;
      consts = reportConsts (builtins.head (filter (s: s.file == reportFile) goSources)).text;
      resolve =
        file: line: raw:
        let
          q = builtins.match "\"([a-z_]+)\"" raw;
          r = builtins.match "report\\.([A-Za-z]+)" raw;
        in
        if q != null then
          builtins.head q
        else if r != null && consts ? ${builtins.head r} then
          consts.${builtins.head r}
        else
          throw "${me}: ${file}: cannot resolve the event name `${raw}` in: ${line}";
      # A ", Kind: " inside a string value must not read as a field, nor a "}"
      # inside one as the literal's close. Raw strings have no escapes.
      stripStrings =
        s:
        concatStringsSep "" (filter builtins.isString (builtins.split "\"([^\"\\\\]|\\\\.)*\"|`[^`]*`" s));
      parseLine =
        file: line:
        let
          m = builtins.match ".*Event\\{\\{?Event: (\"[a-z_]+\"|[A-Za-z]+\\.[A-Za-z]+)(.*)" line;
          rest = stripStrings (builtins.elemAt m 1);
          # builtins.split interleaves empty-list separators with the pieces.
          pieces = filter builtins.isString (builtins.split ", " rest);
          keyOf =
            piece:
            let
              k = builtins.match "([A-Z][A-Za-z]*): .*" piece;
            in
            if k == null then
              [ ]
            else if builtins.head k == "Event" then
              [ ]
            else if fieldOf ? ${builtins.head k} then
              [ fieldOf.${builtins.head k} ]
            else
              throw "${me}: ${file}: unknown Event field `${builtins.head k}` in: ${line} -- is it missing from ${eventsFile}'s struct, or is this a misparse of a string value?";
        in
        if m == null then
          throw "${me}: ${file}: line has an `Event: ` key but is not an `Event{Event: <name>, ...}` literal: ${line} -- keep emit literals on one line or extend this check."
        else if builtins.match ".*[}][})]*" rest == null then
          throw "${me}: ${file}: the Event literal does not close on this line, so a wrapped field would go unseen: ${line} -- keep emit literals on one line or extend this check."
        else
          {
            inherit file;
            event = resolve file line (builtins.elemAt m 0);
            fields = [ "time" ] ++ builtins.concatMap keyOf pieces;
          };
      foreignEmit =
        file: line:
        throw "${me}: ${file}: builds an Event literal outside the daemon packages: ${line} -- this check only reads emit literals in ${concatStringsSep " and " emitDirs}; move the emit there or extend this check.";
    in
    builtins.concatMap (
      s:
      if elem (builtins.dirOf s.file) emitDirs then
        map (parseLine s.file) (filter (l: hasInfix "Event: " l) (linesOf s.text))
      else
        map (foreignEmit s.file) (
          filter (l: builtins.match ".*[A-Za-z0-9_]\\.Event\\{\\{?Event:.*" l != null) (linesOf s.text)
        )
    ) goSources;

  # ---- comparison --------------------------------------------------------

  list = xs: concatStringsSep ", " (map (x: "`${x}`") xs);
  pronoun = xs: if length xs == 1 then "it" else "them";

  drift =
    { docsText, goSources }:
    let
      rows = docsRows docsText;
      sites = emitSites goSources;
      docNames = sorted (map (r: r.event) rows);
      goNames = setOf (map (s: s.event) sites);
      allNames = setOf (docNames ++ goNames);

      forEvent =
        name:
        let
          docRows = filter (r: r.event == name) rows;
          evSites = filter (s: s.event == name) sites;
          where = concatStringsSep ", " (setOf (map (s: s.file) evSites));
          goFields = setOf (builtins.concatMap (s: s.fields) evSites);
          docFields = if docRows == [ ] then [ ] else (builtins.head docRows).fields;
          onlyDocs = subtractLists goFields docFields;
          onlyGo = subtractLists docFields goFields;
        in
        optional (length docRows > 1)
          "event `${name}` has ${toString (length docRows)} rows in ${docsFile}'s event-stream table -- keep one."
        ++ (
          if docRows == [ ] then
            [
              "event `${name}` is emitted (${where}) but has no row in ${docsFile}'s event-stream table -- add one."
            ]
          else if evSites == [ ] then
            [
              "${docsFile}'s event-stream table lists event `${name}` but no emit site builds it in the daemon packages -- remove the row or restore the emit site."
            ]
          else
            optional (onlyGo != [ ])
              "event `${name}`: Go emit sites (${where}) set ${list onlyGo} but ${docsFile}'s row does not list ${pronoun onlyGo} -- add to the docs row, or drop from the emit literal."
            ++
              optional (onlyDocs != [ ])
                "event `${name}`: ${docsFile}'s row lists ${list onlyDocs} but no Go emit site (${where}) sets ${pronoun onlyDocs} -- drop from the docs row, or restore in the emit literal."
        );
    in
    builtins.concatMap forEvent allNames;

  # Every non-test .go file under cmd/launcher (testdata fixtures are never
  # built), so a new emit file or package is picked up.
  goSources =
    map
      (f: {
        file = removePrefix "${toString ../../.}/" f;
        text = builtins.readFile f;
      })
      (
        filter (f: hasSuffix ".go" f && !hasSuffix "_test.go" f && !hasInfix "/testdata/" f) (
          map toString (pkgs.lib.filesystem.listFilesRecursive ../../cmd/launcher)
        )
      );

  realInputs = {
    docsText = builtins.readFile ../../docs/reference.md;
    inherit goSources;
  };

  mismatches = drift realInputs;

  # ---- rejection cases ---------------------------------------------------

  # A replacement that changes nothing would leave the case feeding `drift` the
  # real inputs, so a reworded source must fail loudly, not pass vacuously.
  editDocs =
    from: to:
    let
      out = builtins.replaceStrings [ from ] [ to ] realInputs.docsText;
    in
    if out == realInputs.docsText then
      throw "${me}: rejection case no longer applies: `${from}` is not in ${docsFile} -- update the case."
    else
      realInputs // { docsText = out; };
  editGo =
    from: to:
    let
      out = map (s: s // { text = builtins.replaceStrings [ from ] [ to ] s.text; }) realInputs.goSources;
    in
    if out == realInputs.goSources then
      throw "${me}: rejection case no longer applies: `${from}` is in no scanned Go source -- update the case."
    else
      realInputs // { goSources = out; };

  # jamRow ends at the when cell, so `dup |` completes the inserted row.
  jamRow = "| `jam` | `time`, `kind`, `revision`, `slot`, `wait`, `reason` | ";

  # Each case's `expect` strings must all appear in the joined messages.
  mismatchCases = [
    {
      # e4dbef87 in reverse: the literal gains what the row lacks.
      name = "event-stream-parity-go-gains-field";
      inputs = editGo "Event{{Event: \"awake_close\", " "Event{{Event: \"awake_close\", Kind: kind, ";
      expect = [
        "`awake_close`"
        "set `kind`"
      ];
    }
    {
      name = "event-stream-parity-go-loses-field";
      inputs = editGo "Kind: kind, Wait: wait.String(), Slot: intPtr(slot)}}" "Kind: kind, Slot: intPtr(slot)}}";
      expect = [
        "`idle`"
        "row lists `wait`"
      ];
    }
    {
      name = "event-stream-parity-docs-gains-field";
      inputs = editDocs "| `awake_open` | `time`, `slot`, `reason` |" "| `awake_open` | `time`, `kind`, `slot`, `reason` |";
      expect = [
        "`awake_open`"
        "row lists `kind`"
      ];
    }
    {
      name = "event-stream-parity-docs-loses-field";
      inputs = editDocs "`issue` or `chore`, `phase`, `pass_log`, `record_id`, `revision`, `slot` |" "`issue` or `chore`, `pass_log`, `record_id`, `revision`, `slot` |";
      expect = [
        "`box`"
        "set `phase`"
      ];
    }
    {
      name = "event-stream-parity-emit-site-renamed";
      inputs = editGo "Event{{Event: \"awake_open\", " "Event{{Event: \"awake_opened\", ";
      expect = [
        "event `awake_opened` is emitted"
        "lists event `awake_open` but no emit site"
      ];
    }
    {
      name = "event-stream-parity-duplicate-docs-row";
      inputs = editDocs jamRow "${jamRow}dup |\n${jamRow}";
      expect = [ "event `jam` has 2 rows" ];
    }
  ];

  # Inputs `drift` must refuse to read rather than guess at.
  throwCases = [
    {
      name = "event-stream-parity-multiline-literal-throws";
      inputs = editGo "Event{{Event: \"jam\", " "Event{{\n\t\t\tEvent: \"jam\", ";
      message = "drift must throw on a multi-line Event literal";
    }
    {
      # A field wrapped onto a continuation line must not be silently dropped.
      name = "event-stream-parity-wrapped-field-throws";
      inputs = editGo "could unblock it\"}}" "could unblock it\",\n\t\t\t\tOutcome: \"x\"}}";
      message = "drift must throw when a literal does not close on its Event: line";
    }
    {
      name = "event-stream-parity-foreign-emit-throws";
      inputs = editGo "EventBox     = \"box\"" "EventBox     = \"box\"\nvar _ = daemon.Event{Event: \"x\"}";
      message = "drift must throw on a daemon.Event literal outside the daemon packages";
    }
    {
      name = "event-stream-parity-aliased-foreign-emit-throws";
      inputs = editGo "EventBox     = \"box\"" "EventBox     = \"box\"\nvar _ = d.Event{Event: \"jam\", Outcome: \"x\"}";
      message = "drift must throw on an aliased Event literal outside the daemon packages";
    }
    {
      name = "event-stream-parity-unknown-field-throws";
      inputs = editGo "Event{{Event: \"idle\", " "Event{{Event: \"idle\", Bogus: 1, ";
      message = "drift must throw on a capitalised key that is not an Event field";
    }
  ];

  mismatchCase = c: {
    inherit (c) name;
    value =
      let
        joined = concatStringsSep "\n" (drift c.inputs);
      in
      assert assertMsg (joined != "") "${c.name}: drift reported no mismatch";
      assert assertMsg (builtins.all (
        e: hasInfix e joined
      ) c.expect) "${c.name}: expected messages containing ${builtins.toJSON c.expect}, got:\n${joined}";
      pkgs.runCommand c.name { } "touch $out";
  };

  # tryEval exposes only success/failure, never which throw fired (see
  # awake-window.nix's same caveat), so `drift`'s message text goes unasserted.
  # The inputs are forced OUTSIDE it: a stale editGo/editDocs throw must fail
  # the build, not count as the rejection under test.
  throwCase = c: {
    inherit (c) name;
    value = builtins.deepSeq c.inputs (
      let
        result = builtins.tryEval (builtins.deepSeq (drift c.inputs) true);
      in
      assert assertMsg (!result.success) "${c.name}: ${c.message}";
      pkgs.runCommand c.name { } "touch $out"
    );
  };
in
{
  # Without this positive case, a `drift` that always reported mismatches would
  # satisfy every rejection case.
  event-stream-parity =
    assert assertMsg (mismatches == [ ])
      "event-stream-parity: ${docsFile}'s daemon event-stream table and the Go emit sites disagree (issue #3765):\n  - ${concatStringsSep "\n  - " mismatches}";
    pkgs.runCommand "event-stream-parity" { } "touch $out";
}
// builtins.listToAttrs (map mismatchCase mismatchCases)
// builtins.listToAttrs (map throwCase throwCases)
