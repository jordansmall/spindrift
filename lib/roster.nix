# The default agent roster (issue #264): a first-class, N-agent list of
# { name; model; effort; mode; description; tools; promptFile; prompt }
# entries that both Drivers (lib/drivers/claude.nix's agentsJsonTemplate,
# lib/drivers/opencode.nix's agentFilesTemplate) render from, replacing the
# four hardcoded scout/reviewer/filer/worker model-knob args each Driver
# template used to take directly. `defaultRoster` reproduces today's four
# agents byte-for-byte (same descriptions/tools/promptFile names as the
# templates previously baked in). Its primary, roster-native surface is the
# `models` attrset (issue #2426), keyed by roster entry name (scout/
# reviewer/filer/worker). A name absent from `models` inherits that agent's
# `lib/env-schema.nix` default (issue #2434) -- the same default
# `mkHarness`'s no-roster fallback path resolves through `mergedDefaults`.
# An unknown name in `models` throws at eval time, the same way
# `normalizeRoster` rejects an invalid entry name. The four legacy
# positional knobs (scoutModel/reviewModel/filerModel/workerModel) default
# to `null` -- a sentinel distinguishing "not supplied" from "supplied as
# empty" -- and still work as a lower-precedence fallback per name, since
# lib/mkHarness.nix resolves its deprecated `settings.*Model` knobs through
# them, always supplying an explicit (non-null) value. Precedence per name:
# `models.<name>` (including an explicit `""` opt-out) wins over an
# explicitly supplied legacy knob, which wins over the schema default.
# `prompt` is
# always `null` here -- entrypoint.sh injects each agent's rendered prompt at
# runtime from `promptFile`, never at eval time (see agent/entrypoint.sh's
# generic prompt-injection loop). `effort`, like `model`, is an optional
# pass-through on the general roster schema -- no normalization -- that each
# Driver forwards verbatim when set (issue #2242). `defaultRoster`
# additionally ships a fixed default `effort` per agent, looked up per name
# from `rosterDefaults` (lib/roster-schema-defaults.nix; issue #2386/#2506)
# -- a caller assembling a custom roster by hand still gets no injected
# default, since that stays specific to `defaultRoster`'s own lookup, not a
# `normalizeRoster`-level behavior. The `reviewEffort` legacy knob (issue
# #2512) does NOT live here: unlike the four legacy model knobs above, it
# overrides the reviewer entry's `effort` regardless of whether the roster
# came from `defaultRoster` or an explicit caller-supplied `roster`, so
# lib/mkHarness.nix applies it as a post-processing step on the fully
# resolved roster instead of threading it through this function.
{ lib }:
# `rec` (issue #2571): normalizeRoster is a thin wrapper around
# normalizeRosterResult, defined as a sibling attribute in this same
# returned set, so it needs to be in scope here.
rec {
  # Normalizes a roster list before any Driver consumes it (issue #2152 slice
  # A): validates each entry's name and injects a promptFile default for any
  # entry that omits one, so every Driver-facing consumer can assume every
  # entry already carries a promptFile rather than re-deriving the default
  # itself. Also validates (issue #2571 slice 1) that every entry's keys are
  # a subset of the documented roster entry shape and that every entry
  # literally carries a `model` key (even an explicit `""` opt-out, #392 --
  # only the key's presence is required, not a non-empty value). Deliberately
  # does no escaping (a later slice's concern). Issue #2571 slice 2: also
  # validates that the entry's effective promptFile (explicit or injected
  # default) exists on disk under templates/default/prompts, unless the
  # entry instead carries an inline `prompt`. Issue #2571: this function no
  # longer drops an entry whose `model` is the explicit `""` opt-out (#392)
  # -- `model = ""` is now an ordinary, valid value that passes through
  # completely unfiltered, same as any other entry. The #392
  # opt-out-from-the-built-image behavior instead lives as an explicit step
  # in lib/mkHarness.nix (a later slice), not silently inside this funnel.
  #
  # normalizeRosterResult (issue #2571) is the non-throwing core: it returns
  # a structured `{ ok; value; violation; entryName; message; }` result
  # instead of throwing on the first violation, so eval-level tests
  # (nix/checks/roster.nix) can assert
  # directly on which violation class fired and which entry triggered it --
  # `builtins.tryEval` can only observe *that* an eval aborted, never
  # recover the thrown message text, so a throwing-only contract can't be
  # proven this precisely (mirrors nix/checks/prompt-contract.nix's
  # buildTimeRejectVerdicts pattern). On success: `ok = true; value = <the
  # normalized list>; violation = null; entryName = null; message = null;`.
  # On the first violation (entries checked in list order; per entry, the
  # nine branches below fire in this order: missing-name, invalid-name
  # (non-string name), invalid-name (bad format), duplicate-name,
  # unknown-key, missing-model, invalid-prompt-type, invalid-promptfile-type,
  # missing-promptfile): `ok = false; value = null; violation = "<a short
  # stable tag>"; entryName = <the entry's raw `name` attribute if it has
  # one, whatever its type -- else null only for the missing-name case>;
  # message = "<the human-readable message>";`. `normalizeRoster` is a thin throwing
  # wrapper around this for production callers (lib/mkHarness.nix), whose
  # throwing behavior and messages are unchanged.
  normalizeRosterResult =
    roster:
    let
      inherit (lib) foldl' imap0;
      # The full documented roster entry shape (MIGRATING.md's list of the
      # eight allowed keys) -- any entry carrying a key outside this
      # set (typo/oversight) must throw rather than silently pass through to
      # the Drivers.
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
      # Issue #2571: the four canonical agent names' injected promptFile
      # default is "<name>-prompt.md" for three of them; "reviewer" is the
      # one exception -- its on-disk template is
      # templates/default/prompts/review-prompt.md, not
      # reviewer-prompt.md (matching REVIEW_MODEL/reviewPrompt's own naming
      # elsewhere, not the roster entry name).
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
          # Bound once (issue #2571) so both the unknown-key branch
          # condition and its thrown message below share this single
          # computation instead of each recomputing it -- lazy, so entries
          # that never reach this branch never force it.
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
        # Issue #2571: checked here, ahead of the promptFile branch below,
        # since prompt's validity is independent of
        # promptFile/promptFileExists -- an invalid prompt should be
        # reported on its own terms rather than getting entangled with the
        # promptFile-resolution branch that also reads e.prompt.
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
            # Issue #2571: builtins.pathExists alone blesses non-files -- a
            # directory (including "." and a real subdirectory like
            # "fragments") or a path-traversal escape (".." as a path
            # segment, or an absolute path) all resolve to something
            # pathExists reports as existing. Reject a traversal/absolute
            # promptFile by inspecting the string itself, before ever
            # touching the filesystem.
            promptFileHasTraversal =
              builtins.elem ".." (lib.splitString "/" entry.promptFile)
              || lib.hasPrefix "/" entry.promptFile;
            promptFileResolvedPath = ../templates/default/prompts + "/${entry.promptFile}";
            promptFileExists = builtins.pathExists promptFileResolvedPath;
            # Confirm the resolved path is a regular file, not a directory
            # or anything else -- only called once we know the path exists,
            # since readFileType throws on a nonexistent path.
            promptFileIsRegularFile =
              promptFileExists && builtins.readFileType promptFileResolvedPath == "regular";
            promptFileUsable = !promptFileHasTraversal && promptFileIsRegularFile;
            # Issue #2571: an empty inline prompt ("") is treated the same
            # as no prompt at all -- it satisfies neither Driver's actual
            # prompt-injection need, so it must not short-circuit the
            # promptFile-existence check below (issue #2555 user story 23).
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
      # An empty roster is a deliberate agent-less image (issue #2152) -- the
      # fold's base case naturally returns [] without ever throwing, no
      # special-case needed.
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

  # Thin throwing wrapper around normalizeRosterResult for production
  # callers (lib/mkHarness.nix) -- behavior and thrown messages are
  # unchanged from before normalizeRosterResult became the non-throwing
  # core (issue #2571).
  normalizeRoster =
    roster:
    let
      r = normalizeRosterResult roster;
    in
    if r.ok then r.value else throw r.message;

  # The #392 opt-out (issue #392): drops any entry whose `model` is the
  # explicit `""` sentinel. This is deliberately NOT part of
  # normalizeRoster (issue #2571) -- normalizeRoster only validates and
  # never filters, so a caller that wants #392 semantics applies this as
  # its own explicit, visible step after normalizeRoster succeeds, right
  # before a Driver ever sees the roster. lib/mkHarness.nix is the one
  # production caller.
  #
  # dropOptedOut's contract above assumes every entry already carries a
  # `model` key (normalizeRoster's postcondition) -- but it's also exported
  # directly on the versioned rosterLib surface (flake.nix), so a Consumer
  # can call it standalone on a hand-built roster that skipped
  # normalizeRoster. Without a guard, a missing `model` key aborts with
  # Nix's bare, unhelpful `attribute 'model' missing` (no indication which
  # entry or what's expected) -- name the offending entry (by name if it has
  # one, else its position) and state the precondition instead (review
  # finding on issue #2571). A straightforward per-entry assertMsg, not the
  # full non-throwing `{ ok; ... }` treatment normalizeRosterResult uses
  # above -- dropOptedOut has exactly one caller-visible failure mode, so
  # there's no violation-class taxonomy worth building out here.
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
      isUnknownName = n: !(legacyModels ? ${n});
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
        else if legacyModels.${name} != null then
          legacyModels.${name}
        else
          schemaDefaults.${name};
      effortFor =
        name:
        if (byName.${name}.effort or null) != null then
          byName.${name}.effort
        else
          rosterDefaults.${name}.effort;
    in
    if unknownNames != [ ] then
      throw "defaultRoster: models names unknown agent(s) ${builtins.toJSON unknownNames} -- expected one of ${builtins.toJSON (builtins.attrNames legacyModels)}"
    else if unknownByNameNames != [ ] then
      throw "defaultRoster: byName names unknown agent(s) ${builtins.toJSON unknownByNameNames} -- expected one of ${builtins.toJSON (builtins.attrNames legacyModels)}"
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
          description = "Map relevant files, seams, and tests; return a structured brief";
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
            "WebFetch"
          ];
          promptFile = "worker-prompt.md";
          prompt = null;
        }
      ];
}
