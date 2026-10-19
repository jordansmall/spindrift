# The tuning Chore row (ADR 0062, ADR 0070): catalog data plus the env knobs it
# owns (issue #5700). lib/chore-catalog.nix documents the fields and fixes this
# row's list position; lib/row-knobs.nix folds `knobs` into lib/env-schema.nix.
{
  name = "tuning";
  classList = [
    "cost-waste"
    "quality-regression"
    "prompt-gap"
    "model-fit"
    "knob-tuning"
    "evidence-gap"
  ];
  promotionClasses = [ ];
  patchClasses = [ ];
  scopeSource = "records";
  every = "24h";
  findingLabel = "agent-tuning-finding";
  mergeByHand = true;
  minRecordsKnob = "butlerTuningMinRecords";
  knobs = {
    butlerTuningMinRecords = {
      env = "BUTLER_TUNING_MIN_RECORDS";
      group = "dispatch";
      default = 20;
      doc = "fewest new settled Dispatch Records since the tuning Chore's last sweep before it is due (ADR 0062), alongside its BUTLER_EVERY interval (tuning default 24h); host-only -- the Box never sees this value";
      flakeOption = true;
      legacySettingsExempt = true;
      intKind = "positive";
      nixSubPath = "butler.tuningMinRecords";
      boxEnv = false;
    };
    butlerTuningMinSample = {
      env = "BUTLER_TUNING_MIN_SAMPLE";
      group = "dispatch";
      default = 15;
      doc = "per-row sample size n under which the tuning Chore's digest marks a row thin (ADR 0062); host-only -- the Box never sees this value";
      flakeOption = true;
      legacySettingsExempt = true;
      intKind = "positive";
      nixSubPath = "butler.tuningMinSample";
      boxEnv = false;
    };
    butlerTuningCooldown = {
      env = "BUTLER_TUNING_COOLDOWN";
      group = "dispatch";
      default = "720h";
      doc = "a Go time.ParseDuration string (no day unit: 30 days is 720h): how long a tuning finding closed as not planned keeps suppressing a refile of the same pattern, keyed `<class>:<target file>:<metric>` (ADR 0062); after it the pattern may be filed again. An open or closed-completed match always suppresses, and code Chores keep permanent suppression. 0 means a not-planned close never suppresses; negative is rejected. Forgejo reports no close reason, so there every closed tuning finding keeps suppressing permanently; host-only -- the Box never sees this value. Validated by the launcher at `spindrift butler` time and by `spindrift doctor`";
      flakeOption = true;
      legacySettingsExempt = true;
      nixSubPath = "butler.tuningCooldown";
      boxEnv = false;
    };
    butlerTuningDigestBytes = {
      env = "BUTLER_TUNING_DIGEST_BYTES";
      group = "dispatch";
      default = 49152;
      doc = "byte cap on the tuning Chore's digest (ADR 0062): over it, evidence is trimmed first, outliers second, and prior findings third, never the aggregate tables; host-only -- the Box never sees this value";
      flakeOption = true;
      legacySettingsExempt = true;
      intKind = "positive";
      nixSubPath = "butler.tuningDigestBytes";
      boxEnv = false;
    };
  };
}
