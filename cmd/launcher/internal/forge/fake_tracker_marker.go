package forge

import "fmt"

// RecordLandingCall records a single RecordLanding invocation.
type RecordLandingCall struct {
	Num, Landing string
}

// RecordLanding implements the optional LandingRecorder surface (ADR 0029),
// recording each call for tests to assert against.
func (tf *IssueTrackerFake) RecordLanding(num, landing string) error {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.RecordLandingCalls = append(tf.RecordLandingCalls, RecordLandingCall{num, landing})
	return tf.RecordLandingErr
}

// CloseIssue implements the optional IssueCloser surface (ADR 0029), setting
// the issue's State to IssueClosed and recording the call for tests to
// assert against.
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

// CloseMergedIssue implements the optional MergeCloser surface (issue
// #1892), setting the issue's State to IssueClosed and recording the call
// (separately from CloseIssueCalls) for tests to assert against.
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

// FlagAbandoned implements the optional AbandonedFlagger surface (ADR 0029),
// setting the issue's Abandoned field and recording the call for tests to
// assert against.
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

// PriorClaimState implements the optional PriorClaimStateReader surface,
// returning the issue's scripted PriorClaimStates entry.
func (tf *IssueTrackerFake) PriorClaimState(num string) (DispatchState, bool, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if tf.PriorClaimStateErr != nil {
		return Untriaged, false, tf.PriorClaimStateErr
	}
	state, ok := tf.PriorClaimStates[num]
	return state, ok, nil
}
