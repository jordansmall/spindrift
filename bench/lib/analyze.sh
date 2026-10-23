# shellcheck shell=bash
# bench.sh analyze TASK...: pools every task's runs by factor level, so a
# design with many arms and few reps per arm still answers "worker haiku vs
# sonnet" over a third of all runs. Arm names carry the factors:
# c<M><E>-s<M><E>-w<M><E>-r<M><E> for coordinator, scout, worker, reviewer.

# Prints TASK's runs as task, arm, resolved, cost, duration_ms, overall, peek,
# with overall empty when the run has no usable judge score and peek 1 only
# for a run the peek audit flagged. Columns are found by header name, so
# either file may grow columns.
analyze_rows() {
  local task="$1" dir="$2" scores="$2/judging/SCORES.tsv"
  [ -f "$scores" ] || scores=""
  awk -F'\t' -v OFS='\t' -v task="$task" -v scores="$scores" '
    BEGIN {
      if (scores != "") {
        while ((getline line < scores) > 0) {
          k = split(line, f, "\t")
          if (++sn == 1) { for (i = 1; i <= k; i++) sc[f[i]] = i; continue }
          v = f[sc["overall"]]
          if (v ~ /^[0-9]+(\.[0-9]+)?$/) overall[f[sc["blind"]]] = v
        }
        close(scores)
      }
    }
    FNR == 1 { for (i = 1; i <= NF; i++) mc[$i] = i; next }
    {
      b = $mc["blind"]
      p = ("peek" in mc && $mc["peek"] == 1) ? 1 : 0
      print task, $mc["arm"], $mc["resolved"] + 0, $mc["cost_usd"] + 0, \
        $mc["duration_ms"] + 0, (b != "" && b in overall) ? overall[b] : "", p
    }' "$dir/metrics.tsv"
}

