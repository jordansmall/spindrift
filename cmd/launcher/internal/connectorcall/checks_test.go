package connectorcall_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorcall"
	"spindrift.dev/launcher/internal/connectorprotocol"
)

type chkParams struct{ N int }
type chkResult struct{ V string }

// methodChecked is a forge method and the result check its row names.
func methodChecked(t *testing.T) (name, check string) {
	t.Helper()
	for n, m := range connectorprotocol.Methods {
		if m.Axis == connectorprotocol.AxisForge {
			return n, m.ResultCheck
		}
	}
	t.Fatal("no forge method")
	return "", ""
}

func answering(r chkResult, err error) connectorcall.Call {
	return func(_ context.Context, _ string, _, result any) error {
		*result.(*chkResult) = r
		return err
	}
}

func TestCheckedRunsNamedCheckOnSuccess(t *testing.T) {
	method, check := methodChecked(t)
	refuse := errors.New("refused")
	var gotP chkParams
	checks := connectorcall.Checks{check: connectorcall.Typed(func(p chkParams, r *chkResult) error {
		gotP = p
		if r.V == "bad" {
			return refuse
		}
		r.V += "!" // a repair in place
		return nil
	})}

	var res chkResult
	if err := connectorcall.Checked(answering(chkResult{V: "ok"}, nil), checks)(context.Background(), method, chkParams{N: 7}, &res); err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if res.V != "ok!" || gotP.N != 7 {
		t.Fatalf("repair or params did not reach the caller: res=%+v params=%+v", res, gotP)
	}

	err := connectorcall.Checked(answering(chkResult{V: "bad"}, nil), checks)(context.Background(), method, chkParams{}, &res)
	if !errors.Is(err, refuse) {
		t.Fatalf("want the check's refusal, got %v", err)
	}
}

func TestCheckedSkipsCheckOnCallFailure(t *testing.T) {
	method, check := methodChecked(t)
	boom := errors.New("boom")
	ran := false
	checks := connectorcall.Checks{check: func(_, _ any) error { ran = true; return nil }}
	err := connectorcall.Checked(answering(chkResult{}, boom), checks)(context.Background(), method, chkParams{}, &chkResult{})
	if err != boom || ran {
		t.Fatalf("want the call's error unchanged and no check; err=%v ran=%v", err, ran)
	}
}

func TestCheckedFailsClosed(t *testing.T) {
	method, check := methodChecked(t)
	err := connectorcall.Checked(answering(chkResult{}, nil), connectorcall.Checks{})(context.Background(), method, chkParams{}, &chkResult{})
	if err == nil || !strings.Contains(err.Error(), method) || !strings.Contains(err.Error(), check) {
		t.Fatalf("want an error naming %s and %s, got %v", method, check, err)
	}
	err = connectorcall.Checked(answering(chkResult{}, nil), connectorcall.Checks{})(context.Background(), "no.such", chkParams{}, &chkResult{})
	if err == nil || !strings.Contains(err.Error(), "no.such") {
		t.Fatalf("want an unknown method refused by name, got %v", err)
	}
}

func TestTypedMismatchIsAnError(t *testing.T) {
	c := connectorcall.Typed(func(chkParams, *chkResult) error { return nil })
	for _, tc := range []struct{ p, r any }{{"wrong", &chkResult{}}, {chkParams{}, &chkParams{}}, {chkParams{}, chkResult{}}} {
		if err := c(tc.p, tc.r); err == nil {
			t.Errorf("params %T result %T: want an error", tc.p, tc.r)
		}
	}
}

func TestDecodeChecksNothing(t *testing.T) {
	if err := connectorcall.Decode(nil, nil); err != nil {
		t.Fatal(err)
	}
}

// forgeRegistry has a no-op body for every check a forge method names.
func forgeRegistry() connectorcall.Checks {
	checks := connectorcall.Checks{}
	for _, m := range connectorprotocol.Methods {
		if m.Axis == connectorprotocol.AxisForge {
			checks[m.ResultCheck] = connectorcall.Decode
		}
	}
	return checks
}

func TestUnregisteredAgrees(t *testing.T) {
	if err := connectorcall.Unregistered(connectorprotocol.AxisForge, forgeRegistry()); err != nil {
		t.Fatal(err)
	}
}

func TestUnregisteredNamesMethodWithoutCheck(t *testing.T) {
	for name, m := range connectorprotocol.Methods {
		if m.Axis != connectorprotocol.AxisForge {
			continue
		}
		checks := forgeRegistry()
		delete(checks, m.ResultCheck)
		err := connectorcall.Unregistered(connectorprotocol.AxisForge, checks)
		if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), m.ResultCheck) {
			t.Errorf("without %s: want an error naming %s and %s, got %v", m.ResultCheck, name, m.ResultCheck, err)
		}
	}
}

func TestUnregisteredNamesCheckNoMethodUses(t *testing.T) {
	forgeUses := func(check string) bool {
		for _, m := range connectorprotocol.Methods {
			if m.Axis == connectorprotocol.AxisForge && m.ResultCheck == check {
				return true
			}
		}
		return false
	}
	if forgeUses(connectorprotocol.CheckCountClamp) {
		t.Skip("a forge method now uses countClamp")
	}
	checks := forgeRegistry()
	checks[connectorprotocol.CheckCountClamp] = connectorcall.Decode
	checks["noSuchCheck"] = connectorcall.Decode
	err := connectorcall.Unregistered(connectorprotocol.AxisForge, checks)
	if err == nil || !strings.Contains(err.Error(), connectorprotocol.CheckCountClamp) || !strings.Contains(err.Error(), "noSuchCheck") {
		t.Fatalf("want both stray checks named, got %v", err)
	}
}

func TestUnregisteredIsSorted(t *testing.T) {
	var names []string
	for name, m := range connectorprotocol.Methods {
		if m.Axis == connectorprotocol.AxisForge {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	msg := connectorcall.Unregistered(connectorprotocol.AxisForge, connectorcall.Checks{}).Error()
	last := -1
	for _, n := range names {
		i := strings.Index(msg, n)
		if i < last {
			t.Fatalf("%s out of order in %q", n, msg)
		}
		last = i
	}
}

func TestCoreBugErrorsSatisfyErrCoreBug(t *testing.T) {
	method, _ := methodChecked(t)
	call := connectorcall.Checked(answering(chkResult{}, nil), connectorcall.Checks{})
	typed := connectorcall.Typed(func(chkParams, *chkResult) error { return nil })
	for name, err := range map[string]error{
		"unregistered check": call(context.Background(), method, chkParams{}, &chkResult{}),
		"unknown method":     call(context.Background(), "no.such", chkParams{}, &chkResult{}),
		"typed mismatch":     typed("wrong", &chkResult{}),
	} {
		if !errors.Is(err, connectorcall.ErrCoreBug) {
			t.Errorf("%s: %v does not satisfy ErrCoreBug", name, err)
		}
	}
}
