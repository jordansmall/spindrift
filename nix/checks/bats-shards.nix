# Shard partition for the `tests/*.bats` suite (issue #2648, slices 1 and 4).
# Needs only `pkgs.lib`, not the `common` bundle the other nix/checks/*.nix
# modules take. Returns the shard assignment bats.nix wires into the per-shard
# `bats-shard-N` derivations, plus eval-only guard checks default.nix merges
# into `sourceChecks`.
{ pkgs, ... }:
let
  inherit (pkgs) lib;

  # Read the directory rather than hand-listing filenames, so a newly added
  # suite is picked up without editing this module.
  testsDir = ../../tests;
  testsDirEntries = builtins.readDir testsDir;
  batsFiles = lib.filterAttrs (
    name: type: type == "regular" && lib.hasSuffix ".bats" name
  ) testsDirEntries;

  # The sort keeps the LPT assignment below reproducible across evaluations:
  # it seeds the tie-break comparator in fileCounts, and fixes the order the
  # coverage check compares against.
  allFiles = builtins.sort (a: b: a < b) (builtins.attrNames batsFiles);

  shardCount = 10;
  shardIndices = lib.range 0 (shardCount - 1);
  shardNames = map (i: "bats-shard-${toString i}") shardIndices;

  # `builtins.match` anchors the whole string, and bats' `@test` always starts
  # at column 0 in this repo, so "@test.*" is exact with no leading-whitespace
  # variant to account for.
  countTests =
    file:
    let
      content = builtins.readFile (testsDir + "/${file}");
      lines = lib.splitString "\n" content;
      isTestLine = line: builtins.match "@test.*" line != null;
    in
    builtins.length (builtins.filter isTestLine lines);

  # Round-robin by file index balances file counts but not test counts: one
  # file, entrypoint-prompt-fragments.bats, carries 80 @test cases against a
  # 1-20 range for the rest, so a shard holding it plus a few mid-size files
  # dominates build time. Bin-pack with greedy LPT instead: heaviest file
  # first, ties broken by filename, into the shard with the smallest total.
  fileCounts = map (file: {
    inherit file;
    count = countTests file;
  }) allFiles;

  # Ties go to the lowest index, matching the seed order of lptFold's
  # `emptyShards` binding below.
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

  # Takes `n` and `counts` instead of closing over the module-level
  # `shardCount`/`fileCounts`, so the synthetic scenarios in
  # `"bats-shard-ceiling-formula-is-safe"` run this same fold rather than a
  # hand-copied stand-in that could drift. The parameter is named `n` so it
  # cannot shadow the module-level `shardCount`.
  lptFold =
    n: counts:
    let
      foldShardIndices = lib.range 0 (n - 1);
      sortedByCountDesc = builtins.sort (
        a: b: if a.count != b.count then a.count > b.count else a.file < b.file
      ) counts;
      emptyShards = map (_: {
        total = 0;
        files = [ ];
      }) foldShardIndices;
    in
    lib.foldl' (
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

  finalShards = lptFold shardCount fileCounts;

  # `shards` is a parameter so the real call site asserts over the partition
  # it ships (`finalShards`) rather than a second fold of the same counts, and
  # so the synthetic scenarios share this one definition of balanced.
  partitionStats =
    n: counts: shards:
    let
      totalTests = builtins.foldl' (acc: fc: acc + fc.count) 0 counts;
      maxFileCount = builtins.foldl' (best: fc: if fc.count > best then fc.count else best) 0 counts;
      # Rounded up so integer division does not undercount. No partition can
      # beat this, nor `maxFileCount`: the heaviest single file cannot be
      # split across shards.
      perfectSplit = (totalTests + n - 1) / n;
      # Classical list-scheduling bound (issue #2764): any greedy
      # min-loaded-shard fold satisfies maxShardTotal <= totalTests/n +
      # maxFileCount, and no true optimal makespan is needed to state it.
      # Graham's bound, the previous formula here, does need one; fed the
      # lower bounds above instead it under-reports and trips on valid folds.
      ceiling = perfectSplit + maxFileCount;
      shardTotals = lib.imap0 (i: s: {
        idx = i;
        inherit (s) total files;
      }) shards;
      maxShardTotal = builtins.foldl' (best: s: if s.total > best then s.total else best) 0 shardTotals;
      overCeiling = builtins.filter (s: s.total > ceiling) shardTotals;
      overCeilingDesc = lib.concatMapStringsSep ", " (
        s:
        "shard ${toString s.idx} (total ${toString s.total}, files: ${lib.concatStringsSep ", " s.files})"
      ) overCeiling;
    in
    # `n` and `shards` arrive independently, so only this assert keeps the
    # passed partition paired with the shard count its `ceiling` is derived
    # for.
    assert builtins.length shards == n;
    {
      inherit
        totalTests
        maxFileCount
        ceiling
        maxShardTotal
        overCeiling
        overCeilingDesc
        ;
    };

  # These deliberately do not read testsDir, so the ceiling formula is tested
  # against shapes the current tests/*.bats directory does not happen to hit.
  syntheticScenarios = [
    {
      # Issue #2764 repro: pigeonhole forces one shard to hold 2 of the 11
      # files, so 20 is unavoidable, yet Graham's bound over the lower-bound
      # proxies gave a ceiling of 15.
      name = "equal-size-files-repro";
      shardCount = 10;
      expectedCeiling = 21;
      counts = map (i: {
        file = "synthetic-${toString i}.bats";
        count = 10;
      }) (lib.range 1 11);
    }
    {
      # The shape that motivated LPT weighting in the first place, described
      # on `fileCounts` above.
      name = "one-large-file-many-tiny";
      shardCount = 4;
      expectedCeiling = 68;
      counts = [
        {
          file = "big.bats";
          count = 50;
        }
      ]
      ++ map (i: {
        file = "tiny-${toString i}.bats";
        count = 1;
      }) (lib.range 1 20);
    }
    {
      # Baseline only. Both the old and the new formula give 20 here, so this
      # case discriminates nothing; the two scenarios above do that.
      name = "evenly-divisible";
      shardCount = 3;
      expectedCeiling = 20;
      counts = map (i: {
        file = "even-${toString i}.bats";
        count = 5;
      }) (lib.range 1 9);
    }
  ];

  # Membership is LPT-assigned, but run order within a shard sorts back to
  # alphabetical, matching the old catch-all `bats tests/` run. LPT's
  # descending-by-count order is an implementation detail of balancing.
  shardFiles = shardIdx: builtins.sort (a: b: a < b) (builtins.elemAt finalShards shardIdx).files;
