#!/usr/bin/env bash
# Issue #5862: evaluate the inbox checks with nix-eval-jobs and build each
# derivation as it arrives, so builds overlap eval. Builds run batch by batch:
# each poll's newly evaluated jobs build in one `nix build`, so jobs evaluated
# mid-build wait for that batch. Exits non-zero if any job fails to evaluate or
# build, naming it.
#
#   GATE_INSTALLABLE      flake installable of the checks set (required)
#   GATE_MEMORY_MAX_FILE  cgroup memory limit source (default: the cgroup v2 file)
#   GATE_POLL_SECONDS     idle poll interval while eval runs (default 1)
set -euo pipefail

: "${GATE_INSTALLABLE:?GATE_INSTALLABLE must name the checks set, e.g. .#legacyPackages.<system>.checks-inbox-set}"
memory_max_file="${GATE_MEMORY_MAX_FILE:-/sys/fs/cgroup/memory.max}"
poll="${GATE_POLL_SECONDS:-1}"

# The root cgroup and darwin have no memory.max, and a cgroup with no limit
# reads `max`: both mean uncapped.
limit=max
if [ -r "$memory_max_file" ]; then
  # A file with no trailing newline fails read but still fills $limit.
  limit=
  read -r limit <"$memory_max_file" || [ -n "$limit" ] || limit=max
fi
if [[ "$limit" =~ ^[0-9]+$ ]]; then
  workers=$((limit / (5 * 1024 * 1024 * 1024))) # 5 GiB per worker
  ((workers < 1)) && workers=1
  ((workers > 4)) && workers=4
else
  workers=4
fi

