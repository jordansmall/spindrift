# Tuning digest

Window: after (start) through a4 — 9 settled Records, 9 passes, notional cost $55.00 (API-equivalent).
Baseline: 0 settled Records claimed 2026-10-02 to 2026-10-09, 0 passes.
Rows with n < 2 are marked thin. Δ is window minus baseline; — means the baseline has no data.

## Summary

| Anchor | Metric | n | Window | Baseline | Δ | Flag |
|---|---|---|---|---|---|---|
| summary:usd-per-record | USD per Record | 9 | $6.11 | — | — |  |
| summary:passes-per-record | Passes per Record | 9 | 1.0 | — | — |  |
| summary:landed-share | Landed share of work Records | 9 | 78% | — | — |  |
| summary:reverted | Reverted share of merged work | 0 | — | — | — | thin |
| summary:churn | Mean 14-day churn | 0 | — | — | — | thin |

## Roles

| Anchor | Metric | n | Window | Baseline | Δ | Flag |
|---|---|---|---|---|---|---|
| role:implement:passes-per-record | implement passes per Record | 9 | 1.0 | — | — |  |
| role:implement:avg-usd | implement avg USD per pass | 9 | $6.11 | — | — |  |
| role:implement:avg-min | implement avg minutes per pass | 9 | 2.0 | — | — |  |
| role:implement:reverted | implement reverted share | 0 | — | — | — | thin |
| role:implement:churn | implement mean 14-day churn | 0 | — | — | — | thin |

## Prior findings

The last 6 tuning findings, newest first. For a merged or merged-reopened one, Δ is the metric after its closing PR's merge minus before it, over the Records settled on each side; other states carry no Δ.

| Anchor | State | Revision | Metric | n | Before | After | Δ | Flag |
|---|---|---|---|---|---|---|---|---|
| finding:12 | open | — | churn | — | — | — | — |  |
| finding:11 | closed-completed | — | reverted | — | — | — | — |  |
| finding:10 | closed-not-planned | — | landed-share | — | — | — | — |  |
| finding:9 | open | — | usd-per-record | — | — | — | — |  |
| finding:8 | merged-reopened | def8888 | passes-per-record | 3 | 1.0 | 1.0 | +0.0 |  |
| finding:7 | merged | abc7777 | avg-usd | 6 | $3.00 | $7.67 | +4.67 |  |

## Outliers

Every failed, blocked, or ambiguous Record, then the 5 most expensive of the rest.

| Anchor | Kind | Key | Outcome | Reason | USD | Why |
|---|---|---|---|---|---|---|
| record:a4 | work | 109 | complete | merged | $12.00 | cost |
| record:a3 | work | 108 | complete | merged | $10.00 | cost |
| record:a2 | work | 105 | complete | merged | $8.00 | cost |
| record:a1 | work | 104 | complete | merged | $6.00 | cost |
| record:held8 | work | 8 | complete | tuning-provenance | $5.00 | cost |