cmd_analyze() {
  [ $# -ge 1 ] || die "usage: bench.sh analyze TASK..."
  local root="${BENCH_OUT_ROOT:-bench/results}" out="${BENCH_ANALYSIS_OUT:-bench/results/analysis.md}"
  local task dir rows unscored=""
  for task in "$@"; do
    [ -f "$root/$task/metrics.tsv" ] || die "no results at $root/$task/metrics.tsv"
  done
  rows="$(mktemp)"
  for task in "$@"; do
    dir="$root/$task"
    [ -f "$dir/judging/SCORES.tsv" ] || unscored+="$task "
    analyze_rows "$task" "$dir" >>"$rows"
  done
  mkdir -p "$(dirname "$out")"
  analyze_report "$unscored" <"$rows" >"$out"
  rm -f "$rows"
  echo "$out"
}

analyze_report() {
  awk -F'\t' -v unscored="$1" '
    BEGIN {
      M["o"] = "opus-5-5"; M["s"] = "sonnet-5-5"; M["h"] = "haiku-5-5"
      E["l"] = "low"; E["m"] = "medium"; E["h"] = "high"; E["x"] = "xhigh"
      nmo = split("o s h", MO, " "); neo = split("l m h x", EO, " ")
      nro = split("c s w r", RO, " ")
      RN["c"] = "coordinator"; RN["s"] = "scout"; RN["w"] = "worker"; RN["r"] = "reviewer"
      # Offset of each role letter in the arm name; model follows, then effort.
      RP["c"] = 1; RP["s"] = 5; RP["w"] = 9; RP["r"] = 13
    }
    {
      if ($7 == 1) {
        npeek++
        if (!(($1 " " $2) in peekseen)) { peekseen[$1 " " $2] = 1; peeked = peeked (peeked == "" ? "" : ", ") $1 " " $2 }
        next
      }
      if ($2 !~ /^c[osh][lmhx]-s[osh][lmhx]-w[osh][lmhx]-r[osh][lmhx]$/) {
        nbad++
        if (!($2 in badseen)) { badseen[$2] = 1; bad = bad (bad == "" ? "" : ", ") $2 }
        next
      }
      N++; T[N] = $1; A[N] = $2; R[N] = $3; C[N] = $4; D[N] = $5 / 60000; J[N] = $6
      if (!($1 in tn)) torder[++nt] = $1
      tn[$1]++; tcost[$1] += $4; tmin[$1] += D[N]
      if (!($2 in an)) aorder[++na] = $2
      an[$2]++
    }
    function add(g, i) {
      n[g]++; res[g] += R[i]; cost[g] += C[i]; mins[g] += D[i]
      cr[g] += CR[i]; mr[g] += MR[i]
      if (J[i] != "") { jn[g]++; js[g] += J[i]; js2[g] += J[i] * J[i] }
    }
    function dag(k) { return k < 4 ? "†" : "" }
    function f2(x, k) { return sprintf("%.2f", x) dag(k) }
    function f1(x, k) { return sprintf("%.1f", x) dag(k) }
    function pct(g) { return int(100 * res[g] / n[g] + 0.5) "%" dag(n[g]) }
    function jmean(g) { return jn[g] ? f1(js[g] / jn[g], jn[g]) : "–" }
    function jsd(g,    m, v) {
      if (!jn[g]) return "–"
      m = js[g] / jn[g]
      if (jn[g] < 2) return sprintf("%.1f ± –", m) dag(jn[g])
      v = (js2[g] - jn[g] * m * m) / (jn[g] - 1)
      return sprintf("%.1f ± %.1f", m, sqrt(v > 0 ? v : 0)) dag(jn[g])
    }
    function perres(g) { return res[g] ? f2(cost[g] / res[g], n[g]) : "–" }
    function spell(a, r,    p) { p = RP[r]; return M[substr(a, p + 1, 1)] "/" E[substr(a, p + 2, 1)] }
    function vtag(g) { return dag(n[g] < jn[g] ? n[g] : jn[g]) }
    function rankrow(k, a) {
      printf "| %d | %s | %s | %s | %s | %s | %d | %s | %s | %s | %s |\n", k, a, \
        spell(a, "c"), spell(a, "s"), spell(a, "w"), spell(a, "r"), n["a:" a], \
        pct("a:" a), jmean("a:" a), f2(cr["a:" a] / n["a:" a], n["a:" a]), \
        sprintf("%.2f", val[a]) vtag("a:" a)
    }
    END {
      for (i = 1; i <= N; i++) {
        t = T[i]; mc = tcost[t] / tn[t]; mm = tmin[t] / tn[t]
        # A task whose runs all cost nothing has every run at its mean.
        CR[i] = mc > 0 ? C[i] / mc : 1; MR[i] = mm > 0 ? D[i] / mm : 1
        add("t:" t, i); add("a:" A[i], i)
        for (q = 1; q <= nro; q++) {
          r = RO[q]; p = RP[r]; m = substr(A[i], p + 1, 1); e = substr(A[i], p + 2, 1)
          add("fm:" r ":" m, i); add("fe:" r ":" e, i); add("g:" r ":" m ":" e, i)
        }
      }

      print "# Bench analysis"
      print ""
      printf "%d run(s) across %d task(s) and %d arm(s). Resolved means the hidden tests pass\n", N, nt, na
      print "(and CHECK_CMD, if set). Judge is the blind judge'"'"'s overall score; a run with no"
      print "score (unjudged, or a judge error) is left out of judge statistics only."
      print ""
      print "† marks a statistic resting on fewer than 4 runs (fewer than 4 judged runs,"
      print "for a judge score): read it as an anecdote, not a finding."
      print ""
      print "A cost ratio (minutes ratio) is a run'"'"'s cost (duration) divided by its task'"'"'s"
      print "mean, so tasks of different sizes pool; 1.00 is an average run."
      if (nbad) {
        print ""
        printf "Excluded %d run(s) whose arm does not parse as `c<M><E>-s<M><E>-w<M><E>-r<M><E>`: %s.\n", nbad, bad
      }
      if (npeek) {
        print ""
        printf "Excluded %d run(s) the peek audit flagged as reaching for another run'"'"'s work: %s.\n", npeek, peeked
      }
      k = split(unscored, us, " ")
      for (i = 1; i <= k; i++) {
        print ""
        printf "%s has no judging/SCORES.tsv; its runs count as unjudged.\n", us[i]
      }

      print ""
      print "## Tasks"
      print ""
      print "| task | runs | resolved | judge mean | judged | cost $ mean | minutes mean |"
      print "| --- | --- | --- | --- | --- | --- | --- |"
      for (i = 1; i <= nt; i++) {
        g = "t:" torder[i]
        printf "| %s | %d | %s | %s | %d | %s | %s |\n", torder[i], n[g], pct(g), jmean(g), \
          jn[g], f2(cost[g] / n[g], n[g]), f1(mins[g] / n[g], n[g])
      }

      print ""
      print "## Main effects"
      print ""
      print "Each factor level pooled over every run that has it, whatever the other factors."
      print "Cost $ / resolved is the level'"'"'s total cost over its resolved runs."
      print ""
      print "| factor | level | n | resolved | judge mean ± sd | judged | cost ratio | minutes ratio | cost $ mean | cost $ / resolved |"
      print "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |"
      for (q = 1; q <= nro; q++) {
        r = RO[q]
        for (x = 1; x <= nmo; x++) {
          g = "fm:" r ":" MO[x]
          if (n[g]) effrow(RN[r] " model", M[MO[x]], g)
        }
        for (x = 1; x <= neo; x++) {
          g = "fe:" r ":" EO[x]
          if (n[g]) effrow(RN[r] " effort", E[EO[x]], g)
        }
      }

      print ""
      print "## Model × effort by role"
      print ""
      print "Cell: judge mean / cost ratio (runs)."
      for (q = 1; q <= nro; q++) {
        r = RO[q]
        print ""
        print "### " RN[r]
        print ""
        head = "| model |"; rule = "| --- |"
        for (x = 1; x <= neo; x++) if (n["fe:" r ":" EO[x]]) { head = head " " E[EO[x]] " |"; rule = rule " --- |" }
        print head; print rule
        for (y = 1; y <= nmo; y++) {
          if (!n["fm:" r ":" MO[y]]) continue
          line = "| " M[MO[y]] " |"
          for (x = 1; x <= neo; x++) {
            if (!n["fe:" r ":" EO[x]]) continue
            g = "g:" r ":" MO[y] ":" EO[x]
            line = line (n[g] ? " " jmean(g) " / " f2(cr[g] / n[g], n[g]) " (" n[g] ") |" : "  |")
          }
          print line
        }
      }

      best = 0
      for (i = 1; i <= na; i++) { g = "a:" aorder[i]; if (res[g] / n[g] > best) best = res[g] / n[g] }
      floor = best - 0.25
      ne = 0; low = ""; nojudge = ""
      for (i = 1; i <= na; i++) {
        a = aorder[i]; g = "a:" a
        # Small tolerance: a rate exactly at the floor stays in.
        if (res[g] / n[g] < floor - 1e-9) { low = low (low == "" ? "" : ", ") a " (" int(100 * res[g] / n[g] + 0.5) "%)"; continue }
        if (!jn[g]) { nojudge = nojudge (nojudge == "" ? "" : ", ") a; continue }
        val[a] = (js[g] / jn[g]) / (cr[g] / n[g])
        el[++ne] = a
      }
      for (i = 2; i <= ne; i++) {
        a = el[i]
        for (j = i - 1; j >= 1 && (val[el[j]] < val[a] || (val[el[j]] == val[a] && el[j] > a)); j--) el[j + 1] = el[j]
        el[j + 1] = a
      }
      print ""
      print "## Arm ranking"
      print ""
      print "value = mean judge overall ÷ mean cost ratio: judge points per average-task"
      print "spend, so 4.0 at half the usual cost scores 8.00. Only arms whose resolved rate"
      printf "is within 25 points of the best arm'"'"'s (%d%%) and that have a judge score rank.\n", int(100 * best + 0.5)
      if (low != "") { print ""; printf "Left out (resolved below %d%%): %s.\n", int(100 * floor + 0.5), low }
      if (nojudge != "") { print ""; printf "Left out (no judge score): %s.\n", nojudge }
      print ""
      print "### Top 10"
      print ""
      rhead()
      for (i = 1; i <= ne && i <= 10; i++) rankrow(i, el[i])
      print ""
      print "### Bottom 5"
      print ""
      if (ne <= 10) print "No arms beyond the top 10."
      else {
        rhead()
        for (i = (ne - 4 > 11 ? ne - 4 : 11); i <= ne; i++) rankrow(i, el[i])
      }

      np = 0
      for (i = 1; i <= na; i++) {
        a = aorder[i]; g = "a:" a
        if (!jn[g]) continue
        pc[a] = cr[g] / n[g]; pj[a] = js[g] / jn[g]; cand[++nc] = a
      }
      for (i = 1; i <= nc; i++) {
        a = cand[i]; dom = 0
        for (j = 1; j <= nc && !dom; j++) {
          b = cand[j]
          if (b != a && pc[b] <= pc[a] && pj[b] >= pj[a] && (pc[b] < pc[a] || pj[b] > pj[a])) dom = 1
        }
        if (!dom) fr[++np] = a
      }
      for (i = 2; i <= np; i++) {
        a = fr[i]
        for (j = i - 1; j >= 1 && pc[fr[j]] > pc[a]; j--) fr[j + 1] = fr[j]
        fr[j + 1] = a
      }
      print ""
      print "## Pareto frontier"
      print ""
      print "Arms no other arm beats on both mean cost ratio (lower) and mean judge (higher),"
      print "cheapest first. Arms with no judge score are left out."
      print ""
      print "| arm | cost ratio | judge | runs |"
      print "| --- | --- | --- | --- |"
      for (i = 1; i <= np; i++) {
        g = "a:" fr[i]
        printf "| %s | %s | %s | %d |\n", fr[i], f2(pc[fr[i]], n[g]), jmean(g), n[g]
      }
    }
    function effrow(f, l, g) {
      printf "| %s | %s | %d | %s | %s | %d | %s | %s | %s | %s |\n", f, l, n[g], pct(g), jsd(g), \
        jn[g], f2(cr[g] / n[g], n[g]), f2(mr[g] / n[g], n[g]), f2(cost[g] / n[g], n[g]), perres(g)
    }
    function rhead() {
      print "| rank | arm | coordinator | scout | worker | reviewer | runs | resolved | judge | cost ratio | value |"
      print "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |"
    }'
}
