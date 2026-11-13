package waves

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"spindrift.dev/launcher/internal/forge"
)

// LabelClaimer is the Claimer that moves an issue from Dispatchable to
// InProgress through the tracker's labels (issue #2938). A refused claim
// (status-lifecycle Jira, issue #6378) is commented on the issue once per
// unchanged refusal; the memo is per process, so a fresh one-shot or
// daemon-spawned run posts once per run.
type LabelClaimer struct {
	it              forge.IssueTracker
	label           string
	inProgressLabel string

	// mu guards refused: wave mode calls Claim from concurrent goroutines.
	mu sync.Mutex
	// refused maps an issue number to the refusal text last posted for it.
	refused map[string]string
}

// NewLabelClaimer constructs a LabelClaimer bound to the given tracker.
func NewLabelClaimer(it forge.IssueTracker, label, inProgressLabel string) *LabelClaimer {
	return &LabelClaimer{it: it, label: label, inProgressLabel: inProgressLabel, refused: map[string]string{}}
}

// Claim moves the issue from Dispatchable to InProgress. The caller must
// treat a non-nil error as a skip, not a fatal error, because a stale
// listing racing a concurrent claimant fails here routinely.
func (c *LabelClaimer) Claim(num string) error {
	if err := c.it.TransitionState(num, forge.Dispatchable, forge.InProgress); err != nil {
		// ErrAlreadyClaimed means another claimant already won this issue, not
		// a failure, so the "??" warning line would be noise (#3887).
		if !errors.Is(err, forge.ErrAlreadyClaimed) {
			fmt.Fprintf(os.Stderr, "    ?? #%s: could not claim (%s -> %s): %v\n", num, c.label, c.inProgressLabel, err)
		}
		c.commentRefusalOnce(num, err)
		return err
	}
	c.mu.Lock()
	delete(c.refused, num)
	c.mu.Unlock()
	return nil
}

// commentRefusalOnce posts err on the issue when it is a refusal differing from
// the one last posted for num. The memo entry is reserved before the post so
// concurrent claims of one issue comment once without the lock spanning the
// network call, and released if the post fails so a later claim retries.
func (c *LabelClaimer) commentRefusalOnce(num string, err error) {
	var refusal *forge.TransitionRefusedError
	if !errors.As(err, &refusal) {
		return
	}
	text := refusal.Error()
	c.mu.Lock()
	if c.refused[num] == text {
		c.mu.Unlock()
		return
	}
	c.refused[num] = text
	c.mu.Unlock()
	if _, cerr := forge.CommentRefusal(c.it, num, err); cerr != nil {
		c.mu.Lock()
		if c.refused[num] == text {
			delete(c.refused, num)
		}
		c.mu.Unlock()
	}
}

// claimSkipLine renders the line a caller prints when Claim fails and it
// moves on without dispatching. ErrAlreadyClaimed means someone else already
// has the issue, not a failure, so it gets a quieter, distinct message than
// the generic claim-failed line (#3887).
func claimSkipLine(num string, err error) string {
	if errors.Is(err, forge.ErrAlreadyClaimed) {
		return fmt.Sprintf("    ~~ #%s skipped: already claimed\n", num)
	}
	return fmt.Sprintf("    ~~ #%s claim failed; skipping (%v)\n", num, err)
}
