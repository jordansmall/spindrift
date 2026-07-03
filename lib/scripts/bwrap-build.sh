# Realise the agent store closures for the daemonless bubblewrap runner.
#
# Body fragment: nix (lib/mkHarness.nix) wraps it with writeShellApplication,
# prepending the shebang, `set -euo pipefail`, the pinned runtimeInputs PATH,
# and a preamble defining RUNTIME, AGENT_FILES_DRV, and AGENT_ENV_DRV.
#
# No OCI image is built or loaded — the nix store closures are all the bwrap
# runner needs. Run this from your Consumer flake's directory.

echo "==> bwrap runner: realising agent store closures (no image build/load)"
nix build "${AGENT_FILES_DRV}^*" --no-link
nix build "${AGENT_ENV_DRV}^*" --no-link
echo "==> done: agent store closures realised"
