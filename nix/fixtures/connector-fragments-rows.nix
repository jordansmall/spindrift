# A fixture third-party row's fragment contribution (issue #7647): its own
# folder of every fragment the declared axes leave without a shared root
# fallback. `name` must be a folder of ./connector-fragments-rows; the filter
# keeps that folder's files for `axes`, so a Connector's dir holds nothing the
# contribution does not declare.
{
  name,
  axes ? [ "tracker" ],
}:
let
  root = ./connector-fragments-rows;
  files = builtins.concatMap (a: axisFiles.${a} or [ ]) axes;
  trackerFiles = [
    "filer-dedup.md"
    "pr-body-issue-ref.md"
    "pr-body-no-issue-ref.md"
  ];
  forgeFiles = [ "fix-ci-read.md" ];
  axisFiles = {
    tracker = trackerFiles;
    forge = forgeFiles;
  };
in
{
  inherit name;
  dir = builtins.path {
    name = "${name}-fragments";
    path = root;
    filter =
      p: _:
      p == toString root
      || p == "${toString root}/${name}"
      || builtins.any (f: p == "${toString root}/${name}/${f}") files;
  };
  shipped = builtins.listToAttrs (
    map (f: {
      name = f;
      value = [ name ];
    }) files
  );
}
