package forge

import "fmt"

// RecordLandingCall records a single RecordLanding invocation.
type RecordLandingCall struct {
	Num, Landing string
}

// RecordLanding implements the optional LandingRecorder interface (ADR 0029).
func (tf *IssueTrackerFake) RecordLanding(num, landing string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.RecordLandingCalls = append(tf.RecordLandingCalls, RecordLandingCall{num, landing})
	return tf.RecordLandingErr
}

// RecordLandingPassCall records a single RecordLandingPass invocation.
type RecordLandingPassCall struct {
	Num  string
	Pass int
	Kind string
}

// RecordLandingPass implements the optional LandingPassRecorder interface (issue #2983).
func (tf *IssueTrackerFake) RecordLandingPass(num string, pass int, kind string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.RecordLandingPassCalls = append(tf.RecordLandingPassCalls, RecordLandingPassCall{num, pass, kind})
	return tf.RecordLandingPassErr
}

// CloseIssue implements the optional IssueCloser interface (ADR 0029).
func (tf *IssueTrackerFake) CloseIssue(num string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.CloseIssueCalls = append(tf.CloseIssueCalls, num)
	if tf.CloseIssueErr != nil {
		return tf.CloseIssueErr
	}
	iss, ok := tf.issues[num]
	if !ok {
		return fmt.Errorf("issue %s not found", num)
	}
	iss.State = IssueClosed
	tf.issues[num] = iss
	return nil
}

// CloseMergedIssue implements the optional MergeCloser interface (issue #1892).
// It records into CloseMergedIssueCalls, not CloseIssueCalls.
func (tf *IssueTrackerFake) CloseMergedIssue(num string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.CloseMergedIssueCalls = append(tf.CloseMergedIssueCalls, num)
	if tf.CloseMergedIssueErr != nil {
		return tf.CloseMergedIssueErr
	}
	iss, ok := tf.issues[num]
	if !ok {
		return fmt.Errorf("issue %s not found", num)
	}
	iss.State = IssueClosed
	tf.issues[num] = iss
	return nil
}

// FlagAbandoned implements the optional AbandonedFlagger interface (ADR 0029).
func (tf *IssueTrackerFake) FlagAbandoned(num string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.FlagAbandonedCalls = append(tf.FlagAbandonedCalls, num)
	if tf.FlagAbandonedErr != nil {
		return tf.FlagAbandonedErr
	}
	iss, ok := tf.issues[num]
	if !ok {
		return fmt.Errorf("issue %s not found", num)
	}
	iss.Abandoned = true
	tf.issues[num] = iss
	return nil
}

// PriorClaimState implements the optional PriorClaimStateReader interface.
func (tf *IssueTrackerFake) PriorClaimState(num string) (DispatchState, bool, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if tf.PriorClaimStateErr != nil {
		return Untriaged, false, tf.PriorClaimStateErr
	}
	state, ok := tf.PriorClaimStates[num]
	return state, ok, nil
}
