#!/usr/bin/env bash
# Renders the Nix evaluation-cost table CI posts for review (issue #5860).
# Reviewed, never gated: every content problem (a missing, empty or
# unparseable file, an absent field) degrades the output and exits 0. Only a
# wrong argument count is a usage error.
#
# A delta whose displayed (rounded) value is under noisePct is labelled noise
# because CI's Determinate Nix evaluates in parallel, so counts jitter slightly
# between identical runs.
set -euo pipefail

if [ "$#" -ne 2 ] && [ "$#" -ne 5 ]; then
  echo "usage: eval-stats-render.sh CURRENT_STATS CURRENT_PROBE [BASELINE_STATS BASELINE_PROBE BASELINE_SHA]" >&2
  exit 2
fi

# A file's JSON object, or null when it is missing, empty or unparseable.
load() {
  jq -c 'select(type == "object")' "$1" 2>/dev/null | head -n1 || true
}

cur_stats=$(load "$1")
cur_probe=$(load "$2")
base_stats='' base_probe='' base_sha=''
if [ "$#" -eq 5 ]; then
  base_stats=$(load "$3")
  base_probe=$(load "$4")
  base_sha=$5
fi

jq -nr \
  --argjson cur_stats "${cur_stats:-null}" --argjson cur_probe "${cur_probe:-null}" \
  --argjson base_stats "${base_stats:-null}" --argjson base_probe "${base_probe:-null}" \
  --arg base_sha "$base_sha" '
  def noisePct: 1;
  def num: if type == "number" then . else null end;
  def fixed($dec):
    (pow(10; $dec)) as $p
    | (. * $p | round) as $r
    | (($r / $p | floor | tostring) + "." + ((("0" * $dec) + ($r % $p | tostring))[-$dec:]));
  def calls: if . == null then "n/a" else tostring end;
  def secs: if . == null then "n/a" else (fixed(2)) + "s" end;
  def mib: if . == null then "n/a" else (. / 1048576 | fixed(1)) + " MiB" end;
  def delta($c; $b):
    if $c == null or $b == null or $b == 0 then "n/a"
    else (($c - $b) / $b * 100) as $d
      | ($d * 10 | round) as $r
      | (if $r < 0 then "-" else "+" end) + (($r | fabs) / 10 | fixed(1)) + "%"
        + (if ($r | fabs) < noisePct * 10 then " (noise)" else "" end)
    end;

  # The try keeps a non-object gc at null: a bare `?` would emit nothing,
  # silently dropping the whole row.
  def heap($s): try $s.gc.heapSize catch null;

  (($base_stats != null and $base_sha != "")) as $has
  # An unusable file is null, and null.field is null, so missing inputs
  # fall through to n/a.
  | def row($name; $c; $b; fmt):
      if $has then
        "| \($name) | \($c | num | fmt) | \($b | num | fmt) | \(delta($c | num; $b | num)) |"
      else "| \($name) | \($c | num | fmt) |" end;
    "### Eval stats",
    "",
    if $cur_stats == null and $cur_probe == null then
      "Eval stats unavailable: the current run produced no usable stats."
    else
      (if $has then "Baseline: `\($base_sha)`" else "no baseline" end),
      "",
      (if $has then "| Metric | Current | Baseline | Δ |\n|---|---:|---:|---:|"
       else "| Metric | Current |\n|---|---:|" end),
      row("Function calls"; $cur_stats.nrFunctionCalls; $base_stats.nrFunctionCalls; calls),
      row("CPU time"; $cur_stats.cpuTime; $base_stats.cpuTime; secs),
      row("Heap"; heap($cur_stats); heap($base_stats); mib),
      row("Marginal instance cost"; $cur_probe.marginalFunctionCalls; $base_probe.marginalFunctionCalls; calls)
    end'
