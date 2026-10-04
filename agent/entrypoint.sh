#!/usr/bin/env bash
# The Box's generated shim (ADR 0058): lib/image.nix prepends the export and
# default preambles, and box does everything else (cmd/launcher/box/main.go).
# The flags carry the preamble's non-exported values because exporting them
# would change the Driver's environment.
set -euo pipefail

exec box \
  --work-dir "${WORK_DIR:-}" \
  --outbox-dir "${OUTBOX_DIR:-}" \
  --branch-prefix "${BRANCH_PREFIX:-}" \
  --driver-bash-timeout-ms "${DRIVER_BASH_TIMEOUT_MS:-}" \
  --driver-bash-timeout-env "${DRIVER_BASH_TIMEOUT_ENV:-}" \
  --dev-shell-name "${DEV_SHELL_NAME:-}" \
  --dev-shell-probe-timeout "${DEV_SHELL_PROBE_TIMEOUT:-}" \
  --forbidden-markers-registry "$FORBIDDEN_MARKERS_REGISTRY_FILE" \
  --registry "$PROMPTASSEMBLY_REGISTRY_FILE" \
  --validate-markers-registry "$PROMPT_CONTRACT_REGISTRY_FILE" \
  --driver-skills-dir "$DRIVER_SKILLS_DIR" \
  --driver-session-cache-dir "${DRIVER_SESSION_CACHE_DIR:-}" \
  --prompts-dir "$PROMPTS_DIR" \
  --agents-prompt-files "${AGENTS_PROMPT_FILES:-}" \
  --driver-agent-files-dir "${DRIVER_AGENT_FILES_DIR:-}" \
  --comms-contract-file "$COMMS_CONTRACT_FILE" \
  --check-contract-file "$CHECK_CONTRACT_FILE" \
  --outcome-contract-file "$OUTCOME_CONTRACT_FILE" \
  --research-outcome-contract-file "$RESEARCH_OUTCOME_CONTRACT_FILE" \
  --argv-prompt-style "$DRIVER_ARGV_PROMPT_STYLE" \
  --argv-prompt-flag "${DRIVER_ARGV_PROMPT_FLAG:-}" \
  --argv-model-flag "$DRIVER_ARGV_MODEL_FLAG" \
  --argv-model-omit-empty="${DRIVER_ARGV_MODEL_OMIT_EMPTY:-}" \
  --argv-agents-flag "${DRIVER_ARGV_AGENTS_FLAG:-}" \
  --argv-effort-flag "$DRIVER_ARGV_EFFORT_FLAG" \
  --argv-order "$DRIVER_ARGV_ORDER" \
  --model "${MODEL:-}" \
  --effort "${EFFORT:-}" \
  --driver "$DRIVER_NAME" \
  --driver-bin "$DRIVER_BIN" \
  --driver-flags "$DRIVER_FLAGS_COMMON" \
  --heartbeat-log "${HEARTBEAT_LOG:-}" \
  --max-budget-tokens "${MAX_BUDGET_TOKENS:-0}" \
  --max-budget-usd "${MAX_BUDGET_USD:-0}"
