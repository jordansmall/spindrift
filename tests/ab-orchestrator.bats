#!/usr/bin/env bats
# ab-orchestrator.bats — dry-run argv assertions for the worker A/B harness
# (issue #2057 slice 1): the harness now varies --worker-model between arms
# while holding --orchestrator-enabled fixed, instead of varying
# --orchestrator-enabled itself.

@test "dry-run: off arm omits worker model, on arm sets it, both share orchestrator value" {
  run env -i \
    PATH="$PATH" \
    AB_DRY_RUN=1 AB_CONFIRM=1 \
    AB_REMOTE="git@github.com:acme/mirror.git" \
    AB_REPO_SLUG="acme/mirror" \
    AB_MODEL="claude-sonnet-5" \
    AB_ORCH="" \
    AB_OUTDIR="$BATS_TEST_TMPDIR/ab-out" \
    bash "${AB_ORCHESTRATOR_SH:-$BATS_TEST_DIRNAME/../ab-orchestrator.sh}" 123
  [ "$status" -eq 0 ]
  # off arm: --worker-model empty
  echo "$output" | grep -- "--worker-model ''"
  # on arm: --worker-model set to AB_WORKER_ON (defaults to AB_MODEL)
  echo "$output" | grep -- "--worker-model claude-sonnet-5"
  # orchestrator value identical (empty) on both arms — never "--orchestrator-enabled 1"
  ! echo "$output" | grep -- "--orchestrator-enabled 1"
}

