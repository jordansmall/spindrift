# The agent roster (issue #264) that both Drivers render from. Model
# precedence per name: `models.<name>` (an explicit `""` opt-out included,
# issue #2426) beats a legacy positional knob, which beats the
# lib/env-schema.nix default (issue #2434). lib/mkHarness.nix applies the
# `reviewEffort` knob (issue #2512) after this function, not through it.
{ lib }:
rec {
  # Validates each entry and injects a promptFile default so no Driver
  # re-derives one (issue #2152 slice A, issue #2571). It reports the first
  # violation as `{ ok; value; violation; entryName; message; }` rather than
  # throwing: builtins.tryEval sees that an eval aborted but cannot recover
  # the message, so nix/checks/roster.nix could not pin which check fired.
  normalizeRosterResult =
    roster:
    let
      inherit (lib) foldl' imap0;
      # MIGRATING.md documents these eight keys as the whole entry shape.
      knownKeys = [
        "name"
        "model"
        "effort"
        "mode"
        "description"
        "tools"
        "promptFile"
        "prompt"
      ];
      # "reviewer" is the one name whose template is not "<name>-prompt.md":
      # it is review-prompt.md, matching REVIEW_MODEL and reviewPrompt
      # elsewhere rather than the roster entry name (issue #2571).
      defaultPromptFileOverrides = {
        reviewer = "review-prompt.md";
      };
      defaultPromptFileFor = name: defaultPromptFileOverrides.${name} or "${name}-prompt.md";
      violation = tag: name: message: {
        ok = false;
        value = null;
        violation = tag;
        entryName = name;
        inherit message;
      };
      step =
        acc:
        { idx, e }:
        let
          unknownKeys = builtins.filter (k: !(builtins.elem k knownKeys)) (builtins.attrNames e);
        in
        if acc.violation != null then
          acc
        else if !(e ? name) then
          acc
          // violation "missing-name" null
            "normalizeRoster: entry ${toString idx} is missing a name -- every roster entry must set name"
        else if !(builtins.isString e.name) then
          acc
          // violation "invalid-name" e.name
            "normalizeRoster: entry ${toString idx} has an invalid name ${builtins.toJSON e.name} -- name must be a string"
        else if builtins.match "[a-z0-9-]+" e.name == null then
          acc
          // violation "invalid-name" e.name
            "normalizeRoster: entry ${toString idx} has an invalid name ${builtins.toJSON e.name} -- names must match [a-z0-9-]+"
        else if acc.seen ? ${e.name} then
          acc
          // violation "duplicate-name" e.name
            "normalizeRoster: duplicate name ${builtins.toJSON e.name} at entries ${toString acc.seen.${e.name}} and ${toString idx}"
        else if unknownKeys != [ ] then
          acc
          // violation "unknown-key" e.name
            "normalizeRoster: entry ${builtins.toJSON e.name} has unknown key(s) ${builtins.toJSON unknownKeys} -- expected only ${builtins.toJSON knownKeys}"
        else if !(e ? model) || !(builtins.isString e.model) then
          acc
          // violation "missing-model" e.name
            "normalizeRoster: entry ${builtins.toJSON e.name} is missing model -- every roster entry must set model as a string (\"\" is a valid explicit opt-out)"
        # Checked ahead of the promptFile branch, which also reads e.prompt,
        # so an invalid prompt is reported on its own terms (issue #2571).
        else if e ? prompt && e.prompt != null && !(builtins.isString e.prompt) then
          acc
          // violation "invalid-prompt-type" e.name
            "normalizeRoster: entry ${builtins.toJSON e.name} prompt must be a string or null, got ${builtins.typeOf e.prompt}"
        else if e ? promptFile && !(builtins.isString e.promptFile && e.promptFile != "") then
          acc
          // violation "invalid-promptfile-type" e.name (
            if builtins.isString e.promptFile then
              "normalizeRoster: entry ${builtins.toJSON e.name} promptFile must be a non-empty string, got an empty string"
            else
              "normalizeRoster: entry ${builtins.toJSON e.name} promptFile must be a non-empty string, got ${builtins.typeOf e.promptFile}"
          )
        else
          let
            entry =
              if e ? promptFile then e else e // { promptFile = defaultPromptFileFor e.name; };
            # builtins.pathExists also says yes to a directory or to a
            # traversal escape, so reject ".." segments and absolute paths
            # from the string before touching the filesystem (issue #2571).
            promptFileHasTraversal =
              builtins.elem ".." (lib.splitString "/" entry.promptFile)
              || lib.hasPrefix "/" entry.promptFile;
            promptFileResolvedPath = ../templates/default/prompts + "/${entry.promptFile}";
            promptFileExists = builtins.pathExists promptFileResolvedPath;
            # readFileType throws on a nonexistent path, so promptFileExists
            # guards it.
            promptFileIsRegularFile =
              promptFileExists && builtins.readFileType promptFileResolvedPath == "regular";
            promptFileUsable = !promptFileHasTraversal && promptFileIsRegularFile;
            # An empty inline prompt satisfies neither Driver, so it must not
            # short-circuit the promptFile check below (issue #2555 user
            # story 23).
            hasInlinePrompt = (entry.prompt or null) != null && entry.prompt != "";
          in
          if !promptFileUsable && !hasInlinePrompt then
            acc
            // violation "missing-promptfile" e.name
              "normalizeRoster: entry ${builtins.toJSON e.name} promptFile ${builtins.toJSON entry.promptFile} does not exist under templates/default/prompts and no inline prompt was supplied"
          else
            {
              seen = acc.seen // {
                ${e.name} = idx;
              };
              out = acc.out ++ [ entry ];
              violation = null;
            };
      # An empty roster is a deliberate agent-less image (issue #2152), and
      # the fold's base case returns [] without throwing.
      result = foldl' step {
        seen = { };
        out = [ ];
        violation = null;
      } (imap0 (idx: e: { inherit idx e; }) roster);
    in
    if result.violation == null then
      {
        ok = true;
        value = result.out;
        violation = null;
        entryName = null;
        message = null;
      }
    else
      {
        ok = false;
        value = null;
        violation = result.violation;
        entryName = result.entryName;
        message = result.message;
      };

  # The throwing wrapper production callers (lib/mkHarness.nix) use.
  normalizeRoster =
    roster:
    let
      r = normalizeRosterResult roster;
    in
    if r.ok then r.value else throw r.message;

  # The `""` opt-out (issue #392) drops entries here rather than in
  # normalizeRoster, which validates and never filters, so a caller applies
  # it as its own visible step. flake.nix exports this on rosterLib, so a
  # Consumer can call it on a roster that skipped normalizeRoster: the
  # assert names the entry instead of Nix's bare `attribute 'model' missing`.
  dropOptedOut =
    roster:
    builtins.filter (e: e.model != "") (
      lib.imap0 (
        idx: e:
        assert lib.assertMsg (e ? model) (
          "dropOptedOut: entry "
          + (if e ? name then builtins.toJSON e.name else "at index ${toString idx}")
          + " is missing model -- dropOptedOut requires an already-normalized roster (call normalizeRoster first)"
        );
        e
      ) roster
    );

  # The legacy positional knobs default to null rather than "", since ""
  # already means the #392 opt-out. Every entry's `prompt` is null:
  # agent/entrypoint.sh injects each agent's rendered prompt from
  # `promptFile` at runtime, never at eval time.
  defaultRoster =
    {
      scoutModel ? null,
      reviewModel ? null,
      filerModel ? null,
      workerModel ? null,
      models ? { },
      byName ? { },
    }:
    let
      rosterHelper = import ./roster-schema-defaults.nix { inherit lib; };
      inherit (rosterHelper) schemaDefaults rosterDefaults;
      legacyModels = {
        scout = scoutModel;
        reviewer = reviewModel;
        filer = filerModel;
        worker = workerModel;
      };
      # review-axis (issue #3447, ADR 0049) has no knob of its own and
      # tracks the reviewer's resolved model, so the `""` opt-out (issue
      # #392) drops both entries instead of leaving an orphan fan-out agent
      # on the schema default. legacyModels stays the four deprecated
      # positional knobs MIGRATING.md documents.
      tracksModelOf = {
        "review-axis" = "reviewer";
      };
      knownNames = builtins.attrNames rosterDefaults;
      isUnknownName = n: !(builtins.elem n knownNames);
      unknownNames = builtins.filter isUnknownName (builtins.attrNames models);
      unknownByNameNames = builtins.filter isUnknownName (builtins.attrNames byName);
      unknownByNameFields = lib.concatMap (
        name:
        if !(builtins.isAttrs byName.${name}) then
          throw "defaultRoster: byName.${name} must be an attribute set, got ${builtins.typeOf byName.${name}}"
        else
          let
            unknownFields = builtins.filter (f: f != "model" && f != "effort") (
              builtins.attrNames byName.${name}
            );
          in
          if unknownFields != [ ] then
            [
              {
                inherit name unknownFields;
              }
            ]
          else
            [ ]
      ) (builtins.attrNames byName);
      modelFor =
        name:
        if models ? ${name} then
          models.${name}
        else if (byName.${name}.model or null) != null then
          byName.${name}.model
        else if tracksModelOf ? ${name} then
          modelFor tracksModelOf.${name}
        else if (legacyModels.${name} or null) != null then
          legacyModels.${name}
        else
          schemaDefaults.${name};
      # defaultRoster injects a per-agent default effort from rosterDefaults
      # (issue #2386/#2506). normalizeRoster passes effort through without
      # normalizing it (issue #2242), so a hand-built roster gets no default.
      effortFor =
        name:
        if (byName.${name}.effort or null) != null then
          byName.${name}.effort
        else
          rosterDefaults.${name}.effort;
    in
    if unknownNames != [ ] then
      throw "defaultRoster: models names unknown agent(s) ${builtins.toJSON unknownNames} -- expected one of ${builtins.toJSON knownNames}"
    else if unknownByNameNames != [ ] then
      throw "defaultRoster: byName names unknown agent(s) ${builtins.toJSON unknownByNameNames} -- expected one of ${builtins.toJSON knownNames}"
    else if unknownByNameFields != [ ] then
      throw "defaultRoster: byName has unknown field(s) -- expected only model and/or effort -- ${
        lib.concatMapStringsSep "; " (
          e: "${e.name}: ${builtins.toJSON e.unknownFields}"
        ) unknownByNameFields
      }"
    else
      [
        {
          name = "scout";
          model = modelFor "scout";
          effort = effortFor "scout";
          mode = "subagent";
          description = "Map relevant files, seams, and tests; write a structured brief";
          tools = [
            "Read"
            "Bash"
            "WebFetch"
            "WebSearch"
            "Glob"
            "Grep"
          ];
          promptFile = "scout-prompt.md";
          prompt = null;
        }
        {
          name = "reviewer";
          model = modelFor "reviewer";
          effort = effortFor "reviewer";
          mode = "subagent";
          description = "Review the branch diff for spec compliance and coding standards";
          tools = [
            "Read"
            "Bash"
            "WebFetch"
            "Agent"
          ];
          promptFile = "review-prompt.md";
          prompt = null;
        }
        {
          name = "filer";
          model = modelFor "filer";
          effort = effortFor "filer";
          mode = "subagent";
          description = "File issues from a review's non-blocking findings, best-effort";
          tools = [
            "Read"
            "Bash"
            "WebFetch"
          ];
          promptFile = "filer-prompt.md";
          prompt = null;
        }
        {
          name = "worker";
          model = modelFor "worker";
          effort = effortFor "worker";
          mode = "subagent";
          description = "Implement a scoped slice of work delegated to it, with full implement-capable tools";
          tools = [
            "Read"
            "Bash"
            "Edit"
            "Write"
            "Glob"
            "Grep"
          ];
          promptFile = "worker-prompt.md";
          prompt = null;
        }
        {
          name = "review-axis";
          model = modelFor "review-axis";
          effort = effortFor "review-axis";
          mode = "subagent";
          description = "Run one axis (Standards or Spec) of the code-review skill's two-axis fan-out";
          tools = [
            "Read"
            "Bash"
            "Glob"
            "Grep"
          ];
          promptFile = "review-axis-prompt.md";
          prompt = null;
        }
      ];
}
