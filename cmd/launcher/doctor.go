package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// cmdDoctor is the `doctor` subcommand: probe each forge seam through its
// own adapter (not the combined Client) so a CODE_FORGE=git deployment
// checks the actual remote it will push to, not the IssueTracker's repo a
// second time. No runner/dispatch/settle wiring needed, so it builds its
// wiring via newReadContext (issue #2941) rather than going through
// bootstrap.
func cmdDoctor() int {
	rc := newReadContext()
	return doctorReport(rc.issueTracker, rc.codeForge, rc.config, os.Stdout, os.Stderr, os.Stdin, isStdinTTY())
}

// doctorReport runs cmdDoctor's full exit-vocabulary classification (issue
// #2569). It always runs both validateConfig(c) (main.go) and runDoctor — an
// invalid configuration never skips runDoctor, so a config-invalid run still
// prints every ok/MISSING/advisory status line runDoctor would otherwise
// produce, the same full report origin/main's doctor always gave regardless
// of config validity. Either failure's explanation goes to stderr as it's
// found, so a caller that redirects stdout never loses the reason for a
// non-zero exit (AC2) even when both configErr and runErr are non-nil at
// once. doctorExitCodeFor still gives configErr precedence for the exit
// code itself — an invalid configuration makes runDoctor's own result
// unreliable — but both explanations are already on stderr by the time it
// runs.
func doctorReport(it forge.IssueTracker, cf forge.CodeForge, c config, stdout, stderr io.Writer, stdin io.Reader, interactive bool) int {
	configErr := validateConfig(c)
	if configErr != nil {
		fmt.Fprintf(stderr, "%s\n", configErr)
	}
	runErr := runDoctor(it, cf, c, stdout, stdin, interactive)
	if runErr != nil {
		fmt.Fprintf(stderr, "%s\n", runErr)
	}
	return doctorExitCodeFor(configErr, runErr)
}

// doctorExitCodeFor maps cmdDoctor's two failure sources to the doctor
// exit-code vocabulary (issue #2569): 0 healthy (advisory findings
// allowed), 2 configuration invalid — either configErr itself, or a runErr
// wrapping errReadOnlyGateMisconfigured (the read-only token gate's
// misconfiguration errors: BOX_GH_TOKEN/BOX_FORGEJO_TOKEN unset, identical
// to the Launcher's own token, or write-capable), 3 auth or connectivity
// (doctor.ErrConnectivity, which also covers the read-only token gate's own
// introspection failures), 4 required checks failed or declined
// (doctor.ErrRequiredLabelsMissing), 1 reserved for internal/unclassified
// errors. configErr, from validateConfig(c) (main.go), always wins the exit
// code — doctorReport runs runDoctor regardless of configErr (issue #2559's
// full-report behavior), so both can be genuinely non-nil at once; an
// invalid configuration just makes whatever runDoctor found unreliable, so
// it doesn't get to pick the exit code. runErr is never a bare
// errConfigInvalid: that sentinel is bootstrap.go's own validate(c) wrap,
// which doctorReport surfaces separately as configErr, not through
// runDoctor.
func doctorExitCodeFor(configErr, runErr error) int {
	switch {
	case configErr != nil:
		return 2
	case runErr == nil:
		return 0
	case errors.Is(runErr, errReadOnlyGateMisconfigured):
		return 2
	case errors.Is(runErr, doctor.ErrConnectivity):
		return 3
	case errors.Is(runErr, doctor.ErrRequiredLabelsMissing):
		return 4
	default:
		return 1
	}
}

// runDoctor adapts the launcher's full config to doctor.Config and delegates
// to the shared internal/doctor package (also used in-process by
// Quickstart's finish line, ADR 0027) — this file exists only to keep the
// `spindrift doctor` subcommand's call site (main.go) and its tests
// unchanged by the extraction.
func runDoctor(it forge.IssueTracker, cf forge.CodeForge, c config, w io.Writer, stdin io.Reader, interactive bool) error {
	row, _ := backendByName(c.issueTracker)
	if err := doctor.Run(it, cf, doctor.Config{
		IssueTracker:    c.issueTracker,
		TokenHint:       row.DoctorTokenHint,
		SlugHint:        row.DoctorSlugHint,
		Label:           c.label,
		InProgressLabel: c.inProgressLabel,
		FailedLabel:     c.failedLabel,
		CompleteLabel:   c.completeLabel,
		Runtime:         c.runtime,
		MergePolicy:     c.mergeMode,
		BaseBranch:      c.baseBranch,
	}, w, bufio.NewScanner(stdin), interactive, doctorReportChecks(c)); err != nil {
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

// reportReadOnlyTokenGates runs every backendRows entry's read-only token
// gate under BOX_FORGE_AND_ISSUE_ACCESS=read-only, reporting a row's gate
// only when that row is an active backend (its name is the CODE_FORGE or
// ISSUE_TRACKER selection); a row with no readOnlyTokenGate (jira, local,
// git) is skipped outright.
func reportReadOnlyTokenGates(c config, w io.Writer) error {
	for _, row := range backendRows {
		if row.readOnlyTokenGate == nil {
			continue
		}
		if c.codeForge != row.Name && c.issueTracker != row.Name {
			continue
		}
		verified, err := row.readOnlyTokenGate(c, w)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, row.readOnlyGateOkMessage(verified))
	}
	return nil
}