in
{
  inherit
    shardNames
    shardFiles
    ;

  # Concatenating rather than de-duplicating the shard lists means a double
  # assignment changes the length and fails the equality, not just the "every
  # file present" half. The assert sits in this attribute's own `let ... in
  # assert` so only forcing this derivation forces it; a module-top-level
  # assert would fail every consumer, including default.nix's `.shardNames`.
  "bats-shard-partition-covers-all-suites" =
    let
      unionSorted = builtins.sort (a: b: a < b) (lib.concatMap shardFiles shardIndices);
    in
    assert lib.assertMsg (unionSorted == allFiles)
      "bats shard partition does not cover tests/*.bats exactly: union of shardFiles across all ${toString shardCount} shards (${toString (builtins.length unionSorted)} entries) != tests/*.bats (${toString (builtins.length allFiles)} entries)";
    pkgs.runCommand "bats-shard-partition-covers-all-suites" { } "touch $out";

  # Fold-implementation guard (issue #2764): `ceiling` is a proven upper bound
  # on any correct min-loaded-shard greedy fold, re-derived from the current
  # tests/ directory each eval, so suite growth alone cannot trip it. A
  # non-empty `overCeiling` means a bug in `lptFold`/`minTotalIndex`. On the
  # assert placement, see `"bats-shard-partition-covers-all-suites"` above.
  "bats-shard-partition-is-balanced" =
    let
      stats = partitionStats shardCount fileCounts finalShards;
    in
    assert lib.assertMsg (stats.overCeiling == [ ])
      "bats shard partition is unbalanced: ${stats.overCeilingDesc} exceed the ${toString stats.ceiling}-test ceiling (derived from ${toString stats.totalTests} total tests across ${toString shardCount} shards, largest single file ${toString stats.maxFileCount} tests) -- this ceiling is a proven upper bound for any correct min-loaded-shard fold, so this means a bug in lptFold/minTotalIndex itself, not a suite-balance problem fixable by moving files or raising shardCount";
    pkgs.runCommand "bats-shard-partition-is-balanced" { } "touch $out";

  # Companion guard (issue #2764): the ceiling formula must hold for any shape
  # the fold can face, so this runs it over `syntheticScenarios` and never
  # depends on the live suite's current file sizes. On the assert placement,
  # see `"bats-shard-partition-covers-all-suites"` above.
  "bats-shard-ceiling-formula-is-safe" =
    let
      results = map (
        scenario:
        let
          shards = lptFold scenario.shardCount scenario.counts;
          stats = partitionStats scenario.shardCount scenario.counts shards;
          ceilingOk = stats.ceiling == scenario.expectedCeiling;
          # Same theorem as bats-shard-partition-is-balanced's guard, so it
          # only has teeth against a broken fold. Catching a loose or wrong
          # ceiling is `ceilingOk`'s job.
          safeOk = stats.maxShardTotal <= stats.ceiling;
        in
        {
          inherit (scenario) name expectedCeiling;
          inherit (stats) ceiling maxShardTotal;
          inherit ceilingOk safeOk;
          ok = ceilingOk && safeOk;
        }
      ) syntheticScenarios;
      failing = builtins.filter (r: !r.ok) results;
      failingDesc = lib.concatMapStringsSep "; " (
        r:
        "${r.name}: "
        + lib.concatStringsSep ", " (
          builtins.filter (s: s != null) [
            (
              if r.ceilingOk then
                null
              else
                "ceiling formula gave ${toString r.ceiling}, expected ${toString r.expectedCeiling}"
            )
            (
              if r.safeOk then
                null
              else
                "achieved max shard total ${toString r.maxShardTotal} > ceiling ${toString r.ceiling}"
            )
          ]
        )
      ) failing;
    in
    assert lib.assertMsg (
      failing == [ ]
    ) "bats shard ceiling formula is unsafe for synthetic scenario(s): ${failingDesc}";
    pkgs.runCommand "bats-shard-ceiling-formula-is-safe" { } "touch $out";

  # A broken weighting slips past both guards above: if every file's @test
  # count reads back as 0, the `minTotalIndex` tie-break puts them all in
  # shard 0, coverage still holds, and no shard exceeds the ceiling. This
  # guard checks that shape without consulting the counts. On the assert
  # placement, see `"bats-shard-partition-covers-all-suites"` above.
  "bats-shard-partition-fills-every-shard" =
    let
      emptyShardIndices = builtins.filter (i: (builtins.elemAt finalShards i).files == [ ]) shardIndices;
    in
    assert lib.assertMsg (builtins.length allFiles < shardCount || emptyShardIndices == [ ])
      "bats shard partition left shard(s) ${builtins.toJSON emptyShardIndices} with zero files assigned even though ${toString (builtins.length allFiles)} tests/*.bats files exist for only ${toString shardCount} shards -- likely a broken per-file weighting (e.g. every file's @test count reading back as 0) collapsing the LPT tie-break onto a single shard";
    pkgs.runCommand "bats-shard-partition-fills-every-shard" { } "touch $out";
}
