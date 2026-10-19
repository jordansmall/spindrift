# A stub row exercising every reader shape -- string, int, bool and choices
# kinds, emptyDisables on a string, an int and a choices knob, and a secret
# int -- so the generated reader's non-string paths are testable (issue
# #5636): no real row declares them yet.
# Deliberately absent from lib/row-knob-registries.nix -- its knobs must not
# reach the env schema. lib/row-knob-readers.nix generates its reader into
# cmd/launcher/internal/rowknobs/fixture.
{
  name = "fixture";
  knobs = {
    fixtureName = {
      env = "FIXTURE_NAME";
      default = "fallback \"quoted\"";
    };
    fixtureNote = {
      env = "FIXTURE_NOTE";
    };
    fixtureCount = {
      env = "FIXTURE_COUNT";
      default = 3;
    };
    fixtureEnabled = {
      env = "FIXTURE_ENABLED";
      kind = "bool";
      default = true;
    };
    fixtureLimit = {
      env = "FIXTURE_LIMIT";
      default = "10";
      emptyDisables = true;
    };
    fixtureBudget = {
      env = "FIXTURE_BUDGET";
      default = 5;
      emptyDisables = true;
    };
    fixtureTier = {
      env = "FIXTURE_TIER";
      choices = [
        "gold"
        "silver"
      ];
      emptyDisables = true;
    };
    fixtureToken = {
      env = "FIXTURE_TOKEN";
      kind = "int";
      secret = true;
    };
    fixtureMode = {
      env = "FIXTURE_MODE";
      default = "safe";
      choices = [
        "fast"
        "safe"
      ];
    };
  };
}
