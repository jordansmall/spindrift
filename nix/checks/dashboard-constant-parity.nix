# Pins the Dashboard's hand-mirrored constants against the daemon's (issue
# #4722). The Dashboard mirrors the daemon's published shapes without importing
# the launcher module (ADR 0060), so nothing but this check stops one side
# drifting from the other.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    genAttrs
    hasPrefix
    removePrefix
    replaceStrings
    splitString
    toInt
    ;

  sources = genAttrs [
    "dashboard/status.go"
    "dashboard/history.go"
    "cmd/launcher/internal/daemon/status.go"
    "cmd/launcher/internal/daemon/halt.go"
    "cmd/launcher/internal/daemon/events_file.go"
    "cmd/launcher/internal/report/report.go"
  ] (file: builtins.readFile (../.. + "/${file}"));

  # Dashboard side is the mirror; daemon side is the source of truth.
  # `srcIdent` defaults to `ident`. `srcPattern` replaces the source-side
  # declaration regex for a literal that is a table-row field rather than a
  # declaration; its first capture group is the literal.
  pairs = [
    {
      dash = "dashboard/status.go";
      src = "cmd/launcher/internal/daemon/status.go";
      ident = "statusFileName";
    }
    {
      dash = "dashboard/status.go";
      src = "cmd/launcher/internal/daemon/status.go";
      ident = "statusSchema";
    }
    {
      dash = "dashboard/history.go";
      src = "cmd/launcher/internal/daemon/events_file.go";
      ident = "eventsFileName";
    }
    {
      dash = "dashboard/history.go";
      src = "cmd/launcher/internal/daemon/events_file.go";
      ident = "rotatedSuffix";
    }
    {
      dash = "dashboard/status.go";
      src = "cmd/launcher/internal/daemon/status.go";
      ident = "stateHalted";
      srcIdent = "StateHalted";
    }
    {
      dash = "dashboard/status.go";
      src = "cmd/launcher/internal/report/report.go";
      ident = "nextDueOnTipMove";
      srcIdent = "NextDueOnTipMove";
    }
    {
      dash = "dashboard/status.go";
      src = "cmd/launcher/internal/daemon/halt.go";
      ident = "haltReasonSelfChanged";
      srcIdent = "HaltSelfChanged";
      srcPattern = haltRowReasonPattern "HaltSelfChanged";
    }
  ]
  ++
    map
      (phase: {
        dash = "dashboard/status.go";
        src = "cmd/launcher/internal/daemon/status.go";
        ident = "selfUpdate${phase}";
        srcIdent = "SelfUpdate${phase}";
      })
      [
        "Building"
        "Built"
        "Waiting"
        "Restarting"
      ];

  # Matches a declaration line only (`const x = v`, in-block `x = v`, or typed
  # `X T = v`), so uses of the name elsewhere cannot satisfy it.
  declPattern =
    ident:
    "[[:space:]]*(const[[:space:]]+)?${ident}[[:space:]]+([A-Za-z_][A-Za-z0-9_]*[[:space:]]+)?=[[:space:]]*(\"[^\"]*\"|[0-9]+)[[:space:]]*(//.*)?";

  # Matches the `reason:` field of a haltRenderings row (halt.go), a map-table
  # entry rather than a declaration. The one capture group is the literal.
  # `detailed: true` is pinned because the Dashboard's `self-changed:` prefix
  # match only holds while the row renders a detail suffix.
  haltRowReasonPattern =
    ident:
    "[[:space:]]*${ident}:[[:space:]]*\\{name:[[:space:]]*\"${ident}\",[[:space:]]*reason:[[:space:]]*(\"[^\"]*\"),[[:space:]]*detailed:[[:space:]]*true,.*";

  # Returns the literal text captured by `pattern` (group `group`) in `src`.
  # Throws on zero or several matching lines rather than yielding null, which
  # would let two missing identifiers compare equal.
  literalAt =
    file: src: ident: pattern: group:
    let
      hits = builtins.filter (m: m != null) (map (builtins.match pattern) (splitString "\n" src));
    in
    assert assertMsg (builtins.length hits == 1)
      "${file}: expected exactly one line matching `${ident}`, found ${toString (builtins.length hits)}";
    builtins.elemAt (builtins.head hits) group;

  literalOf =
    file: src: ident:
    literalAt file src ident (declPattern ident) 2;

  # Takes the source table as an argument so the -regression check can feed
  # doctored copies. Returns the list of pinned literals.
  assertParity =
    srcs:
    map (
      p:
      let
        srcIdent = p.srcIdent or p.ident;
        dash = literalOf p.dash srcs.${p.dash} p.ident;
        src =
          if p ? srcPattern then
            literalAt p.src srcs.${p.src} srcIdent p.srcPattern 0
          else
            literalOf p.src srcs.${p.src} srcIdent;
      in
      assert assertMsg (dash == src)
        "${p.dash} `${p.ident}` = ${dash} drifted from ${p.src} `${srcIdent}` = ${src}: the Dashboard mirrors the daemon (ADR 0060) and must change with it";
      dash
    ) pairs;
