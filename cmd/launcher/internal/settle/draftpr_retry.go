package settle

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// retryingDraftPR wraps dpc so every host-side draft PR create shares one
// retry-with-backoff seam. A nil dpc stays nil, so callers' nil checks keep
// meaning "no creator". A non-nil dpc comes with a non-nil prs: every real
// DraftPRCreator adapter is also a PRForge (see forge.DraftPRCreator).
func (s *Settle) retryingDraftPR(num string, gen uint64, dpc forge.DraftPRCreator, prs forge.PRForge) forge.DraftPRCreator {
	if dpc == nil {
		return nil
	}
	return &retryingDraftPRCreator{
		backoff:    s.transientBackoff(),
		maxRetries: s.cfg.Policy.Max,
		stopped:    func() bool { return s.terminated(num, gen) },
		num:        num,
		inner:      dpc,
		prs:        prs,
	}
}

type retryingDraftPRCreator struct {
	backoff    retry.LinearBackoff
	maxRetries int
	stopped    func() bool
	num        string // log lines only
	inner      forge.DraftPRCreator
	prs        forge.PRForge
}

// CreateDraftPR retries every failure whatever its class. The create POST is
// not idempotent and a gateway timeout can fire after the forge made the PR
// (#4994's 504), so before each retry the branch is looked up and an
// open PR there is adopted rather than duplicated; the adapters' own
// adopt-on-already-exists stays the backstop when the lookup itself fails. An
// operator stop during a backoff returns errAbandoned wrapping the last error.
func (r *retryingDraftPRCreator) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	for attempt := 1; ; attempt++ {
		url, created, err := r.inner.CreateDraftPR(title, body, base, head)
		if err == nil || attempt > r.maxRetries {
			return url, created, err
		}
		fmt.Printf("    #%s  landing=%s  status=draft-pr-retry  attempt=%d/%d  !! %v\n",
			r.num, head, attempt, r.maxRetries, err)
		if r.backoff.DoUnless(attempt, r.stopped) {
			return "", false, fmt.Errorf("%w: %w", errAbandoned, err)
		}
		pr, ok, lerr := r.prs.OpenPRForBranch(head)
		if lerr != nil {
			fmt.Printf("    #%s  landing=%s  status=draft-pr-lookup-failed  !! %v\n", r.num, head, lerr)
		} else if ok {
			fmt.Printf("    #%s  landing=%s  status=draft-pr-adopted  %s\n", r.num, head, pr.URL)
			return pr.URL, false, nil
		}
	}
}