work=$(mktemp -d)
eval_pid=
cleanup() {
  [ -z "$eval_pid" ] || kill "$eval_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

echo "gate: $workers eval workers" >&2

# Issue #6095, ADR 0042: the Box's /nix/var DB is a snapshot taken at start, so a
# `-source` path the host realised since is on disk (in the overlay's lower) yet
# invalid. Eval's source copy then deletes it before re-adding, and the lower's
# host-owned dir cannot be chmodded. Registering a path whose NAR hash proves it
# content-addressed (no references) makes nix reuse it. Only top-level -source
# entries: hashing every invalid path is too slow. A daemon client cannot
# --load-db, hence the writable-DB guard.
register_stale_sources() {
  local store=${NIX_STORE_DIR:-/nix/store} p name narhash size invalid
  [ -w "${NIX_STATE_DIR:-/nix/var/nix}/db/db.sqlite" ] || return 0
  if ! invalid=$(find "$store" -maxdepth 1 -name '*-source' -print0 |
    xargs -0 -r nix-store --check-validity --print-invalid); then
    echo "gate: could not list invalid -source paths; registration skipped for some" >&2
  fi
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    name=${p##*/}
    name=${name#*-}
    narhash=$(nix-hash --type sha256 --base32 "$p") || continue
    [ "$(nix-store --print-fixed-path --recursive sha256 "$narhash" "$name")" = "$p" ] || continue
    size=$(nix-store --dump "$p" | wc -c) || continue
    # --load-db record: path, base16 NAR hash, NAR size, empty deriver, reference count 0.
    if printf '%s\n%s\n%s\n\n0\n' "$p" "$(nix-hash --type sha256 --to-base16 "$narhash")" "$size" |
      nix-store --load-db; then
      echo "gate: registered $p, realised by the host after this Box started" >&2
    fi
  done <<<"$invalid"
  return 0
}
register_stale_sources

# Created here, not by the background job's own redirect, which can lose the
# race against the first poll's read.
: >"$work/jobs"

# Each worker peaks near 4.5 GB at 4 workers (hence 5 GiB per worker above), but
# one worker alone reaches ~10 GB over the whole set, so the ceiling is 12 GiB:
# nix-eval-jobs restarts a worker over its share, and each restart re-pays the
# shared fixtures (a 6 GB ceiling made 2 workers slower than 1).
nix-eval-jobs --flake "$GATE_INSTALLABLE" --workers "$workers" \
  --max-memory-size 12288 >"$work/jobs" &
eval_pid=$!

declare -A attr_of_out drv_of_attr
failed=()
total=0
seen=0

while :; do
  # Sampled before reading, so a line written just before exit is not missed.
  alive=1
  kill -0 "$eval_pid" 2>/dev/null || alive=0

  # wc counts newlines, so a line the eval is still writing is not read yet.
  n=$(wc -l <"$work/jobs")
  drvs=()
  outs=()
  attrs=()
  if ((n > seen)); then
    sed -n "$((seen + 1)),${n}p" "$work/jobs" >"$work/batch"
    total=$((total + n - seen))
    seen=$n
    jq -r 'select(has("error")) | "gate: eval failed: \(.attr)\n\(.error)"' "$work/batch" >&2
    mapfile -t -O "${#failed[@]}" failed < <(jq -r 'select(has("error")) | .attr' "$work/batch")
    while IFS=$'\t' read -ra row; do
      attrs+=("${row[0]}")
      drv_of_attr[${row[0]}]=${row[1]}
      drvs+=("${row[1]}^*")
      for out in "${row[@]:2}"; do
        attr_of_out[$out]=${row[0]}
        outs+=("$out")
      done
    done < <(jq -r 'select(has("error") | not) | [.attr, .drvPath, .outputs[]] | @tsv' "$work/batch")
  fi

  if ((${#drvs[@]} > 0)); then
    build=(nix build --no-link --keep-going -L)
    # One build client at a time keeps the Box's nix.conf max-jobs/cores authoritative.
    # Piping makes stderr a non-tty, so nix's progress bar is lost; -L covers it.
    if ! "${build[@]}" "${drvs[@]}" 2>&1 | tee "$work/build.log" >&2; then
      # A fatal store error aborts the invocation, and --keep-going does not
      # survive that: independent jobs are left unbuilt, so invalid outputs are
      # not a reliable list of the jobs that failed.
      candidates=()
      invalid=$(nix-store --check-validity --print-invalid "${outs[@]}" || true)
      declare -A reported=()
      while IFS= read -r out; do
        [ -n "$out" ] || continue
        a=${attr_of_out[$out]:-}
        [ -n "$a" ] && [ -z "${reported[$a]:-}" ] || continue
        reported[$a]=1
        candidates+=("$a")
      done <<<"$invalid"
      # Empty means nothing attributable (a fetch failed, or invalid outputs map
      # to no known attr) or nix-store errored (`|| true` above): all jobs suspect.
      ((${#candidates[@]} > 0)) || candidates=("${attrs[@]}")

      before=${#failed[@]}
      # The fallback rebuilds serially, giving up cross-job parallelism after an abort.
      for a in "${candidates[@]}"; do
        drv=${drv_of_attr[$a]}
        # A real builder failure is already named; rebuilding it would cost minutes.
        # "dependencies of derivation ... failed" is older nix; "Cannot build" is newer.
        if named=$(grep -m1 -F -e "builder for '$drv' failed" -e "Cannot build '$drv'" \
          -e "dependencies of derivation '$drv' failed to build" "$work/build.log"); then
          echo "gate: build failed: $a: $named" >&2
        elif ! "${build[@]}" "$drv^*"; then
          echo "gate: build failed: $a" >&2
        else
          continue
        fi
        failed+=("$a")
      done
      if ((${#failed[@]} == before)); then
        # A failed batch must never pass, even if every lone rebuild succeeds; the
        # jobs built green alone, so name the batch rather than any of them.
        echo "gate: nix build failed but no job could be attributed; see the log above" >&2
        failed+=("nix build (batch)")
      fi
    fi
  elif ((alive == 0)); then
    break
  else
    sleep "$poll"
  fi
done

eval_status=0
wait "$eval_pid" || eval_status=$?
eval_pid=
if ((eval_status != 0)); then
  echo "gate: nix-eval-jobs exited $eval_status" >&2
  failed+=("nix-eval-jobs")
fi

if ((${#failed[@]} > 0)); then
  echo "gate: ${#failed[@]} job(s) failed: ${failed[*]}" >&2
  exit 1
fi
echo "gate: all $total jobs passed" >&2
