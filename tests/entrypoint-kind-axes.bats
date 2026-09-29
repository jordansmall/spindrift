#!/usr/bin/env bats
# Guards agent/entrypoint.sh against re-introducing a branch on the kind name
# (issue #3996): the Box reads DISPATCH_KEYING/DISPATCH_ANNOUNCE_VERB/
# DISPATCH_KEY, the axes dispatch.buildBoxEnv resolved host-side from the
# kind's descriptor, never DISPATCH_KIND itself (display/driver-exec-lookup
# only there) or a kind-name literal. Prior art:
# cmd/launcher/kindaxes_guard_test.go, the Go-side version of the same guard.

load helper

# _kind_branch_violations prints one line per offending non-comment line in
# $1 (full-line comments only are stripped, matching the Go guard's own
# "prose mentioning the old name isn't a comparison" rule):
#   - a DISPATCH_KIND comparison ("${DISPATCH_KIND:-}" = ..., $DISPATCH_KIND
#     == ..., etc) or a `case "$DISPATCH_KIND"` / `case "${DISPATCH_KIND`
#     dispatch;
#   - a kind-name literal ("work" / "research" / "butler") on the right of a
#     =/==/!= comparison;
#   - a "butler-" prefix spelled outside a comment -- that key derivation is
#     dispatch.buildBoxEnv's job (DISPATCH_KEY), not the Box's.
# --dispatch-kind "${DISPATCH_KIND:-work}" (handing the name to driver-exec
# for its own descriptor lookup) and the diagnostic
# "DISPATCH_KIND=${DISPATCH_KIND:-work}" display string are deliberately not
# comparisons and must not match: both lack a comparison operator directly
# after the DISPATCH_KIND expansion.
_kind_branch_violations() {
  local file="$1"
  grep -vE '^[[:space:]]*#' "$file" | grep -nE \
    -e '\$\{?DISPATCH_KIND[^}]*\}?"?[[:space:]]+[!=]?=' \
    -e 'case[[:space:]]+"?\$\{?DISPATCH_KIND' \
    -e '[!=]?=[[:space:]]*"?(butler|research|work)"?([^A-Za-z0-9_.-]|$)' \
    -e '"butler-'
}

@test "agent/entrypoint.sh has no branch on the kind name" {
  run _kind_branch_violations "$ENTRYPOINT"
  [ "$status" -eq 1 ]
  [ -z "$output" ]
}

@test "the checker catches a reintroduced DISPATCH_KIND comparison" {
  local victim="$BATS_TEST_TMPDIR/reintroduced.sh"
  cat >"$victim" <<'SH'
#!/usr/bin/env bash
# a plain comment mentioning butler is fine
if [ "${DISPATCH_KIND:-}" = "butler" ]; then
  echo sweeping
fi
SH
  run _kind_branch_violations "$victim"
  [ "$status" -eq 0 ]
  [[ "$output" == *'DISPATCH_KIND:-}" = "butler"'* ]]
}

@test "the checker catches a reintroduced case-on-kind dispatch" {
  local victim="$BATS_TEST_TMPDIR/reintroduced-case.sh"
  cat >"$victim" <<'SH'
#!/usr/bin/env bash
case "$DISPATCH_KIND" in
  research) echo researching ;;
  *) echo implementing ;;
esac
SH
  run _kind_branch_violations "$victim"
  [ "$status" -eq 0 ]
  [[ "$output" == *'case "$DISPATCH_KIND"'* ]]
}

@test "the checker catches a bare butler- key literal outside a comment" {
  local victim="$BATS_TEST_TMPDIR/reintroduced-key.sh"
  cat >"$victim" <<'SH'
#!/usr/bin/env bash
export BRANCH="butler-${CHORE_NAME}"
SH
  run _kind_branch_violations "$victim"
  [ "$status" -eq 0 ]
  [[ "$output" == *'"butler-'* ]]
}
