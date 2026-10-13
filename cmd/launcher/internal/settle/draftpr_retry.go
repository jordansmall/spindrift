package settle

import (
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/forge"
)

// retryingDraftPR wraps dpc so every host-side draft PR create shares one
// retry-with-backoff seam. A nil dpc stays nil, so callers' nil checks keep
// meaning "no creator". A non-nil dpc comes with a non-nil prs: every real
// DraftPRCreator adapter is also a PRForge (see forge.DraftPRCreator).
func (s *Settle) retryingDraftPR(num string, gen uint64, dpc forge.DraftPRCreator, prs forge.PRForge, onAdoptOwn func()) forge.DraftPRCreator {
	if dpc == nil {
		return nil
	}
	return &retryingDraftPRCreator{
		retryLoop:  s.newRetryLoop(num, gen),
		inner:      dpc,
		prs:        prs,
		onAdoptOwn: onAdoptOwn,
	}
}

type retryingDraftPRCreator struct {
	retryLoop
	inner forge.DraftPRCreator
	prs   forge.PRForge
	// onAdoptOwn fires when a result with created=false follows a failed attempt of
	// this wrapper, i.e. the PR is the host's own earlier attempt (issue #5173).
	// Nil-safe.
	onAdoptOwn func()
}

// CreateDraftPR retries every failure whatever its class. The create POST is
// not idempotent and a gateway timeout can fire after the forge made the PR
// (#4994's 504), so after every failed attempt, the last included, the
// branch is looked up and an open PR there is adopted rather than duplicated
// or parked. When a non-final lookup itself fails, the next attempt's
// adapter-side adopt-on-already-exists is the backstop; after a failed final
// lookup only a later `spindrift recover` is. An operator stop during a
// backoff returns errAbandoned wrapping the last error, with no lookup.
func (r *retryingDraftPRCreator) CreateDraftPR(title, body, base, head string) (string, bool, error) {
	var url string
	var created bool
	attempts := 0
	err := r.run("draft-pr-retry", "draft-pr-final-lookup", head,
		func() error {
			attempts++
			var err error
			url, created, err = r.inner.CreateDraftPR(title, body, base, head)
			if err == nil && !created && attempts > 1 {
				r.fireOnAdoptOwn()
			}
			return err
		},
		nil, // no final error: every failure retries
		func() bool {
			pr, ok, lerr := r.prs.OpenPRForBranch(head)
			if lerr != nil {
				fmt.Printf("    #%s  landing=%s  status=draft-pr-lookup-failed  !! %v\n", r.num, head, lerr)
			} else if ok {
				fmt.Printf("    #%s  landing=%s  status=draft-pr-adopted  %s\n", r.num, head, pr.URL)
				r.fireOnAdoptOwn()
				url, created = pr.URL, false
				return true
			}
			return false
		})
	if errors.Is(err, errAbandoned) {
		return "", false, err
	}
	return url, created, err
}

func (r *retryingDraftPRCreator) fireOnAdoptOwn() {
	if r.onAdoptOwn != nil {
		r.onAdoptOwn()
	}
}
