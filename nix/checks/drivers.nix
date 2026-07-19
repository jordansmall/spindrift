# Eval-level pins for lib/drivers/default.nix (issue #624): the registry's
# required-attribute shape assertion, on top of nix/checks/bats.nix's use of
# the same renderPreamble output the image bakes in.
{ pkgs, ... }:
let
  driverRegistry = import ../../lib/drivers/default.nix { inherit (pkgs) lib; };
  inherit (pkgs.lib)
    assertMsg
    hasInfix
    filter
    concatStringsSep
    splitString
    imap0
    ;
  # Shared stub-cli fixture (issue #1144): drivers-render-preamble-shape
  # consumes it as-is; drivers-assert-shape-succeeds extends it with the
  # three attrs renderPreamble doesn't read but assertShape requires.
  stubDriverBase = {
    bin = "stub-cli";
    flagsCommon = "--stub-flag --two";
    skillsDirRelative = ".stub/skills";
    outcomeExtractFnBody = "echo stub-outcome\n";
    sessionFlagsFnBody = "echo stub-session\n";
  };
in
{
  drivers-assert-shape-missing-attribute-throws =
    let
      incomplete = {
        name = "incomplete";
        package = pkgs: pkgs.hello;
        bin = "incomplete";
        flagsCommon = "";
        skillsDirRelative = ".incomplete/skills";
        outcomeExtractFnBody = "";
        # sessionFlagsFnBody and agentsJsonTemplate deliberately omitted.
      };
      result = builtins.tryEval (driverRegistry.assertShape "incomplete" incomplete);
    in
    # tryEval exposes only success/failure, never the thrown message text, so
    # this can assert throw/no-throw but not that the message names the
    # Driver and missing attribute (see drivers-assert-shape-succeeds below
    # for the complementary positive-shape case).
    assert assertMsg (
      !result.success
    ) "assertShape must throw when a Driver entry is missing a required attribute";
    pkgs.runCommand "drivers-assert-shape-missing-attribute-throws" { } "touch $out";

  drivers-render-preamble-shape =
    let
      out = driverRegistry.renderPreamble stubDriverBase;
    in
    assert assertMsg (hasInfix "DRIVER_BIN=stub-cli" out)
      "renderPreamble must bake DRIVER_BIN from the Driver entry's bin, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_FLAGS_COMMON='--stub-flag --two'" out)
      "renderPreamble must shell-escape DRIVER_FLAGS_COMMON from the Driver entry's flagsCommon, got: ${out}";
    assert assertMsg (hasInfix "DRIVER_SKILLS_DIR=/home/agent/.stub/skills" out)
      "renderPreamble must bake DRIVER_SKILLS_DIR under /home/agent, got: ${out}";
    assert assertMsg (hasInfix "_driver_extract_outcome() {\necho stub-outcome" out)
      "renderPreamble must fold in the Driver entry's outcomeExtractFnBody, got: ${out}";
    assert assertMsg (hasInfix "_driver_session_flags() {\necho stub-session" out)
      "renderPreamble must fold in the Driver entry's sessionFlagsFnBody, got: ${out}";
    pkgs.runCommand "drivers-render-preamble-shape" { } "touch $out";

  drivers-assert-shape-succeeds =
    let
      complete = stubDriverBase // {
        name = "stub";
        package = pkgs: pkgs.hello;
        agentsJsonTemplate = "{}";
      };
      result = builtins.tryEval (driverRegistry.assertShape "stub" complete);
    in
    assert assertMsg (result.success
    ) "assertShape must not throw when a Driver entry has every required attribute";
    assert assertMsg (
      result.value == complete
    ) "assertShape must return the Driver entry unchanged when it has every required attribute";
    pkgs.runCommand "drivers-assert-shape-succeeds" { } "touch $out";

  # Issue #1609: a Box Driver session must never see the harness's
  # re-invocation-promising tools -- each is a promise the headless runner
  # will not keep (a backgrounded gate + ScheduleWakeup on #1542 lost a run
  # outright). Checked against the real claude entry (not stubDriverBase),
  # since flagsCommon is shared verbatim across the main run, conflict-resolve
  # pass, and fix pass (issue #1609 AC4) -- one flagsCommon, one assertion.
  drivers-claude-blocks-loop-background-affordances =
    let
      claudeEntry = driverRegistry.entries.claude;
      disallowed = [
        "ScheduleWakeup"
        "CronCreate"
        "CronDelete"
        "CronList"
        "RemoteTrigger"
        "Monitor"
      ];
      # entrypoint.sh's DRIVER_FLAGS_COMMON splice is unquoted (whitespace
      # word-split, matching driver-exec/args.go's strings.Fields), so the
      # --disallowedTools value is the single word right after the flag.
      # Split into tokens for exact matching, not hasInfix substring
      # matching, so a typo'd sibling like "ScheduleWakeupX" can't slip a
      # false pass by.
      words = splitString " " claudeEntry.flagsCommon;
      indexedWords = imap0 (i: w: { inherit i w; }) words;
      flagMatches = filter (iw: iw.w == "--disallowedTools") indexedWords;
      flagIndex = if flagMatches == [ ] then null else (builtins.head flagMatches).i;
      deniedTools =
        if flagIndex == null then [ ] else splitString "," (builtins.elemAt words (flagIndex + 1));
      missing = filter (t: !(builtins.elem t deniedTools)) disallowed;
    in
    assert assertMsg (
      flagIndex != null
    ) "claude Driver's flagsCommon must include --disallowedTools, got: ${claudeEntry.flagsCommon}";
    assert assertMsg (missing == [ ])
      "claude Driver's flagsCommon --disallowedTools must deny ${concatStringsSep ", " missing}, got: ${claudeEntry.flagsCommon}";
    pkgs.runCommand "drivers-claude-blocks-loop-background-affordances" { } "touch $out";
}
