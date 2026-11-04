<!-- spindrift-research -->
## Verdict

**`recommend`** — the fix is small and the cause is clear.

## Context for a worker

- The adapter posts bodies verbatim in `cmd/launcher/internal/forge/jira/jira.go:412`.
  - Nested detail with *emphasis* and a snake_case_name left alone.
  - See [the ADR](https://example.com/docs/adr/0013_jira.md) for the contract.
- Related: https://example.com/issues/6348?x=a_b_c&y=*z*

1. Reproduce with `go test ./...`
2. Confirm the body renders
   1. nested step one
   2. nested step two

> Note: the Box cannot write the tracker itself.

```go
func main() { fmt.Println("**not bold**") }
```

## Filed issues

- [Escape braces in comments](https://example.com/issues/101)
- **Add a retry** (filing failed) — upstream returned 500

## Skipped (deduplicated)

These findings matched an already-filed issue — open, closed, or one this run filed itself — and were not filed again.

- **Rename the knob** — already tracked: #88
