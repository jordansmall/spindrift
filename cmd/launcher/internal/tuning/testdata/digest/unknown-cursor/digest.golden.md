# Tuning digest

Window: after pruned-by-store through u3 — 3 settled Records, 3 passes, notional cost $9.00 (API-equivalent).
Baseline: 0 settled Records claimed 2026-10-02 to 2026-10-09, 0 passes.
Rows with n < 2 are marked thin. Δ is window minus baseline; — means the baseline has no data.

## Summary

| Anchor | Metric | n | Window | Baseline | Δ | Flag |
|---|---|---|---|---|---|---|
| summary:usd-per-record | USD per Record | 3 | $3.00 | — | — |  |
| summary:passes-per-record | Passes per Record | 3 | 1.0 | — | — |  |
| summary:landed-share | Landed share of work Records | 3 | 67% | — | — |  |
| summary:reverted | Reverted share of merged work | 0 | — | — | — | thin |
| summary:churn | Mean 14-day churn | 0 | — | — | — | thin |

## Roles

| Anchor | Metric | n | Window | Baseline | Δ | Flag |
|---|---|---|---|---|---|---|
| role:implement:passes-per-record | implement passes per Record | 3 | 1.0 | — | — |  |
| role:implement:avg-usd | implement avg USD per pass | 3 | $3.00 | — | — |  |
| role:implement:avg-min | implement avg minutes per pass | 3 | 2.0 | — | — |  |
| role:implement:reverted | implement reverted share | 0 | — | — | — | thin |
| role:implement:churn | implement mean 14-day churn | 0 | — | — | — | thin |

## Outliers

Every failed, blocked, or ambiguous Record, then the 5 most expensive of the rest.

| Anchor | Kind | Key | Outcome | Reason | USD | Why |
|---|---|---|---|---|---|---|
| record:u2 | work | 102 | failed | ci-red | $3.00 | failed |
| record:u3 | work | 103 | complete | merged | $4.00 | cost |
| record:u1 | work | 101 | complete | merged | $2.00 | cost |
