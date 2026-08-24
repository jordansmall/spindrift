# Shard partition for the `tests/*.bats` suite (issue #2648, slices 1 and 4).
# Pure and self-contained: only needs `pkgs.lib`, not the full `common` bundle
# the other nix/checks/*.nix modules take. Returns both the shard-assignment
# data (`shardNames`/`shardFiles`) that bats.nix wires into the per-shard
# `bats-shard-N` derivations, and a handful of eval-only guard check
# derivations (coverage/balance/no-empty-shard) that default.nix merges into
# `sourceChecks`.
{ pkgs, ... }:
let
  inherit (pkgs) lib;

  # Enumerate tests/*.bats straight from the directory listing (relative to
  # this file, nix/checks/) rather than hand-listing filenames, so a newly
  # added suite is picked up automatically without editing this module.
  testsDir = ../../tests;
  testsDirEntries = builtins.readDir testsDir;
  batsFiles = lib.filterAttrs (
    name: type: type == "regular" && lib.hasSuffix ".bats" name
  ) testsDirEntries;

  # Deterministic ordering: sort matters for the LPT assignment below to be
  # stable/reproducible across evaluations (it seeds the tie-break comparator
  # in fileCounts, and gives allFiles itself a fixed order for the coverage
  # check).
  allFiles = builtins.sort (a: b: a < b) (builtins.attrNames batsFiles);

  shardCount = 10;
  shardIndices = lib.range 0 (shardCount - 1);
  shardNames = map (i: "bats-shard-${toString i}") shardIndices;

  # Test-count-balanced partition (slice 4): round-robin by file index is
  # file-count-balanced but not test-count-balanced -- one file,
  # entrypoint-prompt-fragments.bats, alone carries 80 @test cases against a
  # 1-20 range for most others, so a naive round-robin can land several
  # mid-size files in the same shard as that one outlier and blow past every
  # other shard's build time. Count each file's `@test` cases and bin-pack
  # with greedy LPT (longest-processing-time-first): sort files by descending
  # test count (ties broken by filename, ascending, for determinism) and
  # repeatedly drop the next-heaviest file into whichever shard currently
  # holds the smallest running total.
  #
  # bats' `@test` keyword always starts at column 0 in this repo (verified
  # against every tests/*.bats file), and `builtins.match` is always fully
  # anchored/whole-string by definition, not just at the front -- so matching
  # each line whole against "@test.*" is exact, no leading-whitespace variant
  # to account for.
  countTests =
    file:
    let
      content = builtins.readFile (testsDir + "/${file}");
      lines = lib.splitString "\n" content;
      isTestLine = line: builtins.match "@test.*" line != null;
    in
    builtins.length (builtins.filter isTestLine lines);

  fileCounts = map (file: {
    inherit file;
    count = countTests file;
  }) allFiles;

  sortedByCountDesc = builtins.sort (
    a: b: if a.count != b.count then a.count > b.count else a.file < b.file
  ) fileCounts;

  emptyShards = map (_: {
    total = 0;
    files = [ ];
  }) shardIndices;

  # Index of the shard with the smallest running total in `shards` (first
  # such index wins ties, matching the emptyShards seed order).
  minTotalIndex =
    shards:
    let
      indexed = lib.imap0 (i: s: {
        inherit i;
        inherit (s) total;
      }) shards;
      lowest = lib.foldl' (
        best: entry: if entry.total < best.total then entry else best
      ) (builtins.head indexed) (builtins.tail indexed);
    in
    lowest.i;

  finalShards = lib.foldl' (
    shards: entry:
    let
      idx = minTotalIndex shards;
      target = builtins.elemAt shards idx;
      updated = {
        total = target.total + entry.count;
        files = target.files ++ [ entry.file ];
      };
    in
    lib.imap0 (i: s: if i == idx then updated else s) shards
  ) emptyShards sortedByCountDesc;

  # Shard *membership* is LPT-assigned (above) for test-count balance, but the
  # run *order* within a shard is sorted back to alphabetical here, matching
  # the old single catch-all `bats tests/` run's directory-listing order --
  # LPT's descending-by-count assignment order is an implementation detail of
  # balancing, not something the suite's run order should visibly change.
  shardFiles = shardIdx: builtins.sort (a: b: a < b) (builtins.elemAt finalShards shardIdx).files;