in
{
  dashboard-constant-parity = builtins.seq (builtins.deepSeq (assertParity sources) null) (
    pkgs.runCommand "dashboard-constant-parity" { } "touch $out"
  );

  dashboard-constant-parity-regression =
    let
      # Rewrites only the declaration line of `ident`, deriving what it changes
      # from the source itself, so a bump made on both sides keeps every
      # fixture matching.
      doctorMatching =
        file: pattern: rewrite:
        sources
        // {
          ${file} = concatStringsSep "\n" (
            map (l: if builtins.match pattern l == null then l else rewrite l) (
              splitString "\n" sources.${file}
            )
          );
        };
      doctor = file: ident: doctorMatching file (declPattern ident);
      drift =
        file: ident:
        let
          lit = literalOf file sources.${file} ident;
          drifted =
            if hasPrefix "\"" lit then "\"drifted-${removePrefix "\"" lit}" else toString (toInt lit + 1);
        in
        doctor file ident (replaceStrings [ lit ] [ drifted ]);
      rename = file: ident: doctor file ident (replaceStrings [ ident ] [ "${ident}Renamed" ]);
      haltRowPair = builtins.head (builtins.filter (p: p ? srcPattern) pairs);
      haltRowFile = haltRowPair.src;
      haltRowPattern = haltRowPair.srcPattern;
      haltRowLiteral = literalAt haltRowFile sources.${haltRowFile} "HaltSelfChanged" haltRowPattern 0;
      fixtures = [
        {
          name = "the Dashboard's statusSchema bumped without the daemon";
          doctored = drift "dashboard/status.go" "statusSchema";
          file = "dashboard/status.go";
        }
        {
          name = "the daemon's statusFileName renamed without the Dashboard";
          doctored = drift "cmd/launcher/internal/daemon/status.go" "statusFileName";
          file = "cmd/launcher/internal/daemon/status.go";
        }
        {
          name = "the Dashboard's in-block rotatedSuffix changed";
          doctored = drift "dashboard/history.go" "rotatedSuffix";
          file = "dashboard/history.go";
        }
        {
          name = "the daemon's typed StateHalted renamed (missing identifier)";
          doctored = rename "cmd/launcher/internal/daemon/status.go" "StateHalted";
          file = "cmd/launcher/internal/daemon/status.go";
        }
        {
          name = "the Dashboard's nextDueOnTipMove renamed (missing identifier)";
          doctored = rename "dashboard/status.go" "nextDueOnTipMove";
          file = "dashboard/status.go";
        }
        {
          name = "the daemon's SelfUpdateWaiting phase changed without the Dashboard";
          doctored = drift "cmd/launcher/internal/daemon/status.go" "SelfUpdateWaiting";
          file = "cmd/launcher/internal/daemon/status.go";
        }
        {
          name = "the daemon's HaltSelfChanged row reason changed without the Dashboard";
          doctored = doctorMatching haltRowFile haltRowPattern (
            replaceStrings [ haltRowLiteral ] [ "\"drifted-self-changed\"" ]
          );
          file = haltRowFile;
        }
      ];
      # A fixture that changes nothing would pass vacuously.
      check =
        f:
        assert assertMsg (
          f.doctored.${f.file} != sources.${f.file}
        ) "dashboard-constant-parity-regression: the fixture for ${f.name} left ${f.file} unchanged";
        assert assertMsg (!(builtins.tryEval (builtins.deepSeq (assertParity f.doctored) null)).success)
          "dashboard-constant-parity-regression: expected assertParity to reject ${f.name}, but it evaluated successfully";
        true;
    in
    assert builtins.all check fixtures;
    pkgs.runCommand "dashboard-constant-parity-regression" { } "touch $out";
}
