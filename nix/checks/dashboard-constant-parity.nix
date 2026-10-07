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
    "cmd/launcher/internal/daemon/events_file.go"
    "cmd/launcher/internal/report/report.go"
  ] (file: builtins.readFile (../.. + "/${file}"));

  # Dashboard side is the mirror; daemon side is the source of truth.
  # `srcIdent` defaults to `ident`.
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
  ];

  # Matches a declaration line only (`const x = v`, in-block `x = v`, or typed
  # `X T = v`), so uses of the name elsewhere cannot satisfy it.
  declPattern =
    ident:
    "[[:space:]]*(const[[:space:]]+)?${ident}[[:space:]]+([A-Za-z_][A-Za-z0-9_]*[[:space:]]+)?=[[:space:]]*(\"[^\"]*\"|[0-9]+)[[:space:]]*(//.*)?";

  # Returns the literal text assigned to `ident` in `src`. Throws on zero or
  # several declarations rather than yielding null, which would let two missing
  # identifiers compare equal.
  literalOf =
    file: src: ident:
    let
      hits = builtins.filter (m: m != null) (
        map (builtins.match (declPattern ident)) (splitString "\n" src)
      );
    in
    assert assertMsg (builtins.length hits == 1)
      "${file}: expected exactly one literal declaration of `${ident}`, found ${toString (builtins.length hits)}";
    builtins.elemAt (builtins.head hits) 2;

  # Takes the source table as an argument so the -regression check can feed
  # doctored copies. Returns the list of pinned literals.
  assertParity =
    srcs:
    map (
      p:
      let
        srcIdent = p.srcIdent or p.ident;
        dash = literalOf p.dash srcs.${p.dash} p.ident;
        src = literalOf p.src srcs.${p.src} srcIdent;
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
      doctor =
        file: ident: rewrite:
        sources
        // {
          ${file} = concatStringsSep "\n" (
            map (l: if builtins.match (declPattern ident) l == null then l else rewrite l) (
              splitString "\n" sources.${file}
            )
          );
        };
      drift =
        file: ident:
        let
          lit = literalOf file sources.${file} ident;
          drifted =
            if hasPrefix "\"" lit then "\"drifted-${removePrefix "\"" lit}" else toString (toInt lit + 1);
        in
        doctor file ident (replaceStrings [ lit ] [ drifted ]);
      rename = file: ident: doctor file ident (replaceStrings [ ident ] [ "${ident}Renamed" ]);
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
