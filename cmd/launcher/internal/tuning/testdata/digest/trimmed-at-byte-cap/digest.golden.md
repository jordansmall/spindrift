# Tuning digest

Window: after (start) through c5 — 8 settled Records, 24 passes, notional cost $78.60 (API-equivalent).
Baseline: 0 settled Records claimed 2026-10-02 to 2026-10-09, 0 passes.
Rows with n < 2 are marked thin. Δ is window minus baseline; — means the baseline has no data.

## Summary

| Anchor | Metric | n | Window | Baseline | Δ | Flag |
|---|---|---|---|---|---|---|
| summary:usd-per-record | USD per Record | 8 | $9.83 | — | — |  |
| summary:passes-per-record | Passes per Record | 8 | 3.0 | — | — |  |
| summary:landed-share | Landed share of work Records | 8 | 75% | — | — |  |
| summary:reverted | Reverted share of merged work | 0 | — | — | — | thin |
| summary:churn | Mean 14-day churn | 0 | — | — | — | thin |

## Roles

| Anchor | Metric | n | Window | Baseline | Δ | Flag |
|---|---|---|---|---|---|---|
| role:implement:passes-per-record | implement passes per Record | 8 | 1.0 | — | — |  |
| role:implement:avg-usd | implement avg USD per pass | 8 | $9.62 | — | — |  |
| role:implement:avg-min | implement avg minutes per pass | 8 | 2.0 | — | — |  |
| role:implement:reverted | implement reverted share | 0 | — | — | — | thin |
| role:implement:churn | implement mean 14-day churn | 0 | — | — | — | thin |
| role:review:passes-per-record | review passes per Record | 8 | 1.0 | — | — |  |
| role:review:avg-usd | review avg USD per pass | 8 | $0.10 | — | — |  |
| role:review:avg-min | review avg minutes per pass | 8 | 1.0 | — | — |  |
| role:review:block-rate | review block rate | 8 | 100% | — | — |  |
| role:review:reverted | review reverted share | 0 | — | — | — | thin |
| role:review:churn | review mean 14-day churn | 0 | — | — | — | thin |
| role:fix:passes-per-record | fix passes per Record | 8 | 1.0 | — | — |  |
| role:fix:avg-usd | fix avg USD per pass | 8 | $0.10 | — | — |  |
| role:fix:avg-min | fix avg minutes per pass | 8 | 1.0 | — | — |  |
| role:fix:reverted | fix reverted share | 0 | — | — | — | thin |
| role:fix:churn | fix mean 14-day churn | 0 | — | — | — | thin |

## Prior findings

The last 2 tuning findings, newest first. For a merged or merged-reopened one, Δ is the metric after its closing PR's merge minus before it, over the Records settled on each side; other states carry no Δ.

| Anchor | State | Revision | Metric | n | Before | After | Δ | Flag |
|---|---|---|---|---|---|---|---|---|
| finding:4 | closed-not-planned | — | churn | — | — | — | — |  |
| finding:3 | open | — | usd-per-record | — | — | — | — |  |

## Outliers

Every failed, blocked, or ambiguous Record, then the 5 most expensive of the rest.

| Anchor | Kind | Key | Outcome | Reason | USD | Why |
|---|---|---|---|---|---|---|
| record:fa | work | 101 | failed | ci-red | $1.20 | failed |

_Trimmed to fit BUTLER_TUNING_DIGEST_BYTES (3000 bytes): 16 evidence items, 6 outliers, and 0 prior findings omitted._
