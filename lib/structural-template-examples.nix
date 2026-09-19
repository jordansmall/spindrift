# Worked examples for the two agents.models knobs that have no representable
# schema default (issue #2572): byName and roster, whose real default is a Nix
# function call in lib/roster.nix, not a literal. renderTemplateSettingsBlock
# splices them into the generated templates/default/flake.nix. Each `lines`
# entry is one line of bare Nix source; the renderer adds the `# ` and indent.
{ lib }:
let
  defaultModelFixture = import ./default-model-fixture.nix;
  structuralPaths = import ./structural-paths.nix;
  byNamePaths = import ./byname-paths.nix;
  rosterLib = import ./roster.nix { inherit lib; };
  entryFor = name: roster: builtins.head (builtins.filter (e: e.name == name) roster);
  defaultRosterEntries = rosterLib.defaultRoster { };
  scoutDefaults = entryFor "scout" defaultRosterEntries;
  reviewerDefaults = entryFor "reviewer" defaultRosterEntries;

  byNameExample = {
    filer = {
      model = defaultModelFixture.dogfoodPins.filer;
      effort = "high";
    };
  };

  # The inherited fields come from defaultRoster rather than hand-copied text,
  # so the example cannot drift from the real defaults (issue #2572). Omit
  # promptFile and normalizeRoster injects `reviewer-prompt.md`, but the
  # shipped file is `review-prompt.md`.
  rosterExample = [
    {
      name = "scout";
      model = defaultModelFixture.schemaDefaults.scoutModel;
      mode = "subagent";
      inherit (scoutDefaults)
        description
        tools
        promptFile
        effort
        ;
    }
    {
      name = "reviewer";
      model = defaultModelFixture.schemaDefaults.reviewModel;
      mode = "subagent";
      inherit (reviewerDefaults)
        description
        tools
        promptFile
        effort
        ;
    }
  ];

  # builtins.toJSON encodes a plain string as a valid Nix string literal.
  renderByNameLines =
    example:
    [ "byName = {" ]
    ++ lib.concatMap (name: [
      "  ${name} = {"
      "    model = ${builtins.toJSON example.${name}.model};"
      "    effort = ${builtins.toJSON example.${name}.effort};"
      "  };"
    ]) (builtins.attrNames example)
    ++ [ "};" ];

  # A Nix list literal separates elements with spaces, not commas, so tools
  # cannot reuse builtins.toJSON: `["a","b"]` is a Nix syntax error.
  renderToolsLiteral = tools: "[ ${lib.concatStringsSep " " (map builtins.toJSON tools)} ]";

  renderRosterLines =
    example:
    [ "roster = [" ]
    ++ lib.concatMap (e: [
      "  {"
      "    name = ${builtins.toJSON e.name};"
      "    model = ${builtins.toJSON e.model};"
      "    mode = ${builtins.toJSON e.mode};"
      "    description = ${builtins.toJSON e.description};"
      "    tools = ${renderToolsLiteral e.tools};"
      "    promptFile = ${builtins.toJSON e.promptFile};"
      "    effort = ${builtins.toJSON e.effort};"
      "  }"
    ]) example
    ++ [ "];" ];

  # Guards against a typo silently rendering a mismatched key: lines[0] must
  # open with the path's last segment, so a path ending in `roster` pairs
  # with a lines[0] of "roster = [".
  checkEntry =
    e:
    let
      name = builtins.elemAt e.path (builtins.length e.path - 1);
      firstLine = builtins.head e.lines;
    in
    assert lib.hasPrefix "${name} = " firstLine;
    e;
in
# Each record carries the value itself under `example` so
# nix/checks/schema-drift.nix can validate the real data instead of
# re-parsing the rendered text.
map checkEntry [
  {
    path = byNamePaths.byName;
    doc = "Name-keyed model/effort shorthand (issue #2560): a lighter alternative to the roster list below when you only want to override one agent's model or effort.";
    example = byNameExample;
    lines = renderByNameLines byNameExample;
  }
  {
    path = structuralPaths.roster;
    doc = "Subagent roster (issue #264): the first-class N-agent list. Supersedes the four deprecated per-agent model knobs (filer/review above, scout/worker below) and the byName shorthand above, when set. An explicit roster like this one replaces defaultRoster wholesale, so a Consumer copying this two-entry scout/reviewer example verbatim drops filer/worker/review-axis. Add entries for them too, and keep review-axis alongside reviewer or mkHarness fires its explicit-roster warning (rosterWarnings).";
    example = rosterExample;
    lines = renderRosterLines rosterExample;
  }
]
