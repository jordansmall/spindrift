package main

import (
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/runner"
)

// lookupRunnerStrategy is a seam so a test can install a stub Strategy without
// touching the global runner registry.
var lookupRunnerStrategy = runner.Lookup

// runnerPrerequisites returns the active runner family's prerequisites for c.
// An unregistered RUNNER_KIND yields nil: it is reported by doctor's
// registry-proxy-transport row and fails runner construction (runnerForKind)
// at launch, and no prerequisite row can name it.
func runnerPrerequisites(c config) []runner.Prerequisite {
	s, err := lookupRunnerStrategy(c.runnerKind)
	if err != nil {
		return nil
	}
	return s.PrerequisitesFor(runnerConfig(c), c.knobLookup)
}

// checkRunnerLaunchPrerequisites runs each Required LaunchGate prerequisite in
// launch order (runner.LaunchGates), returning the first Probe error verbatim.
// Doctor-only, LaunchWithConfig (enforced earlier, by validate) and Advisory
// prerequisites never run here.
func checkRunnerLaunchPrerequisites(ps []runner.Prerequisite) error {
	for _, p := range runner.LaunchGates(ps, runner.LaunchGate) {
		if _, err := p.Probe(); err != nil {
			return err
		}
	}
	return nil
}

// runtimePrerequisiteProbe is the Probe of c's runner family's runtime
// prerequisite, or nil when the family declares none (doctor then falls back
// to the default runtime probe).
func runtimePrerequisiteProbe(c config) func() (any, error) {
	for _, p := range runner.LaunchGates(runnerPrerequisites(c), runner.LaunchWithConfig) {
		if p.Name == runner.RuntimePrerequisiteName {
			return p.Probe
		}
	}
	return nil
}

// prerequisiteChecks splits c's runner prerequisites into the classify half
// (the doctor-only ones) and the report half (all but the LaunchWithConfig
// ones, in declaration order). A doctor-only prerequisite's Required failure
// must fail doctor because doctor is its only enforcement -- the daemon's
// startup preflight reads doctor's exit code (issue #3544). The Advisory
// bwrap-cgroup-delegation row joins classify too but never blocks, since
// doctor.Blocking ignores Advisory. A launch gate is in report only: the
// launch enforces it, and a host missing pasta is an installation fault, not
// a configuration one (issue #2671). A LaunchWithConfig prerequisite (the
// runtime row) is in neither half: validate enforces it, and doctor.Run
// renders it on its own advisory line.
func prerequisiteChecks(c config) (classify, report []doctor.Check) {
	ps := runnerPrerequisites(c)
	all := make([]doctor.Check, len(ps))
	for i, p := range ps {
		all[i] = doctor.PrerequisiteCheck(p)
	}
	// One memoized slice feeds both halves, so a probe runs once per call.
	memoized := memoizeCheckProbes(all)
	for i, p := range ps {
		if p.Launch == runner.LaunchWithConfig {
			continue
		}
		report = append(report, memoized[i])
		if p.Launch == runner.DoctorOnly {
			classify = append(classify, memoized[i])
		}
	}
	return classify, report
}
