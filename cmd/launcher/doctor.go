package main

import (
	"bufio"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// runDoctor adapts the launcher's full config to doctor.Config and delegates
// to the shared internal/doctor package (also used in-process by
// Quickstart's finish line, ADR 0027) — this file exists only to keep the
// `spindrift doctor` subcommand's call site (main.go) and its tests
// unchanged by the extraction.
func runDoctor(it forge.IssueTracker, cf forge.CodeForge, c config, w io.Writer, stdin io.Reader, interactive bool) error {
	if err := doctor.Run(it, cf, doctor.Config{
		IssueTracker:    c.issueTracker,
		Label:           c.label,
		InProgressLabel: c.inProgressLabel,
		FailedLabel:     c.failedLabel,
		CompleteLabel:   c.completeLabel,
	}, w, bufio.NewScanner(stdin), interactive); err != nil {
		return err
	}
	return reportReadOnlyTokenGate(c, w)
}

// reportReadOnlyTokenGate surfaces checkReadOnlyTokenGate's and
// checkReadOnlyForgejoTokenGate's outcomes in `spindrift doctor` (issue
// #1950, extended to Forgejo by #1964): read-write prints an explicit no-op
// line so an operator scanning doctor output isn't left wondering whether
// the gates ran; read-only reports each gate's outcome (with any warning the
// gate itself prints) or returns the gate's own fail-closed error, so a
// misconfigured deployment fails doctor exactly as it would fail a live
// dispatch at bootstrap. Each gate self-noops when its backend (github,
// forgejo) isn't active, so both are always called under read-only and only
// the relevant one prints anything beyond its self-noop.
func reportReadOnlyTokenGate(c config, w io.Writer) error {
	// Guarded on read-write (not read-only) so the read-only branch stays the
	// default: boxForgeAndIssueAccess is a schema enum constrained upstream to
	// exactly read-only|read-write, so no third value reaches here — the two
	// gates below self-noop under read-write anyway, making the branch choice a
	// display concern (no-op line vs. gate outcomes) rather than a safety one.
	if c.boxForgeAndIssueAccess != "read-write" {
		return reportReadOnlyTokenGates(c, w)
	}
	fmt.Fprintln(w, "ok: BOX_FORGE_AND_ISSUE_ACCESS=read-write — read-only token gate is a no-op")
	return nil
}

// reportReadOnlyTokenGates runs both backend-specific read-only token gates
// under BOX_FORGE_AND_ISSUE_ACCESS=read-only, reporting the github gate only
// when github is an active backend and the forgejo gate only when forgejo
// is an active backend, since each gate self-noops otherwise.
func reportReadOnlyTokenGates(c config, w io.Writer) error {
	if c.codeForge == "github" || c.issueTracker == "github" {
		verified, err := checkReadOnlyTokenGate(c, ghTokenIntrospector, w)
		if err != nil {
			return err
		}
		if verified {
			fmt.Fprintln(w, "ok: read-only token gate satisfied — BOX_GH_TOKEN is set, distinct, and confirmed not write-capable")
		} else {
			fmt.Fprintln(w, "ok: read-only token gate satisfied — BOX_GH_TOKEN is set and distinct (see warning above: its write capability could not be verified)")
		}
	}
	if c.codeForge == "forgejo" || c.issueTracker == "forgejo" {
		if _, err := checkReadOnlyForgejoTokenGate(c, w); err != nil {
			return err
		}
		fmt.Fprintln(w, "ok: read-only token gate satisfied — BOX_FORGEJO_TOKEN is set and distinct (see warning above: its write capability could not be verified — Forgejo exposes no introspection endpoint)")
	}
	return nil
}
