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

// reportReadOnlyTokenGate surfaces checkReadOnlyTokenGate's outcome in
// `spindrift doctor` (issue #1950): read-write prints an explicit no-op line
// so an operator scanning doctor output isn't left wondering whether the
// gate ran; read-only either reports success (with any warning the gate
// itself prints) or returns the gate's own fail-closed error, so a
// misconfigured deployment fails doctor exactly as it would fail a live
// dispatch at bootstrap.
func reportReadOnlyTokenGate(c config, w io.Writer) error {
	if c.boxForgeAndIssueAccess != "read-only" {
		fmt.Fprintln(w, "ok: BOX_FORGE_AND_ISSUE_ACCESS=read-write — read-only token gate is a no-op")
		return nil
	}
	if err := checkReadOnlyTokenGate(c, ghTokenIntrospector, w); err != nil {
		return err
	}
	fmt.Fprintln(w, "ok: read-only token gate satisfied — BOX_GH_TOKEN is set, distinct, and not write-capable")
	return nil
}
