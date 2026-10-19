# Where a lib/fragments.nix row's file lives under prompts/fragments/ (issue
# #5646, ADR 0068). A plain row is one root-level file; an `axis` row has one
# file per backend valid on that axis, in `<backend>/<fragment>`, because the
# active backend picks the file at render time. Scan sites iterate `pathsOf`
# (filtered by pathExists where a Consumer fragmentsDir may omit files).
let
  backends = import ./backends/default.nix;
  validField = {
    tracker = "validAsTracker";
    forge = "validAsCodeForge";
  };
in
{
  pathsOf =
    row:
    if (row.axis or "") == "" then
      [ row.fragment ]
    else
      map (b: "${b.name}/${row.fragment}") (
        builtins.filter (b: b.${validField.${row.axis}} or false) backends
      );
}
