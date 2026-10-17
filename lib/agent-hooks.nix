# The baked claude/opencode Bash hooks directory. The Box has no
# /usr/bin/env, so each hook's `#!/usr/bin/env bash` is pinned to the store
# bash (issue #5399). credential-vars stays beside the scripts: the scrub
# hook finds it via dirname of its own path.
p:
p.runCommand "agent-hooks" { } ''
  mkdir -p $out
  for f in ${
    p.lib.concatMapStringsSep " " (f: "${f}") [
      ../agent/reject-background-bash.sh
      ../agent/credential-deny.sh
      ../agent/env-credential-scrub.sh
      ../agent/bash-output-tee.sh
      ../agent/bash-output-summary.sh
    ]
  }; do
    hook=$out/$(stripHash $f)
    cp $f $hook
    chmod +x $hook
    substituteInPlace $hook \
      --replace-fail '#!/usr/bin/env bash' '#!${p.bash}/bin/bash'
  done
  cp ${../agent/credential-vars} $out/credential-vars
''
