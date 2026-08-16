# The quickstart wizard's option-path lookup table (issue #2556): for each of
# the wizard's flakeOption knobs, the knob's canonical nix option path -- the
# domain-tree path lib/nixpath.nix's resolveNixPath derives from the knob's
# `group` and optional `nixSubPath` in lib/env-schema.nix (ADR 0037 Pass 2).
# Single-sourced here rather than hand-typed in the quickstart wizard's
# rendered flake.nix output (cmd/launcher/quickstart/quickstart.go's
# renderFlakeNix), so the wizard's generated Nix literal paths
# (cmd/launcher/quickstart/quickstart_paths_gen.go) can't drift from
# lib/env-schema.nix's own group/nixSubPath taxonomy.
let
  schema = import ./env-schema.nix;
  resolveNixPath = import ./nixpath.nix;
  # The wizard's own knob set -- every schema key the quickstart flow prompts
  # for and writes into the generated flake.nix.
  knobNames = [
    "repoSlug"
    "gitUserName"
    "gitUserEmail"
    "issueTracker"
    "codeForge"
    "forgejoBaseURL"
  ];
in
builtins.listToAttrs (
  map (name: {
    inherit name;
    value = resolveNixPath name schema.${name};
  }) knobNames
)