in
{
  inherit
    shardNames
    shardFiles
    ;

  # Coverage self-check (mirrors the eval-only assert-then-runCommand pattern
  # at nix/checks/default.nix's linux-only-check-names-exist /
  # checks-inbox-excludes-image-checks): the union of every shard's files,
  # sorted, must equal `allFiles` exactly -- no suite dropped, no suite
  # double-assigned. Concatenating (rather than de-duplicating) the shard
  # lists before sorting means a duplicate assignment changes the list length
  # and so still fails the equality check, not just the "every file present"
  # half.
  #
  # The assert lives inside this attribute's own `let ... in assert ...;`
  # (not a module-top-level `assert` ahead of the returned attrset) so that
  # forcing *this* derivation is the only way to force *this* assert --
  # forcing an unrelated attribute of this module's output (e.g.
  # `.shardNames`, `.shardFiles`, or the sibling guards below)
  # never touches it. A module-top-level `assert A; assert B; { ... }` body
  # would instead force both A and B just to reach WHNF of the returned
  # attrset, so any consumer touching any single attribute -- including
  # default.nix:22's `.shardNames` -- would fail the whole module's
  # evaluation on either guard tripping, not just the one check that should
  # go red.
  "bats-shard-partition-covers-all-suites" =
    let
      unionSorted = builtins.sort (a: b: a < b) (lib.concatMap shardFiles shardIndices);
    in
    assert lib.assertMsg (unionSorted == allFiles)
      "bats shard partition does not cover tests/*.bats exactly: union of shardFiles across all ${toString shardCount} shards (${toString (builtins.length unionSorted)} entries) != tests/*.bats (${toString (builtins.length allFiles)} entries)";
    pkgs.runCommand "bats-shard-partition-covers-all-suites" { } "touch $out";

  # Regression guard (slice 4): every shard's total @test count must sit
  # under a ceiling derived from the *current* tests/ directory every eval,
  # not a hardcoded constant -- a fixed constant goes unsatisfiable by any
  # partition once the suite grows past it (e.g. entrypoint-prompt-
  # fragments.bats alone already carries 80 @test cases; 11 more in that one
  # file, or ~40% overall growth, would brick a fixed ceiling of 90 no matter
  # how well-balanced the partition is), bricking flake eval for a module
  # whose whole point is that adding a tests/*.bats file needs no edit here.
  #
  # See the comment on `"bats-shard-partition-covers-all-suites"` above for
  # why the assert lives inside this attribute's own `let ... in assert`
  # rather than a module-top-level assert chain.
  "bats-shard-partition-is-balanced" =
    let
      totalTests = builtins.foldl' (acc: fc: acc + fc.count) 0 fileCounts;
      maxFileCount = builtins.foldl' (best: fc: if fc.count > best then fc.count else best) 0 fileCounts;
      # Lower bound no partition can beat: a perfectly even split of the
      # total (rounded up), or the single largest file's own count -- one
      # file can't be split across shards, so no shard can ever do better
      # than whichever file is heaviest.
      perfectSplit = (totalTests + shardCount - 1) / shardCount; # ceil(totalTests / shardCount)
      optimal = if maxFileCount > perfectSplit then maxFileCount else perfectSplit;
      # Greedy LPT's textbook worst-case bound: makespan <= (4/3 - 1/(3 *
      # shardCount)) * optimal. Rounding that up to ceil(4 * optimal / 3)
      # drops the (always-positive) "- 1/(3*shardCount)" term, which only
      # tightens the true bound further, so this ceiling is always >= the
      # guaranteed LPT worst case -- any correct LPT partition of whatever
      # tests/ looks like *today* is guaranteed to pass, and the ceiling
      # scales with the suite automatically instead of needing a hand-tuned
      # constant revisited on every growth spurt.
      ceiling = (4 * optimal + 2) / 3; # ceil(4 * optimal / 3)
      shardTotals = lib.imap0 (i: s: {
        idx = i;
        inherit (s) total;
      }) finalShards;
      overCeiling = builtins.filter (s: s.total > ceiling) shardTotals;
      # A file whose own count already exceeds the ceiling can never be fixed
      # by rebalancing -- it alone blows the ceiling regardless of which
      # shard it lands in -- so name it explicitly and point at the only two
      # real fixes (grow shardCount, or split the file).
      oversizedFiles = builtins.filter (fc: fc.count > ceiling) fileCounts;
      overCeilingDesc = lib.concatMapStringsSep ", " (
        s: "shard ${toString s.idx} (total ${toString s.total})"
      ) overCeiling;
      oversizedFilesDesc =
        if oversizedFiles == [ ] then
          ""
        else
          "; tests/"
          + lib.concatMapStringsSep ", tests/" (
            fc: "${fc.file} alone has ${toString fc.count} tests"
          ) oversizedFiles
          + " -- over the ceiling on its own, so no rebalancing can fix this, only raising shardCount or splitting that file will";
    in
    assert lib.assertMsg (overCeiling == [ ])
      "bats shard partition is unbalanced: ${overCeilingDesc} exceed the ${toString ceiling}-test ceiling (derived from ${toString totalTests} total tests across ${toString shardCount} shards, largest single file ${toString maxFileCount} tests)${oversizedFilesDesc}";
    pkgs.runCommand "bats-shard-partition-is-balanced" { } "touch $out";

  # Second, independent balance guard: the LPT weighting above (fileCounts /
  # countTests) could itself be broken (e.g. every file's `@test` count
  # somehow reading back as 0) without either guard above catching it -- if
  # every file ties at count 0, the `minTotalIndex` tie-break (lowest index
  # wins) puts every single file in shard 0, leaving every other shard empty;
  # coverage still holds (every file is assigned exactly once) and "0 >
  # ceiling" is false for every shard, so neither guard above notices. Catch
  # that shape directly and independently of the weighting: whenever there
  # are at least as many files as shards, no shard may be left with zero
  # files.
  #
  # See the comment on `"bats-shard-partition-covers-all-suites"` above for
  # why the assert lives inside this attribute's own `let ... in assert`
  # rather than a module-top-level assert chain.
  "bats-shard-partition-fills-every-shard" =
    let
      emptyShardIndices = builtins.filter (i: (builtins.elemAt finalShards i).files == [ ]) shardIndices;
    in
    assert lib.assertMsg (builtins.length allFiles < shardCount || emptyShardIndices == [ ])
      "bats shard partition left shard(s) ${builtins.toJSON emptyShardIndices} with zero files assigned even though ${toString (builtins.length allFiles)} tests/*.bats files exist for only ${toString shardCount} shards -- likely a broken per-file weighting (e.g. every file's @test count reading back as 0) collapsing the LPT tie-break onto a single shard";
    pkgs.runCommand "bats-shard-partition-fills-every-shard" { } "touch $out";
}
