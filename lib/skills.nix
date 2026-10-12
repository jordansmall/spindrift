# Validation for the `skills` argument of lib/mkHarness.nix (issue #5022). A
# `{ name; src; }` entry's name is spliced into shell scripts that bake the
# skill into the image, so it must be a single safe path component.
rec {
  namePattern = "[A-Za-z0-9._-]+";

  # The one name predicate: the flake module's option type and the mkHarness
  # backstop both use it.
  isValidName =
    name:
    builtins.isString name && builtins.match namePattern name != null && name != "." && name != "..";

  # Returns { ok; message; } instead of throwing: builtins.tryEval cannot
  # recover a thrown message, so checks assert on this directly.
  validateSkillsResult =
    skills:
    let
      # lib.isDerivation inlined: this file stays lib-free so lib/renderers.nix
      # and lib/structural-options-doc.nix can import it without pkgs.
      isContentEntry = f: builtins.isAttrs f && !(f ? type && f.type == "derivation");
      bad = builtins.filter ({ f, ... }: isContentEntry f && !(isValidName (f.name or null))) (
        builtins.genList (idx: {
          inherit idx;
          f = builtins.elemAt skills idx;
        }) (builtins.length skills)
      );
      first = builtins.head bad;
    in
    if bad == [ ] then
      {
        ok = true;
        message = "";
      }
    else
      {
        ok = false;
        message = "skills: entry ${toString first.idx} has an invalid name ${
          builtins.toJSON (first.f.name or null)
        } -- names must match ${namePattern} and must not be \".\" or \"..\"";
      };

  validateSkills =
    skills:
    let
      r = validateSkillsResult skills;
    in
    if r.ok then skills else throw r.message;
}
