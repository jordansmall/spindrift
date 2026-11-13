package forge

import (
	"fmt"
	"net/http"
)

// RecordLandingCall records a single RecordLanding invocation.
type RecordLandingCall struct {
	Num, Landing string
}

// RecordLanding implements the optional LandingRecorder interface (ADR 0029).
func (tf *IssueTrackerFake) RecordLanding(num, landing string) error {
	return tf.call(http.MethodPost, pathComment, nil, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		tf.RecordLandingCalls = append(tf.RecordLandingCalls, RecordLandingCall{num, landing})
		return nil, tf.RecordLandingErr
	})
}

// RecordLandingPassCall records a single RecordLandingPass invocation.
type RecordLandingPassCall struct {
	Num  string
	Pass int
	Kind string
}

// RecordLandingPass implements the optional LandingPassRecorder interface (issue #2983).
func (tf *IssueTrackerFake) RecordLandingPass(num string, pass int, kind string) error {
	return tf.call(http.MethodPost, pathComment, nil, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		tf.RecordLandingPassCalls = append(tf.RecordLandingPassCalls, RecordLandingPassCall{num, pass, kind})
		return nil, tf.RecordLandingPassErr
	})
}

// CloseIssue implements the optional IssueCloser interface (ADR 0029).
func (tf *IssueTrackerFake) CloseIssue(num string) error {
	return tf.call(http.MethodPost, pathClose, nil, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		tf.CloseIssueCalls = append(tf.CloseIssueCalls, num)
		if tf.CloseIssueErr != nil {
			return nil, tf.CloseIssueErr
		}
		return nil, tf.closeLocked(num)
	})
}

// CloseMergedIssue implements the optional MergeCloser interface (issue #1892).
// It records into CloseMergedIssueCalls, not CloseIssueCalls.
func (tf *IssueTrackerFake) CloseMergedIssue(num string) error {
	return tf.call(http.MethodPost, pathClose, nil, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		tf.CloseMergedIssueCalls = append(tf.CloseMergedIssueCalls, num)
		if tf.CloseMergedIssueErr != nil {
			return nil, tf.CloseMergedIssueErr
		}
		return nil, tf.closeLocked(num)
	})
}

// closeLocked marks num closed; the caller holds tf.mu.
func (tf *IssueTrackerFake) closeLocked(num string) error {
	iss, ok := tf.issues[num]
	if !ok {
		return fmt.Errorf("issue %s not found", num)
	}
	iss.State = IssueClosed
	tf.issues[num] = iss
	return nil
}

// FlagAbandoned implements the optional AbandonedFlagger interface (ADR 0029).
// Real adapters set a frontmatter field, not a label; the Fake borrows the
// editLabels Route to model that edit as a tracker write.
func (tf *IssueTrackerFake) FlagAbandoned(num string) error {
	return tf.call(http.MethodPost, pathEditLabels, nil, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		tf.FlagAbandonedCalls = append(tf.FlagAbandonedCalls, num)
		if tf.FlagAbandonedErr != nil {
			return nil, tf.FlagAbandonedErr
		}
		iss, ok := tf.issues[num]
		if !ok {
			return nil, fmt.Errorf("issue %s not found", num)
		}
		iss.Abandoned = true
		tf.issues[num] = iss
		return nil, nil
	})
}

// priorClaimAnswer is PriorClaimState's answer over pathPriorClaim.
type priorClaimAnswer struct {
	State DispatchState
	OK    bool
}

// PriorClaimState implements the optional PriorClaimStateReader interface.
func (tf *IssueTrackerFake) PriorClaimState(num string) (DispatchState, bool, error) {
	var out priorClaimAnswer
	err := tf.read(pathPriorClaim, &out, func() (any, error) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		if tf.PriorClaimStateErr != nil {
			return nil, tf.PriorClaimStateErr
		}
		state, ok := tf.PriorClaimStates[num]
		return priorClaimAnswer{state, ok}, nil
	})
	if err != nil {
		return Untriaged, false, err
	}
	return out.State, out.OK, nil
}
