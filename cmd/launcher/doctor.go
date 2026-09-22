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

// cmdDoctor is the `doctor` subcommand. It probes each forge seam through its
// own adapter rather than the combined Client, so a CODE_FORGE=git deployment
// checks the actual remote it will push to instead of the IssueTracker's repo
// a second time. It needs no runner/dispatch/settle wiring, so it builds its
// own via newReadContext (issue #2941) instead of bootstrap.
func cmdDoctor(verbose bool) int {
	// doctor never dispatches, so it carries no dispatch kind (issue #2944).
	rc := newReadContext("", false)
	return doctorReport(rc, os.Stdout, os.Stderr, os.Stdin, doctorOptions{interactive: isStdinTTY(), verbose: verbose})
}

// doctorOptions is doctorReport's last parameter. A struct rather than two
// adjacent same-typed bools because Go cannot catch swapped bools at a call
// site (issue #3060).
type doctorOptions struct {
	interactive bool
	verbose     bool
}

// doctorReport runs cmdDoctor's exit-vocabulary classification (issue #2569).
// An invalid configuration never skips runDoctor, so a config-invalid run still
// prints the full report (issue #2559), and both failures explain themselves on
// stderr so redirecting stdout never loses the reason for a non-zero exit. Call
// rc.validation() once: it holds the memoized Probes (issues #3144, #2992).
// opts.verbose gates the report's ok:/advisory: rows (issue #3777); it never
// changes which exit code doctorExitCodeFor returns.
func doctorReport(rc readContext, stdout, stderr io.Writer, stdin io.Reader, opts doctorOptions) int {
	v := rc.validation()
	if v.configErr != nil {
		fmt.Fprintf(stderr, "%s\n", v.configErr)
	}
	rep := doctor.NewReporter(stdout, opts.verbose)
	runErr := runDoctor(rc.issueTracker, rc.codeForge, rc.config, rep, rep.AdvisoryWriter(), stdin, opts.interactive, v.reportChecks)
	if runErr != nil {
		fmt.Fprintf(stderr, "%s\n", runErr)
	}
	return doctorExitCodeFor(v.configErr, runErr)
}

// doctorExitCodeFor maps cmdDoctor's two failure sources to the doctor
// exit-code vocabulary (issues #2569, #2942): 0 healthy, 2 configuration
// invalid, 3 auth or connectivity, 4 required checks failed or declined, 1
// unclassified. Both errors can be non-nil at once, and configErr wins because
// an invalid configuration makes runDoctor's findings unreliable.
func doctorExitCodeFor(configErr, runErr error) int {
	switch {
	case configErr != nil:
		return 2
	case runErr == nil:
		return 0
	case errors.Is(runErr, errReadOnlyGateMisconfigured), errors.Is(runErr, errLaunchGateConfigInvalid):
		return 2
	case errors.Is(runErr, doctor.ErrConnectivity):
		return 3
	case errors.Is(runErr, doctor.ErrRequiredLabelsMissing):
		return 4
	default:
		return 1
	}
}

// runDoctor adapts the launcher's config to doctor.Config and delegates to the
// shared internal/doctor package (ADR 0027). The caller passes extraChecks in
// rather than runDoctor building them from c: rebuilding them here would create
// un-memoized checks and run each credential Probe a second time (issue #3144).
// checkW is separate from rep: it's a gate Check's own operator-facing writer
// (issue #2942 AC5). For `spindrift doctor`, doctorReport hands it rep's own
// AdvisoryWriter() rather than raw stdout, so a passing gate's own prose
// (e.g. the read-only token gates' WARNING: line on a Box token that cannot
// be introspected) obeys the same quiet gate as an advisory: row instead of
// bypassing the Reporter (issue #3777). The two params stay distinct since
// only one of them is the report stream.
func runDoctor(it forge.IssueTracker, cf forge.CodeForge, c config, rep *doctor.Reporter, checkW io.Writer, stdin io.Reader, interactive bool, extraChecks []doctor.Check) error {
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
	}, rep, bufio.NewScanner(stdin), interactive, extraChecks); err != nil {
		return err
	}
	// The two token gates are Applicable only under read-only (issue #2942), so
	// walkSplitGateRegistry prints nothing for them here. Without this line
	// read-write would never mention the token gate. It stays doctor-only:
	// printing it from gatedContext would add stdout noise to preview and
	// bootstrap, which must stay quiet.
	if c.boxForgeAndIssueAccess == "read-write" {
		rep.Success("BOX_FORGE_AND_ISSUE_ACCESS=read-write — read-only token gate is a no-op")
	}
	// Walk the launch gates (issue #2942) through the same
	// splitGateRegistryByNetwork construction gatedContext enforces with, not
	// gateRegistry's raw declaration order, so doctor cannot report a different
	// set or order from what gatedContext enforces.
	return walkSplitGateRegistry(gateRegistry, c, checkW, rep, true)
}
