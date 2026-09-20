# Eval-level parse of the DAEMON_AWAKE_WINDOW knob (lib/env-schema.nix's
# daemonAwakeWindow). cmd/launcher/internal/daemon/awake.go's ParseWindow
# parses the same knob at runtime, so the accepted shape and rejections here
# must not diverge from it, with one deliberate asymmetry: ParseWindow trims
# surrounding whitespace before parsing, while this rejects a padded value
# outright. Rejecting more at eval time than at runtime is always safe -- an
# eval-time throw means the value never reaches the daemon at all. Pure
# builtins only, no pkgs.lib, so a bare `nix eval` tests this file without a
# locked nixpkgs.
let
  # Mirrors awake.go's windowFormat: two-digit hour, two-digit minute, a
  # literal '-', one space, then a non-empty zone with no embedded
  # whitespace. builtins.match is an extended regex, which has
  # no \S shorthand, so "not whitespace" is spelled as the POSIX [:space:]
  # class negated -- a plain [^ ] would miss a tab, under-rejecting relative
  # to \S and inverting the header claim above that eval only ever rejects
  # more than runtime.
  windowRe = "([0-9]{2}):([0-9]{2})-([0-9]{2}):([0-9]{2}) ([^[:space:]]+)";

  prefix = "DAEMON_AWAKE_WINDOW: ";

  # builtins.fromJSON rejects a leading-zero numeral ("00", "05" are not
  # valid JSON numbers), which every clock field below 10 is, so
  # two-digit clock fields are decoded digit-by-digit instead.
  digitToInt = {
    "0" = 0;
    "1" = 1;
    "2" = 2;
    "3" = 3;
    "4" = 4;
    "5" = 5;
    "6" = 6;
    "7" = 7;
    "8" = 8;
    "9" = 9;
  };
  twoDigit =
    s: (digitToInt.${builtins.substring 0 1 s} * 10) + digitToInt.${builtins.substring 1 1 s};

  clockMinutes =
    raw: hh: mm:
    let
      h = twoDigit hh;
      m = twoDigit mm;
    in
    if h > 23 then
      throw "${prefix}hour \"${hh}\" out of range 0-23 (in \"${raw}\")"
    else if m > 59 then
      throw "${prefix}minute \"${mm}\" out of range 0-59 (in \"${raw}\")"
    else
      h * 60 + m;
in
{
  # The empty string is the schema default and means no window -- always
  # awake -- mirroring ParseWindow's (nil, nil) result. Nix cannot load the
  # IANA zone database, so unlike awake.go this cannot confirm the zone
  # actually resolves; it only validates the zone token's shape (non-empty,
  # no embedded whitespace, not the literal "Local"). The runtime
  # ParseWindow is what proves the zone resolves, which is also why the Go
  # parse remains the real guarantee for an env-supplied value.
  parse =
    raw:
    if raw == "" then
      null
    else
      let
        m = builtins.match windowRe raw;
      in
      if m == null then
        throw "${prefix}want \"HH:MM-HH:MM Zone\" (e.g. \"22:00-06:00 Europe/London\"), got \"${raw}\""
      else
        let
          hh1 = builtins.elemAt m 0;
          mm1 = builtins.elemAt m 1;
          hh2 = builtins.elemAt m 2;
          mm2 = builtins.elemAt m 3;
          zone = builtins.elemAt m 4;
          start = clockMinutes raw hh1 mm1;
          end = clockMinutes raw hh2 mm2;
        in
        if start == end then
          throw "${prefix}start and end are both ${hh1}:${mm1}: ambiguous between an all-day and a never-open window (in \"${raw}\")"
        else if zone == "Local" then
          throw "${prefix}zone \"Local\" resolves to the host's own zone -- the exact inheritance this knob exists to prevent; use an IANA zone name (e.g. \"Europe/London\", \"UTC\") (in \"${raw}\")"
        else
          {
            inherit start end zone;
          };
}
