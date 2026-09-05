# Shard partition for the `tests/*.bats` suite (issue #2648, slices 1 and 4).
# Pure and self-contained: only needs `pkgs.lib`, not the full `common` bundle
# the other nix/checks/*.nix modules take. Returns both the shard-assignment
# data (`shardNames`/`shardFiles`) that bats.nix wires into the per-shard
# `bats-shard-N` derivations, and a handful of eval-only guard check
# derivations (coverage, balance, ceiling-formula safety, and no-empty-shard)
# that default.nix merges into `sourceChecks`.
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

  # Index of the shard with the smallest running total in `shards` (first
  # such index wins ties, matching the seed order of lptFold's own
  # `emptyShards` binding below).
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

  # The LPT bin-packing fold itself, generalized over an `n` shard count and
  # a `counts` list of `{ file, count }` attrsets instead of closing over the
  # module-level `shardCount`/`fileCounts` -- so both the real tests/*.bats
  # partition below and the synthetic scenarios in
  # `"bats-shard-ceiling-formula-is-safe"` run the exact same fold instead of
  # a hand-copied stand-in that could silently drift from real behavior.
  # Parameter named `n`, not `shardCount`, so it can't shadow the
  # module-level `shardCount`/`shardIndices` bindings above.
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

  # Stats over an already-folded `shards` partition, taken as a parameter so
  # the real-suite call site can pass `finalShards` and assert over the
  # partition it actually ships rather than a second fold of the same counts.
  # Generalized the same way as `lptFold` itself so both that partition and
  # the synthetic scenarios in `"bats-shard-ceiling-formula-is-safe"` share
  # one implementation of "what does balanced even mean" instead of two that
  # could drift apart.
  partitionStats =
    n: counts: shards:
    let
      totalTests = builtins.foldl' (acc: fc: acc + fc.count) 0 counts;
      maxFileCount = builtins.foldl' (best: fc: if fc.count > best then fc.count else best) 0 counts;
      # Perfectly even split of the total, rounded up so integer division
      # doesn't undercount. One of the two lower-bound terms `ceiling`
      # combines below with `maxFileCount` (no partition can beat either:
      # not the even split, and not the heaviest single file, which can't
      # be split across shards).
      perfectSplit = (totalTests + n - 1) / n; # ceil(totalTests / n)
      # Classical list-scheduling bound, safe for any greedy
      # min-loaded-shard assignment order (not just LPT specifically) and
      # not dependent on knowing the true optimal makespan (issue #2764):
      # take the shard `i` that ends up with the max total, and the last
      # file `j` the fold placed into it. Because the fold always assigns
      # the next file to whichever shard currently has the smallest running
      # total, every other shard's running total at the moment `j` was
      # placed was >= shard i's total *before* `j` was added. Totals only
      # grow from there, so summing that inequality across all `n` shards
      # gives `totalTests >= n * (finalMaxTotal - countOf(j))`.
      # `countOf(j) <= maxFileCount` (the largest file's own count), so
      # `finalMaxTotal <= totalTests/n + maxFileCount`. `perfectSplit =
      # ceil(totalTests/n) >= totalTests/n` keeps the ceiling
      # integer-safe: `ceiling = perfectSplit + maxFileCount`. Unlike
      # Graham's-bound (the previous formula here), this derivation never
      # assumes it is fed the true optimal makespan -- `perfectSplit`/
      # `maxFileCount` are only lower bounds on true OPT, and Graham's bound
      # is unsound when applied to a lower-bound proxy instead of true OPT
      # (see issue #2764 for a concrete repro: 11 equal-size files into 10
      # shards force an unavoidable makespan of 20, but Graham's bound over
      # the lower-bound proxy produced a ceiling of 15).
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
    # `n` and `shards` now arrive independently, so nothing but this assert
    # keeps the passed partition paired with the shard count its `ceiling` is
    # derived for -- the internal fold this helper used to run guaranteed that
    # pairing by construction.
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

  # Synthetic scenarios for `"bats-shard-ceiling-formula-is-safe"` -- do not
  # read testsDir at all, so this check exercises the ceiling formula against
  # shapes the *current* tests/*.bats directory doesn't happen to hit today,
  # rather than only ever being as strong as whatever partition risk
  # tests/*.bats currently poses.
  syntheticScenarios = [
    {
      # issue #2764 repro: 11 equal-size files (10 tests each) into 10
      # shards. Pigeonhole forces one shard to hold 2 files (total 20); true
      # OPT is exactly 20, but perfectSplit/maxFileCount (both lower bounds,
      # not true OPT) fed into Graham's-bound math produced a ceiling of 15
      # -- tighter than the unavoidable optimum.
      name = "equal-size-files-repro";
      shardCount = 10;
      expectedCeiling = 21;
      counts = map (i: {
        file = "synthetic-${toString i}.bats";
        count = 10;
      }) (lib.range 1 11);
    }
    {
      # One large outlier file plus many tiny ones -- the shape that
      # motivated LPT weighting in the first place (see the module-level
      # comment on `fileCounts` above).
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
      # A shape that already divides evenly (9 equal files into 3 shards, 3
      # files each): baseline sanity check pinning the formula's ceiling
      # value (perfectSplit + maxFileCount = 15 + 5 = 20) on a case neither
      # formula ever struggled with. It does not discriminate between the
      # old and new formula (both give 20 here) and asserts nothing about
      # looseness -- equal-size-files-repro and one-large-file-many-tiny
      # above are what actually discriminate.
      name = "evenly-divisible";
      shardCount = 3;
      expectedCeiling = 20;
      counts = map (i: {
        file = "even-${toString i}.bats";
        count = 5;
      }) (lib.range 1 9);
    }
  ];

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

  # Fold-implementation guard (issue #2764): `ceiling = perfectSplit +
  # maxFileCount` (see `partitionStats` above) is a proven upper bound on
  # *any* correct min-loaded-shard greedy fold, derived fresh from the
  # *current* tests/ directory every eval rather than a hardcoded constant.
  # Unlike the old Graham's-bound formula, it can no longer trip from suite
  # growth or composition alone -- the only way `overCeiling` comes out
  # non-empty is a bug in `lptFold`/`minTotalIndex` itself.
  #
  # See the comment on `"bats-shard-partition-covers-all-suites"` above for
  # why the assert lives inside this attribute's own `let ... in assert`
  # rather than a module-top-level assert chain.
  "bats-shard-partition-is-balanced" =
    let
      stats = partitionStats shardCount fileCounts finalShards;
    in
    assert lib.assertMsg (stats.overCeiling == [ ])
      "bats shard partition is unbalanced: ${stats.overCeilingDesc} exceed the ${toString stats.ceiling}-test ceiling (derived from ${toString stats.totalTests} total tests across ${toString shardCount} shards, largest single file ${toString stats.maxFileCount} tests) -- this ceiling is a proven upper bound for any correct min-loaded-shard fold, so this means a bug in lptFold/minTotalIndex itself, not a suite-balance problem fixable by moving files or raising shardCount";
    pkgs.runCommand "bats-shard-partition-is-balanced" { } "touch $out";

  # Companion guard (issue #2764): the ceiling formula in `partitionStats`
  # above must be safe for *any* shape the LPT fold can face, not just
  # whatever tests/*.bats happens to look like today -- exercise it against
  # hardcoded synthetic scenarios (`syntheticScenarios` above) that don't
  # read testsDir at all, so this check's coverage of the formula's edge
  # cases never depends on the live suite's current file sizes.
  #
  # See the comment on `"bats-shard-partition-covers-all-suites"` above for
  # why the assert lives inside this attribute's own `let ... in assert`
  # rather than a module-top-level assert chain.
  "bats-shard-ceiling-formula-is-safe" =
    let
      results = map (
        scenario:
        let
          shards = lptFold scenario.shardCount scenario.counts;
          stats = partitionStats scenario.shardCount scenario.counts shards;
          ceilingOk = stats.ceiling == scenario.expectedCeiling;
          # Restates the same proven-upper-bound theorem as
          # bats-shard-partition-is-balanced's guard, so this only has teeth
          # against a broken fold -- it can't catch a loose or wrong
          # ceiling, which is `ceilingOk`'s job.
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
