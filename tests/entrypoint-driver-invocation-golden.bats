#!/usr/bin/env bats
# Golden of the first Driver invocation (issue #4293): the argv a dispatch
# hands the orchestrator and the environment it runs under, for each dispatch
# kind, with run-specific paths normalised. The same goldens
# (cmd/launcher/box/testdata/driver-invocation/) were captured from the bash path
# before box took that run over, and box's Go seam test must keep reproducing
# them byte for byte, so no phase move changes what the Driver sees.
#
# Golden shape, plain LF text:
#   argv      the flags with path values replaced by placeholders (<handoff>,
#             <prompt>, <session>, <stream-log>; <outbox>/manifest.json)
#   session   the --session-file content, then a newline when non-empty
#   devshell  the handoff's `<Devshell> <DevshellName>` pair
#   prompt    `<handoff-prompt>` when the --prompt-file content equals the handoff's
#             PromptFile content modulo trailing newlines, else
#             `<differs from handoff PromptFile>`
#   env       sorted (LC_ALL=C, whole lines) delta against the test's own env:
#             NAME=value for added/changed vars (newlines in values written as
#             \n), NAME<unset> for vars the orchestrator no longer sees.
#             SHLVL, _ and OLDPWD are excluded.
# Run with UPDATE_DRIVER_INVOCATION_GOLDEN=1 against a writable golden dir to
# regenerate. Box now produces this invocation, so regenerating only records
# its own output: never regenerate to clear a mismatch, which is exactly the
# drift these goldens exist to catch.

load helper

setup() {
  : "${DRIVER_INVOCATION_GOLDEN_DIR:?DRIVER_INVOCATION_GOLDEN_DIR must be set (dir holding the <kind>.golden files)}"
  setup_entrypoint_env
}

set_butler_env() {
  unset ISSUE_NUMBER ISSUE_TITLE
  export CHORE_NAME="bugs"
  set_dispatch_kind butler
  export CHORE_HEAD="deadbeef"
  export CHORE_DIFF_RANGE=""
  export CHORE_SLICE="agent/entrypoint.sh"
}

# _norm_value rewrites run-specific substrings so the golden is stable across
# runs and sandboxes. Order matters: the outbox and mktemp paths first, since
# either may sit under the test tmpdir.
_norm_value() {
  local v="$1"
  [ -n "${OUTBOX_DIR:-}" ] && v="${v//"$OUTBOX_DIR"/<outbox>}"
  v="$(printf '%s' "$v" | sed -E \
    -e 's#/([^/:[:space:]]+/)*tmp\.[A-Za-z0-9]{10}#<mktemp>#g' \
    -e "s#${BATS_TEST_TMPDIR//#/\\#}#<tmp>#g" \
    -e 's#/nix/store/[a-z0-9]{32}-#<store>/#g')"
  printf '%s' "$v"
}

# _read_env <env -0 file> <assoc array name>
_read_env() {
  local -n _out="$2"
  local _entry
  while IFS= read -r -d '' _entry; do
    _out["${_entry%%=*}"]="${_entry#*=}"
  done <"$1"
}

