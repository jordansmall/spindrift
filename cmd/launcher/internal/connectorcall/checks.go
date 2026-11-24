package connectorcall

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/connectorprotocol"
)

// Check is core's body for one named result check: it refuses, or repairs in
// place, the result (a pointer, as passed to Call) a method answered for
// params. The bodies stay hand-written because they encode policy; only their
// registration is tied to the protocol's method table.
type Check func(params, result any) error

// Checks is one axis's registry of check bodies, keyed by the
// connectorprotocol.Check* name the method table gives them.
type Checks map[string]Check

// ErrCoreBug marks a check that cannot run for a reason in core, not in the
// Connector's answer: a method the table lacks, a check the registry lacks, or
// a body registered under the wrong name. A caller must not recover from it as
// from a failed call.
var ErrCoreBug = errors.New("core bug")

// Typed is a Check over concrete types. A params or result of another type is
// a core bug (a body registered under the wrong name), reported as an error
// rather than a panic.
func Typed[P, R any](f func(p P, r *R) error) Check {
	return func(params, result any) error {
		p, okP := params.(P)
		r, okR := result.(*R)
		if !okP || !okR {
			return fmt.Errorf("connectorcall: check body takes (%T, %T), got (%T, %T): %w", *new(P), new(R), params, result, ErrCoreBug)
		}
		return f(p, r)
	}
}

// Decode is the body of connectorprotocol.CheckDecode: core decodes the result
// into its Go type and checks nothing more.
var Decode Check = func(_, _ any) error { return nil }

// Checked is call followed, on success, by the result check the method table
// names for the method. A failed call returns its error with no check run. A
// method the table lacks, or whose check checks does not register, fails
// closed; Unregistered keeps the second from reaching a run.
func Checked(call Call, checks Checks) Call {
	return func(ctx context.Context, method string, params, result any) error {
		if err := call(ctx, method, params, result); err != nil {
			return err
		}
		m, ok := connectorprotocol.Methods[method]
		if !ok {
			return fmt.Errorf("connectorcall: %s is not a protocol method: %w", method, ErrCoreBug)
		}
		check, ok := checks[m.ResultCheck]
		if !ok {
			return fmt.Errorf("connectorcall: %s has no registered result check %q: %w", method, m.ResultCheck, ErrCoreBug)
		}
		return check(params, result)
	}
}

// Unregistered is nil when checks and axis's methods agree, else an error
// naming each method whose result check checks lacks and each registered check
// no method of axis names. Each axis's test runs it on that axis's registry,
// so drift fails a test, not a run.
func Unregistered(axis connectorprotocol.Axis, checks Checks) error {
	var problems []string
	used := map[string]bool{}
	for name, m := range connectorprotocol.Methods {
		if m.Axis != axis {
			continue
		}
		used[m.ResultCheck] = true
		if _, ok := checks[m.ResultCheck]; !ok {
			problems = append(problems, fmt.Sprintf("method %s: no body registered for result check %q", name, m.ResultCheck))
		}
	}
	for name := range checks {
		if !used[name] {
			problems = append(problems, fmt.Sprintf("result check %q: no %s method names it", name, axis))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	slices.Sort(problems)
	return errors.New(strings.Join(problems, "\n"))
}
