# Which packages get a generated typed knob reader (ADR 0068 decision 3, issue
# #5636). Generic over registries: each one is `{ source; rows; goPackageDir; }`
# where `goPackageDir` maps a row name to the repo-relative Go package directory
# holding that row's code. `present` lists a reader per registry row that
# declares knobs; `fixture` is the standalone test-only target exercising every
# reader shape, kept out of `present` so it reads as what it is. `generated` is
# `present ++ [ fixture ]`, the list regen writes and the drift check compares.
# `absent` lists the reader paths of rows that declare none, which regen deletes
# so a row losing its knobs leaves no stale file.
let
  registries = import ./row-knob-registries.nix;
  readerFile = "knobs_gen.go";
  entries = builtins.concatMap (
    registry:
    map (row: {
      dir = registry.goPackageDir row.name;
      inherit row;
      source = "${registry.source}/${row.name}.nix";
    }) registry.rows
  );
  hasKnobs = entry: (entry.row.knobs or { }) != { };
  present = map (entry: {
    path = "${entry.dir}/${readerFile}";
    package = baseNameOf entry.dir;
    inherit (entry) source;
    knobs = entry.row.knobs;
  }) (builtins.filter hasKnobs (entries registries));
  fixture = {
    path = "cmd/launcher/internal/rowknobs/fixture/${readerFile}";
    package = "fixture";
    source = "lib/row-knob-fixture.nix";
    knobs = (import ./row-knob-fixture.nix).knobs;
  };
in
{
  inherit present fixture;
  generated = present ++ [ fixture ];
  absent = map (entry: "${entry.dir}/${readerFile}") (
    builtins.filter (entry: !hasKnobs entry) (entries registries)
  );
}
