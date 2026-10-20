#!/usr/bin/env bash
# Render a Nix eval profile as a "where did eval time go" Markdown report:
# top self-sample locations by file:line, plus self samples per file
# (issue #5861).
#
# Input is a folded-stacks file from
#   nix eval ... --eval-profiler flamegraph --eval-profile-file <file>
# where each line is `frame;frame;...;frame <count>` and only the leaf frame
# takes the sample.
set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: eval-profile-top.sh <folded-stacks-file> [repo-root]" >&2
  exit 2
fi

profile=$1
root=${2:-$PWD}

# A missing file reads as empty, so both land on the awk no-samples message.
input=$profile
[ -r "$profile" ] || input=/dev/null

# Byte-wise string compares keep the tie-break order locale independent, and
# shorten() counts «/» as two bytes each (the substr offsets below).
PROFILE=$profile LC_ALL=C awk -v root="${root%/}" -v top_n=15 -v file_n=10 '
# «github:NixOS/nixpkgs/<rev>?narHash=...» -> nixpkgs
function input_name(block,    n, parts) {
  sub(/\?.*/, "", block)
  # A forge ref names the repo in its second segment, locked to a rev or not.
  if (block ~ /^(github|gitlab|sourcehut):/) {
    sub(/^[A-Za-z]+:/, "", block)
    split(block, parts, "/")
    return parts[2]
  }
  sub(/^[A-Za-z0-9+.-]+:/, "", block)
  # path:/nix/store/<hash>-<name>: the hash is noise.
  if (sub(/^\/nix\/store\/[0-9a-z]+-/, "", block)) return block
  n = split(block, parts, "/")
  if (n > 1 && parts[n] ~ /^[0-9a-f]+$/ && length(parts[n]) >= 7 && length(parts[n]) <= 40) n--
  block = parts[n]
  sub(/\.git$/, "", block)
  return block
}

function shorten(path,    end) {
  if (substr(path, 1, 2) == "«") {
    end = index(path, "»")
    # «string» and «stdin» are not files; keep the markers.
    if (end && end + 2 <= length(path)) return input_name(substr(path, 3, end - 3)) substr(path, end + 2)
    if (end) return path
  }
  if (match(path, /^\/nix\/store\/[0-9a-z]+-/)) return substr(path, RLENGTH + 1)
  if (root != "" && index(path, root "/") == 1) return substr(path, length(root) + 2)
  return path
}

function pct(n) { return sprintf("%.1f%%", 100 * n / total) }

# Print a Markdown table of the top `limit` keys of arr by count desc, key asc.
function table(arr, limit, ranked, col,    done, k, best, found, i, rows) {
  for (k in arr) rows++
  print "| " (ranked ? "# | " : "") col " | Self samples | % |"
  print "|" (ranked ? "--:|" : "") "---|--:|--:|"
  for (i = 1; i <= limit && i <= rows; i++) {
    found = 0
    for (k in arr) {
      if (k in done) continue
      if (!found || arr[k] > arr[best] || (arr[k] == arr[best] && k < best)) { best = k; found = 1 }
    }
    done[best] = 1
    print "| " (ranked ? i " | " : "") "`" best "` | " arr[best] " | " pct(arr[best]) " |"
  }
}

{
  sub(/\r$/, "")
  sp = 0
  for (i = length($0); i > 0; i--) if (substr($0, i, 1) == " ") { sp = i; break }
  count = substr($0, sp + 1)
  if (sp == 0 || count !~ /^[0-9]+$/) next
  stack = substr($0, 1, sp - 1)
  n = split(stack, frames, ";")
  leaf = frames[n]
  total += count

  if (match(leaf, /:[0-9]+:[0-9]+(:|$)/)) {
    path = substr(leaf, 1, RSTART - 1)
    pos = substr(leaf, RSTART + 1)
    split(pos, p, ":")
    func_name = substr(pos, length(p[1]) + length(p[2]) + 3)
    file = shorten(path)
    key = file ":" p[1]
    if (func_name ~ /^primop /) key = key " (" func_name ")"
  } else {
    file = key = leaf
  }
  by_loc[key] += count
  by_file[file] += count
}

END {
  if (total == 0) {
    print "No eval profile samples: " ENVIRON["PROFILE"] " is missing or has no samples."
    exit
  }
  print "## Eval profile: top " top_n " by self samples"
  print ""
  print "Total samples: " total
  print ""
  table(by_loc, top_n, 1, "Location")
  print ""
  print "## Self samples by file"
  print ""
  table(by_file, file_n, 0, "File")
}
' "$input"
