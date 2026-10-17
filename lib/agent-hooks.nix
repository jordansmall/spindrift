# The baked claude/opencode Bash hooks directory. The Box has no
# /usr/bin/env, so each hook's `#!/usr/bin/env bash` is pinned to the store
# bash (issue #5399). Helpers and data files stay beside the scripts: the
# hooks source credential-vars-parse from their own dirname, and it finds
# credential-vars from its own.
p:
let
  bashHooks = import ./bash-hooks.nix;
in
p.runCommand "agent-hooks" { } ''
  mkdir -p $out
  for f in ${p.lib.concatMapStringsSep " " (s: "${../agent + "/${s}"}") bashHooks.scripts}; do
    hook=$out/$(stripHash $f)
    cp $f $hook
    chmod +x $hook
    substituteInPlace $hook \
      --replace-fail '#!/usr/bin/env bash' '#!${p.bash}/bin/bash'
  done
  ${p.lib.concatMapStrings (f: ''
    cp ${../agent + "/${f}"} $out/${f}
  '') (bashHooks.helpers ++ bashHooks.dataFiles)}
''
