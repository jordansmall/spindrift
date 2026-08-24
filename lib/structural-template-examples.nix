# Hand-authored worked examples for the two structural agents.models
# entries that have no representable schema default (issue #2572): byName
# and roster (lib/structural-paths.nix; their real default is a Nix
# function call, lib/roster.nix's defaultRoster, not a literal).
# renderTemplateSettingsBlock (lib/renderers.nix) splices these into the
# same nested/sorted domain tree flakeOption schema knobs render into, at
# the given `path`, rendering `doc` as the `# ` comment line (like a schema
# knob's `.doc`) and `lines` as the commented Nix assignment (instead of a
# schema-default-derived single-line literal) -- so the generated
# templates/default/flake.nix example shows the deprecated per-agent model
# knobs' replacement alongside them, discoverable without a hand-edit.
#
# Each `lines` entry is relative Nix source (no leading `# ` comment marker
# and no base indentation -- renderTemplateSettingsBlock prefixes every line
# with the block's `# ` marker and the node's depth-derived indent), one
# array element per source line, so a Consumer can strip the leading `# `
# from just these lines and use the result as-is.
#
# Issue #2572 round 2 (blocking findings 1 and 2): `lines` used to be
# hand-typed literal text, independent of any real Nix value -- nothing
# stopped it from silently rotting out of sync with lib/roster.nix's actual
# shape (finding 1: the roster example omitted description/tools, which a
# Driver renders as ""/[ ] when absent, producing a capability-less agent),
# and nothing evaluated the example data against normalizeRoster/
# defaultRoster to catch that kind of drift (finding 2). Each example below
# is now a real Nix value (`byNameExample`/`rosterExample`) -- for roster,
# `description`/`tools`/`promptFile`/`effort` are all derived from
# lib/roster.nix's own `defaultRoster { }` output rather than hand-copied,
# so the example can never independently drift from the real defaults again
# -- and `lines` is a *derived view* of that value (via a small per-shape
# renderer, builtins.toJSON for the string/list literals), not a second
# hand-typed source of truth. Each exported record also carries the value
# itself under `.example`, so nix/checks/schema-drift.nix's
# structural-template-examples-{roster,byname}-valid checks can validate
# the real data directly instead of re-parsing the rendered text.
#
# Issue #2572 round 3 (blocking finding 1): the same class of bug recurred
# through `promptFile`, which round 2's fix didn't inherit -- omitting it
# left normalizeRoster injecting a `<name>-prompt.md` default, silently
# wrong for reviewer specifically (its real shipped file is
# `review-prompt.md`, not `reviewer-prompt.md`; templates/default/prompts/).
# `promptFile`/`effort` are now `inherit`ed alongside description/tools, and
# structural-template-examples-roster-valid additionally checks every entry
# against its defaultRoster counterpart field-for-field, not just
# description/tools, to close the class rather than the one field.
{ lib }:
let
  defaultModelFixture = import ./default-model-fixture.nix;
  structuralPaths = import ./structural-paths.nix;
  rosterLib = import ./roster.nix { inherit lib; };
  # Pulls a named entry out of a roster list (mirrors the entryFor helper in
  # nix/checks/roster.nix).
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

  # Renders a byName example attrset as commented Nix source lines, one
  # array element per source line (see file header). builtins.toJSON gives
  # a valid Nix string literal for a plain string value.
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

  # Renders a roster example list as commented Nix source lines. `tools` is
  # a Nix list literal (space-separated elements, no commas) -- NOT the same
  # syntax as a JSON array, so it can't reuse builtins.toJSON like the other
  # scalar fields do; a comma-separated `["a","b"]` is a Nix syntax error.
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

  # Guards against a typo silently rendering a mismatched key: each entry's
  # `lines[0]` is expected to open with `<path's last segment> = `, e.g.
  # path [ "agents" "models" "byName" ] pairs with lines[0] "byName = {".
  checkEntry =
    e:
    let
      name = builtins.elemAt e.path (builtins.length e.path - 1);
      firstLine = builtins.head e.lines;
    in
    assert lib.hasPrefix "${name} = " firstLine;
    e;
in
map checkEntry [
  {
    path = [
      "agents"
      "models"
      "byName"
    ];
    doc = "Name-keyed model/effort shorthand (issue #2560): a lighter alternative to the roster list below when you only want to override one agent's model or effort.";
    example = byNameExample;
    lines = renderByNameLines byNameExample;
  }
  {
    path = structuralPaths.roster;
    doc = "Subagent roster (issue #264): the first-class N-agent list. Supersedes the four deprecated per-agent model knobs (filer/review above, scout/worker below) and the byName shorthand above, when set. An explicit roster like this one replaces defaultRoster wholesale -- this two-entry example customizes only scout/reviewer, so a Consumer copying it verbatim drops filer/worker; add entries for them too to keep all four.";
    example = rosterExample;
    lines = renderRosterLines rosterExample;
  }
]