# normalise_snapshot <snapshot dir> <pre-run env -0 file>
normalise_snapshot() {
  local snap="$1" pre_file="$2"
  local -a argv
  mapfile -t argv <"$snap/argv"

  local handoff="" i
  for ((i = 0; i < ${#argv[@]}; i++)); do
    [ "${argv[i]}" = "--handoff-file" ] && handoff="${argv[i + 1]}"
  done
  jq -e '.PromptFile | strings | select(length > 0)' "$handoff" >/dev/null || {
    echo "handoff $handoff has no PromptFile" >&2
    return 1
  }
  local handoff_prompt_file
  handoff_prompt_file="$(jq -r '.PromptFile' "$handoff")"

  echo "argv"
  for ((i = 0; i < ${#argv[@]}; i++)); do
    case "${argv[i]}" in
      --handoff-file) printf '%s\n%s\n' "${argv[i]}" "<handoff>" ;;
      --prompt-file) printf '%s\n%s\n' "${argv[i]}" "<prompt>" ;;
      --session-file) printf '%s\n%s\n' "${argv[i]}" "<session>" ;;
      --log-path) printf '%s\n%s\n' "${argv[i]}" "<stream-log>" ;;
      --manifest-path)
        printf '%s\n%s\n' "${argv[i]}" "${argv[i + 1]/#"$OUTBOX_DIR"/<outbox>}"
        ;;
      *) : ;;
    esac
    case "${argv[i]}" in
      --handoff-file | --prompt-file | --session-file | --log-path | --manifest-path)
        i=$((i + 1))
        ;;
      *) printf '%s\n' "${argv[i]}" ;;
    esac
  done

  echo "session"
  if [ -s "$snap/session" ]; then
    cat "$snap/session"
    echo
  fi

  echo "devshell"
  jq -r '"\(.Devshell) \(.DevshellName)"' "$handoff"

  echo "prompt"
  if [ "$(cat "$snap/prompt")" = "$(cat "$handoff_prompt_file")" ]; then
    echo "<handoff-prompt>"
  else
    echo "<differs from handoff PromptFile>"
  fi

  echo "env"
  local -A pre=() post=()
  _read_env "$pre_file" pre
  _read_env "$snap/env" post
  local name val lines=()
  for name in "${!post[@]}"; do
    case "$name" in SHLVL | _ | OLDPWD) continue ;; esac
    if [ "${pre[$name]+set}" != set ] || [ "${pre[$name]}" != "${post[$name]}" ]; then
      val="$(_norm_value "${post[$name]}")"
      lines+=("$name=${val//$'\n'/\\n}")
    fi
  done
  for name in "${!pre[@]}"; do
    case "$name" in SHLVL | _ | OLDPWD) continue ;; esac
    [ "${post[$name]+set}" = set ] || lines+=("$name<unset>")
  done
  printf '%s\n' "${lines[@]}" | LC_ALL=C sort
}

# check_golden <kind>: drives one entrypoint run and compares snapshot 1.
check_golden() {
  local kind="$1" golden="$DRIVER_INVOCATION_GOLDEN_DIR/$1.golden"
  export ORCHESTRATOR_SNAPSHOT_DIR="$BATS_TEST_TMPDIR/orchestrator-snapshots"
  mkdir -p "$ORCHESTRATOR_SNAPSHOT_DIR"
  # Makes the Bash-timeout exports appear in the delta.
  export DRIVER_BASH_TIMEOUT_MS=1800000
  env -0 >"$BATS_TEST_TMPDIR/pre.env"
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [ -d "$ORCHESTRATOR_SNAPSHOT_DIR/1" ]

  local actual="$BATS_TEST_TMPDIR/actual.golden"
  normalise_snapshot "$ORCHESTRATOR_SNAPSHOT_DIR/1" "$BATS_TEST_TMPDIR/pre.env" >"$actual"

  if [ -n "${UPDATE_DRIVER_INVOCATION_GOLDEN:-}" ]; then
    cp "$actual" "$golden"
    return 0
  fi
  if ! diff -u "$golden" "$actual" >"$BATS_TEST_TMPDIR/golden.diff"; then
    echo "driver invocation golden mismatch for kind $kind:" >&2
    cat "$BATS_TEST_TMPDIR/golden.diff" >&2
    echo "--- actual normalised text for $golden ---" >&2
    cat "$actual" >&2
    echo "--- end actual ---" >&2
    return 1
  fi
}

@test "work kind's first Driver invocation matches the golden" {
  check_golden work
}

@test "research kind's first Driver invocation matches the golden" {
  set_dispatch_kind research
  check_golden research
}

@test "butler kind's first Driver invocation matches the golden" {
  set_butler_env
  check_golden butler
}

