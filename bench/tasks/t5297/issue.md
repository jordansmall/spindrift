---
title: "driver-exec signal silently drops every flag after a stray positional argument"
state: ready-for-agent
---
## Problem

`parseSignalFlags` (`cmd/launcher/driver-exec/signal_cmd.go:192-201`) calls `fs.Parse(rest)` and treats a nil error as success. Go's `flag` package stops parsing at the first non-flag argument. Nothing in `runSignal` checks `fs.NArg()`, and no verb in `driver-exec` checks `fs.Args()` either. So one stray positional token, usually an unquoted multi-word value, ends flag parsing. Every flag after it is then dropped with no error. The verbs accept no positionals at all, so any leftover argument is a usage error.

## Failure scenario

An agent runs:

    driver-exec signal issue-intent -title 'Fix race' -type bug -class correctness stray -dedup a.go:Sym -patch-file p.diff

`-dedup` and `-patch-file` are never parsed. The class and type checks still pass, so the intent goes to the socket with no dedup terms and no patch, and exits 0. The host then files a duplicate-prone issue with its patch missing. Verified against a built binary: the same command with `-patch-file /nonexistent` and no stray token fails with `read patch file: open /nonexistent`. With the stray token in place, it gets past the patch read entirely. Likewise, an unquoted `-concurrence Reviewer agrees ...` keeps only the first word, and every flag after it is lost. When the dropped flag is a required one, the failure is a misleading `-type is required` even though `-type` was passed.

## Suggested fix

In `parseSignalFlags`, after a successful `fs.Parse`, reject `fs.NArg() > 0` through `signalUsageFailure`. Name the first leftover token, and hint that a multi-word value needs quoting. Add a `signal_cmd_test.go` case for an `issue-intent` with a stray positional before `-dedup`.

## Acceptance criteria

- [ ] `parseSignalFlags` rejects leftover positional arguments via `signalUsageFailure`, naming the first leftover token and hinting that multi-word values need quoting.
- [ ] A `signal_cmd_test.go` case covers an `issue-intent` with a stray positional before `-dedup` and asserts rejection.
- [ ] `go test` for the `driver-exec` package passes.

Found by butler `bugs` Chore (dedup key `cmd/launcher/driver-exec/signal_cmd.go:parseSignalFlags`).


Filed by the butler's `bugs` Chore. Class: `input-validation`. Files: `cmd/launcher/driver-exec/signal_cmd.go`.

<!-- spindrift-dedup: cmd:launcher:driver-exec:signal:cmd:go:parsesignalflags -->
