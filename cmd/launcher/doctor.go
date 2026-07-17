package main

import (
	"bufio"
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
	return doctor.Run(it, cf, doctor.Config{
		IssueTracker:    c.issueTracker,
		Label:           c.label,
		InProgressLabel: c.inProgressLabel,
		FailedLabel:     c.failedLabel,
		CompleteLabel:   c.completeLabel,
	}, w, bufio.NewScanner(stdin), interactive)
}
