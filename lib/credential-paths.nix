# The credential paths agent/credential-deny.sh refuses, as globs. It mirrors
# that hook's regexes for Drivers that don't read Claude Code hooks (opencode
# renders it into its deny rules); the drivers-credential-paths-hook-parity
# check asserts the hook denies every entry.
[
  ".claude/.credentials.json"
  ".config/gh/hosts.yml"
  ".pi/agent/auth.json"
  ".pi/agent/models.json"
  ".local/share/opencode/auth.json"
  ".env"
  ".env.*"
]
