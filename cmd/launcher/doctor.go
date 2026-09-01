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
// checks the actual remote it will push to instead of the IssueTracker's repo a
// second time.
func cmdDoctor() int {
	// "" rather than dispatchKindWork: doctor never dispatches, so it carries no
	// dispatch kind at all.
	rc := newReadContext("", false)
	return doctorReport(rc.issueTracker, rc.codeForge, rc.config, os.Stdout, os.Stderr, os.Stdin, isStdinTTY())
}

// doctorReport runs cmdDoctor's full exit-vocabulary classification. An invalid
// configuration deliberately does not skip runDoctor, so a config-invalid run
// still prints every ok/MISSING/advisory status line. Both failures' explanations
// go to stderr as they are found, so a caller redirecting stdout never loses the
// reason for a non-zero exit even when configErr and runErr are both non-nil.
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
// exit-code vocabulary: 0 healthy (advisory findings allowed), 2 configuration
// invalid, 3 auth or connectivity, 4 required checks failed or declined, 1
// internal/unclassified.
//
// configErr always wins the exit code: both can be genuinely non-nil at once,
// and an invalid configuration makes whatever runDoctor found unreliable.
// runErr is never a bare errConfigInvalid — that sentinel is bootstrap.go's own
// validate(c) wrap, surfaced separately as configErr.
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

// runDoctor adapts the launcher's full config to doctor.Config and delegates to
// the shared internal/doctor package (also used in-process by Quickstart's
// finish line, ADR 0027).
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
	// The two token gates are Applicable only under read-only, so walkGateRegistry
	// prints nothing about them under read-write rather than a false "ok" for a
	// check that never ran. Print the operator-facing no-op line here instead —
	// doctor-only, since gatedContext's enforcement path must stay quiet for
	// preview/bootstrap.
	if c.boxForgeAndIssueAccess == "read-write" {
		fmt.Fprintln(w, "ok: BOX_FORGE_AND_ISSUE_ACCESS=read-write — read-only token gate is a no-op")
	}
	// Walk through walkSplitGateRegistry — the same splitGateRegistryByNetwork
	// construction gatedContext uses for enforcement, not gateRegistry's raw
	// declaration order — so "doctor reports what gatedContext enforces" holds
	// regardless of future edits to the registry.
	return walkSplitGateRegistry(gateRegistry, c, w, w, true)
}
