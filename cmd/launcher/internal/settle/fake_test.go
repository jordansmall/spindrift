package settle

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
)

// TestFake_ImplementsSettler asserts that *Fake satisfies Settler.
func TestFake_ImplementsSettler(t *testing.T) {
	var _ Settler = NewFake()
}

// TestFake_RecordsCalls verifies the Fake records Settle invocations for
// callers that only need to assert wiring.
func TestFake_RecordsCalls(t *testing.T) {
	f := NewFake()
	d := dispatch.NewFake()
	result := dispatch.Result{}

	f.Settle(d, "1", 7, result)

	if len(f.SettleCalls) != 1 || f.SettleCalls[0].Num != "1" || f.SettleCalls[0].Gen != 7 {
		t.Errorf("SettleCalls = %+v, want one call for num=1, gen=7", f.SettleCalls)
	}
}

func beginOK() (dispatch.Dispatcher, error) { return dispatch.NewFake(), nil }

func TestFake_RecoverRecordsAfterBeginAndFiresHook(t *testing.T) {
	f := NewFake()
	fired := 0
	f.On("Recover", func(int) { fired++ })
	f.RecoverErr = errors.New("relay: boom")
	f.Settlement = Settlement{PRURL: "u"}

	st, err := f.Recover(RecoverInput{Num: "1", OpenPR: "u", Strict: true, Begin: beginOK})

	if fired != 1 || !errors.Is(err, f.RecoverErr) || st.PRURL != "u" {
		t.Errorf("fired=%d st=%+v err=%v, want hook once, Settlement and RecoverErr", fired, st, err)
	}
	if len(f.RecoverCalls) != 1 || f.RecoverCalls[0].Num != "1" || f.RecoverCalls[0].OpenPR != "u" || !f.RecoverCalls[0].Strict {
		t.Errorf("RecoverCalls = %+v, want one call for num=1", f.RecoverCalls)
	}
}

func TestFake_RecoverBeginErrorRecordsNothing(t *testing.T) {
	f := NewFake()
	boom := errors.New("claim refused")

	_, err := f.Recover(RecoverInput{Num: "1", Begin: func() (dispatch.Dispatcher, error) { return nil, boom }})

	if !errors.Is(err, boom) || len(f.RecoverCalls) != 0 {
		t.Errorf("err=%v calls=%+v, want Begin's error and no recorded call", err, f.RecoverCalls)
	}
}

func TestFake_RecoverDeclinesSkipBegin(t *testing.T) {
	begin := func() (dispatch.Dispatcher, error) { t.Fatal("Begin called on a decline"); return nil, nil }

	f := NewFake()
	f.RecoverDecline = Settlement{Verdict: ForReason(dispatchrecord.ReasonUnreviewed)}
	if st, err := f.Recover(RecoverInput{Begin: begin}); err != nil || st != f.RecoverDecline {
		t.Errorf("decline: st=%+v err=%v, want RecoverDecline, nil", st, err)
	}

	f = NewFake()
	f.RecoverNothing = true
	if _, err := f.Recover(RecoverInput{Begin: begin}); !errors.Is(err, ErrNothingToRecover) {
		t.Errorf("nothing: err=%v, want ErrNothingToRecover", err)
	}
}
