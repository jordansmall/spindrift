package main

import (
	"errors"
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/exitcode"
	"spindrift.dev/launcher/internal/waves"
)

// dispatchProducers gives, per dispatch exit code, the launcher-side result
// that must produce it. A new exitcode.DispatchTable row fails
// TestDispatchExitCodes_AgreeWithTable until it gets an entry here.
var dispatchProducers = map[exitcode.Dispatch]func() int{
	exitcode.Dispatched:       func() int { return exitCodeFor(nil) },
	exitcode.Failed:           func() int { return exitCodeFor(errors.New("boom")) },
	exitcode.QueueEmpty:       func() int { return exitCodeFor(fmt.Errorf("drain: %w", errQueueEmpty)) },
	exitcode.NoneDispatchable: func() int { return exitCodeFor(fmt.Errorf("drain: %w", waves.ErrOpenNoneDispatchable)) },
	exitcode.ImageStale:       func() int { return exitCodeFor(fmt.Errorf("drain: %w", waves.ErrImageStale)) },
	exitcode.HostTainted:      func() int { return exitCodeFor(fmt.Errorf("drain: %w", errImageHostTainted)) },
	exitcode.ConfigInvalid: func() int {
		return bootstrapExitCode(fmt.Errorf("%w: %w", errConfigInvalid, errors.New("REPO_SLUG is required")))
	},
	exitcode.SignalledStop: func() int { return exitCodeFor(fmt.Errorf("drain: %w", waves.ErrSignalledStop)) },
}

// haltClassFor is the daemon halt class each exitcode.Halt must surface as.
var haltClassFor = map[exitcode.Halt]daemon.HaltClass{
	exitcode.NoHalt:            daemon.HaltNone,
	exitcode.HaltHostTainted:   daemon.HaltChildHostTainted,
	exitcode.HaltConfigInvalid: daemon.HaltChildConfigInvalid,
	exitcode.HaltSignalled:     daemon.HaltChildSignalled,
}

// The launcher's error-to-exit mappers and the daemon's Interpret must agree
// on every row of exitcode.DispatchTable.
func TestDispatchExitCodes_AgreeWithTable(t *testing.T) {
	for _, row := range exitcode.DispatchTable {
		t.Run(row.Name, func(t *testing.T) {
			produce, ok := dispatchProducers[row.Code]
			if !ok {
				t.Fatalf("no launcher-side producer for %s; add one to dispatchProducers", row.Name)
			}
			code := produce()
			if code != int(row.Code) {
				t.Fatalf("launcher mapping for %s = %d, want %d", row.Name, code, row.Code)
			}

			for _, stopClosed := range []bool{true, false} {
				outcome, action, halt := daemon.Interpret(code, stopClosed)
				wantAction, wantHalt := row.Verdict(stopClosed)
				if outcome != row.Outcome || action != wantAction {
					t.Errorf("Interpret(%d, %v) = (%q, %v), want (%q, %v)", code, stopClosed, outcome, action, row.Outcome, wantAction)
				}
				if want := haltClassFor[wantHalt]; halt != want {
					t.Errorf("Interpret(%d, %v) halt = %v, want %v (row halt %v)", code, stopClosed, halt, want, wantHalt)
				}
			}
		})
	}
}

// doctorProducers gives, per doctor exit code, the (configErr, runErr) pair
// that must produce it.
var doctorProducers = map[exitcode.Doctor]struct{ configErr, runErr error }{
	exitcode.DoctorHealthy:               {nil, nil},
	exitcode.DoctorUnclassified:          {nil, errors.New("boom")},
	exitcode.DoctorConfigInvalid:         {errors.New("bad config"), nil},
	exitcode.DoctorConnectivity:          {nil, fmt.Errorf("%w: boom", doctor.ErrConnectivity)},
	exitcode.DoctorRequiredLabelsMissing: {nil, fmt.Errorf("%w: boom", doctor.ErrRequiredLabelsMissing)},
}

// doctorExitCodeFor and the daemon's ClassifyPreflight must agree on every row
// of exitcode.DoctorTable.
func TestDoctorExitCodes_AgreeWithTable(t *testing.T) {
	for _, row := range exitcode.DoctorTable {
		t.Run(row.Name, func(t *testing.T) {
			p, ok := doctorProducers[row.Code]
			if !ok {
				t.Fatalf("no launcher-side producer for %s; add one to doctorProducers", row.Name)
			}
			code := doctorExitCodeFor(p.configErr, p.runErr)
			if code != int(row.Code) {
				t.Fatalf("doctorExitCodeFor for %s = %d, want %d", row.Name, code, row.Code)
			}
			v := daemon.ClassifyPreflight(code)
			if v.Outcome != row.Outcome || v.Healthy != row.Healthy || v.Detail != row.Detail || v.Remedy != row.Remedy {
				t.Errorf("ClassifyPreflight(%d) = %+v, want row %+v", code, v, row)
			}
		})
	}
}
