# Tuning digest

Window: after b through w2 — 2 settled Records, 2 passes, notional cost $8.00 (API-equivalent).
Baseline: 1 settled Records claimed 2026-10-02 to 2026-10-09, 1 passes.
Rows with n < 5 are marked thin. Δ is window minus baseline; — means the baseline has no data.

## Summary

| Anchor | Metric | n | Window | Baseline | Δ | Flag |
|---|---|---|---|---|---|---|
| summary:usd-per-record | USD per Record | 2 | $4.00 | $2.00 | +2.00 | thin |
| summary:passes-per-record | Passes per Record | 2 | 1.0 | 1.0 | +0.0 | thin |
| summary:landed-share | Landed share of work Records | 2 | 50% | 100% | -50pp | thin |
| summary:reverted | Reverted share of merged work | 0 | — | — | — | thin |
| summary:churn | Mean 14-day churn | 0 | — | — | — | thin |

## Roles

| Anchor | Metric | n | Window | Baseline | Δ | Flag |
|---|---|---|---|---|---|---|
| role:implement:passes-per-record | implement passes per Record | 2 | 1.0 | 1.0 | +0.0 | thin |
| role:implement:avg-usd | implement avg USD per pass | 2 | $4.00 | $2.00 | +2.00 | thin |
| role:implement:avg-min | implement avg minutes per pass | 2 | 2.0 | 2.0 | +0.0 | thin |
| role:implement:reverted | implement reverted share | 0 | — | — | — | thin |
| role:implement:churn | implement mean 14-day churn | 0 | — | — | — | thin |

## Outliers

Every failed, blocked, or ambiguous Record, then the 5 most expensive of the rest.

| Anchor | Kind | Key | Outcome | Reason | USD | Why |
|---|---|---|---|---|---|---|
| record:w2 | work | 103 | failed | ci-red | $5.00 | failed |
| record:w1 | work | 102 | complete | merged | $3.00 | cost |