# Issue #4409: DRIVER_BASH_TIMEOUT_MS is a Consumer knob; box exports
# it under each name the Driver's registry entry lists in DRIVER_BASH_TIMEOUT_ENV
# (claude: BASH_DEFAULT_TIMEOUT_MS and BASH_MAX_TIMEOUT_MS).
@test "DRIVER_BASH_TIMEOUT_MS set exports both Claude Code timeout vars to the Driver" {
  export DRIVER_BASH_TIMEOUT_MS=1800000
  unset BASH_DEFAULT_TIMEOUT_MS BASH_MAX_TIMEOUT_MS
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qx 'env: BASH_DEFAULT_TIMEOUT_MS=1800000' "$DRIVER_LOG"
  grep -qx 'env: BASH_MAX_TIMEOUT_MS=1800000' "$DRIVER_LOG"
}

@test "DRIVER_BASH_TIMEOUT_MS unset leaves both Claude Code timeout vars unset" {
  unset DRIVER_BASH_TIMEOUT_MS BASH_DEFAULT_TIMEOUT_MS BASH_MAX_TIMEOUT_MS
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qx 'env: BASH_DEFAULT_TIMEOUT_MS=<unset>' "$DRIVER_LOG"
  grep -qx 'env: BASH_MAX_TIMEOUT_MS=<unset>' "$DRIVER_LOG"
}

@test "DRIVER_BASH_TIMEOUT_MS empty leaves both Claude Code timeout vars unset" {
  export DRIVER_BASH_TIMEOUT_MS=""
  unset BASH_DEFAULT_TIMEOUT_MS BASH_MAX_TIMEOUT_MS
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qx 'env: BASH_DEFAULT_TIMEOUT_MS=<unset>' "$DRIVER_LOG"
  grep -qx 'env: BASH_MAX_TIMEOUT_MS=<unset>' "$DRIVER_LOG"
}

# A value that is not a positive integer is warned about and skipped, since
# Claude Code may silently fall back to its own cap instead of rejecting it.
assert_bad_bash_timeout_skipped() {
  export DRIVER_BASH_TIMEOUT_MS="$1"
  unset BASH_DEFAULT_TIMEOUT_MS BASH_MAX_TIMEOUT_MS
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  [[ "$output" == *"WARNING: DRIVER_BASH_TIMEOUT_MS='$1' is not a positive integer"* ]]
  grep -qx 'env: BASH_DEFAULT_TIMEOUT_MS=<unset>' "$DRIVER_LOG"
  grep -qx 'env: BASH_MAX_TIMEOUT_MS=<unset>' "$DRIVER_LOG"
}

@test "DRIVER_BASH_TIMEOUT_MS=0 warns and exports neither timeout var" {
  assert_bad_bash_timeout_skipped 0
}

@test "DRIVER_BASH_TIMEOUT_MS=-5 warns and exports neither timeout var" {
  assert_bad_bash_timeout_skipped -5
}

@test "DRIVER_BASH_TIMEOUT_MS=30m warns and exports neither timeout var" {
  assert_bad_bash_timeout_skipped 30m
}

# opencode's registry entry declares no bashTimeoutEnv, so its preamble carries
# no DRIVER_BASH_TIMEOUT_ENV line. Stripping the claude one from the wrapped
# entrypoint reproduces that preamble shape.
@test "a Driver preamble without DRIVER_BASH_TIMEOUT_ENV ignores DRIVER_BASH_TIMEOUT_MS" {
  ! grep -q '^DRIVER_BASH_TIMEOUT_ENV=' "$OPENCODE_DRIVER_PREAMBLE_FILE"
  grep -q '^DRIVER_BASH_TIMEOUT_ENV=' "$ENTRYPOINT"
  sed -i '/^DRIVER_BASH_TIMEOUT_ENV=/d' "$ENTRYPOINT"
  export DRIVER_BASH_TIMEOUT_MS=1800000
  unset BASH_DEFAULT_TIMEOUT_MS BASH_MAX_TIMEOUT_MS
  run bash "$ENTRYPOINT"
  [ "$status" -eq 0 ]
  grep -qx 'env: BASH_DEFAULT_TIMEOUT_MS=<unset>' "$DRIVER_LOG"
  grep -qx 'env: BASH_MAX_TIMEOUT_MS=<unset>' "$DRIVER_LOG"
}
