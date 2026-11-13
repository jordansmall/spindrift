# The shell that copies the shipped prompts/fragments tree to `dest` and overlays
# each Connector's fragment dir onto it (issue #6857), so the Box's one
# fragments root carries the Connector-owned `<folder>/<fragment>` files the Go
# assembler reads. A store copy is read-only, hence the chmod before an overlay.
{
  lib,
  src,
  connectorFragments,
  dest,
}:
''
  cp -r ${src} ${dest}
''
+ lib.optionalString (connectorFragments != [ ]) ''
  chmod -R u+w ${dest}
  ${lib.concatMapStrings (c: "cp -r ${c.dir}/. ${dest}/\n") connectorFragments}''
