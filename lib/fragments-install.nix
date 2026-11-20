# The shell that copies the shipped prompts/fragments tree to `dest` and overlays
# each Connector's fragment dir onto it (issue #6857), so the Box's one
# fragments root carries the Connector-owned `<folder>/<fragment>` files the Go
# assembler reads. A store copy is read-only, hence the chmod before each
# overlay: an earlier overlay's folder is read-only too, and two Connectors may
# ship into the same folder.
{
  lib,
  src,
  connectorFragments,
  dest,
}:
''
  cp -r ${src} ${dest}
''
+ lib.concatMapStrings (c: ''
  chmod -R u+w ${dest}
  cp -r ${c.dir}/. ${dest}/
'') connectorFragments