@test "--breakdown attributes tokens per role+model with injected pricing" {
  local script="${AB_ORCHESTRATOR_SH:-$BATS_TEST_DIRNAME/../ab-orchestrator.sh}"
  local log="$BATS_TEST_TMPDIR/box.log"
  cat >"$log" <<'JSON'
==> some non-json narration line
{"type":"assistant","message":{"model":"claude-sonnet-5","content":[{"type":"tool_use","id":"toolu_scout","name":"Task","input":{"subagent_type":"scout"}}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":10,"cache_creation_input_tokens":5}}}
{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","content":[],"usage":{"input_tokens":200,"output_tokens":80,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}},"parent_tool_use_id":"toolu_scout"}
{"type":"assistant","message":{"model":"claude-sonnet-5","content":[],"usage":{"input_tokens":50,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
{"type":"result","total_cost_usd":0.5,"usage":{"input_tokens":9,"output_tokens":9}}
JSON
  run env AB_PRICES='{"claude-sonnet-5":[3,15,0.3,3.75],"claude-haiku":[1,5,0.1,1]}' \
    bash "$script" --breakdown "$log"
  [ "$status" -eq 0 ]
  # implementor on sonnet-5: fresh_input=100+50=150, output=50+20=70, cr=10, cw=5
  #   cost = (150*3 + 70*15 + 10*0.3 + 5*3.75)/1e6 = (450+1050+3+18.75)/1e6 = 0.00152175 -> 0.001522
  echo "$output" | grep -P '^implementor\tclaude-sonnet-5\t10\t5\t150\t70\t0.001522$'
  # scout on haiku: cr=0 cw=0 in=200 out=80 cost=(200*1+80*5)/1e6=(200+400)/1e6=0.0006 -> 0.000600
  echo "$output" | grep -P '^scout\tclaude-haiku-4-5-20251001\t0\t0\t200\t80\t0.000600$'
}

@test "--breakdown reports zero cost for a model with no pricing-table prefix match" {
  local script="${AB_ORCHESTRATOR_SH:-$BATS_TEST_DIRNAME/../ab-orchestrator.sh}"
  local log="$BATS_TEST_TMPDIR/unknown.log"
  cat >"$log" <<'JSON'
{"type":"assistant","message":{"model":"some-other-model","content":[],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":1,"cache_creation_input_tokens":1}}}
JSON
  run env AB_PRICES='{"claude-sonnet-5":[3,15,0.3,3.75]}' bash "$script" --breakdown "$log"
  [ "$status" -eq 0 ]
  echo "$output" | grep -P '^implementor\tsome-other-model\t1\t1\t10\t5\t0.000000$'
}

@test "--compare emits side-by-side per role+model rows for both arms and a per-model + total effective-$ delta" {
  local script="${AB_ORCHESTRATOR_SH:-$BATS_TEST_DIRNAME/../ab-orchestrator.sh}"
  local off="$BATS_TEST_TMPDIR/off.tsv"
  local on="$BATS_TEST_TMPDIR/on.tsv"
  printf 'implementor\tclaude-sonnet-5\t10\t5\t150\t70\t0.001522\n' >"$off"
  printf 'implementor\tclaude-sonnet-5\t20\t10\t100\t40\t0.001000\n' >"$on"
  printf 'scout\tclaude-haiku-4-5-20251001\t0\t0\t200\t80\t0.000600\n' >>"$on"

  run bash "$script" --compare "$off" "$on"
  [ "$status" -eq 0 ]
  echo "$output" | grep -P '^\| off \| implementor \| claude-sonnet-5 \| 10 \| 5 \| 150 \| 70 \| 0.001522 \|$'
  echo "$output" | grep -P '^\| on \| scout \| claude-haiku-4-5-20251001 \| 0 \| 0 \| 200 \| 80 \| 0.000600 \|$'
  echo "$output" | grep -P '^\| claude-haiku-4-5-20251001 \| 0.000000 \| 0.000600 \| 0.000600 \|$'
  echo "$output" | grep -P '^\| \*\*total\*\* \| 0.001522 \| 0.001600 \| 0.000078 \|$'
}

@test "--breakdown then --compare gives per-model attribution and a total delta end-to-end (criteria 1-3)" {
  local script="${AB_ORCHESTRATOR_SH:-$BATS_TEST_DIRNAME/../ab-orchestrator.sh}"
  local offlog="$BATS_TEST_TMPDIR/off-box.log"
  local onlog="$BATS_TEST_TMPDIR/on-box.log"
  local off_tsv="$BATS_TEST_TMPDIR/off.tsv"
  local on_tsv="$BATS_TEST_TMPDIR/on.tsv"

  cat >"$offlog" <<'JSON'
{"type":"assistant","message":{"model":"claude-sonnet-5","content":[],"usage":{"input_tokens":1000,"output_tokens":500,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
JSON

  cat >"$onlog" <<'JSON'
{"type":"assistant","message":{"model":"claude-sonnet-5","content":[{"type":"tool_use","id":"toolu_w","name":"Task","input":{"subagent_type":"worker"}}],"usage":{"input_tokens":300,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}
{"type":"assistant","message":{"model":"claude-sonnet-5","content":[],"usage":{"input_tokens":400,"output_tokens":200,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}},"parent_tool_use_id":"toolu_w"}
JSON

  run bash "$script" --breakdown "$offlog"
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" >"$off_tsv"

  run bash "$script" --breakdown "$onlog"
  [ "$status" -eq 0 ]
  printf '%s\n' "$output" >"$on_tsv"

  run bash "$script" --compare "$off_tsv" "$on_tsv"
  [ "$status" -eq 0 ]
  echo "$output" | grep -P '^\| on \| worker \| claude-sonnet-5 \| 0 \| 0 \| 400 \| 200 \| 0.004200 \|$'
  echo "$output" | grep -P '^\| on \| implementor \| claude-sonnet-5 \| 0 \| 0 \| 300 \| 100 \| 0.002400 \|$'
  echo "$output" | grep -P '^\| off \| implementor \| claude-sonnet-5 \| 0 \| 0 \| 1000 \| 500 \| 0.010500 \|$'
  echo "$output" | grep -P '^\| \*\*total\*\* \| 0.010500 \| 0.006600 \| -0.003900 \|$'
}
