#!/usr/bin/env bats
# __complete-issues (issue #556): the hidden discovery seam the bash/zsh/fish
# completion renderers shell out to for dynamic positional issue-number
# completion on dispatch/preview/recover. Driven directly through the real
# spindrift binary against the tests/fakes/gh tracker fake — never a live
# network call.

load helper

setup() {
  setup_run_env
}

@test "__complete-issues lists the dispatchable queue as tab-separated number/title lines" {
  export FAKE_GH_ISSUES=$'12\tFix the thing\n13\tAnother one'
  run "$SPINDRIFT_CMD" __complete-issues
  [ "$status" -eq 0 ]
  [[ "$output" == *$'12\tFix the thing'* ]]
  [[ "$output" == *$'13\tAnother one'* ]]
}

@test "__complete-issues excludes an issue not carrying the dispatch label" {
  export FAKE_GH_ISSUES=$'1\tReady one\n2\tAlready claimed'
  # Pre-seed GH_STATE so issue 2 carries agent-in-progress, not
  # ready-for-agent — same technique run-reconcile-recover.bats uses.
  printf '2\tagent-in-progress\n' >>"$GH_LOG.state"
  run "$SPINDRIFT_CMD" __complete-issues
  [ "$status" -eq 0 ]
  [[ "$output" == *$'1\tReady one'* ]]
  [[ "$output" != *"Already claimed"* ]]
}
