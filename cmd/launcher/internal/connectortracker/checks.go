package connectortracker

import (
	"fmt"
	"slices"
	"time"

	"spindrift.dev/launcher/internal/connectorcall"
	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/forge"
)

// The result shapes the check bodies read and repair, named where a method's
// answer is not already one of the wire types.
type (
	wireIssues struct {
		Issues []wireIssue `json:"issues"`
	}
	wireComments struct {
		Comments []wireComment `json:"comments"`
	}
	// wireIDs carries the method that asked, which the idList refusal names;
	// the decode leaves the unexported field alone.
	wireIDs struct {
		IDs    []string `json:"ids"`
		method string
	}
	wireProbeStatus struct {
		Status int `json:"status"`
	}
	wireCount struct {
		Count int `json:"count"`
	}
	wireInterval struct {
		Seconds int `json:"seconds"`
	}
	wireLabels struct {
		Labels []string `json:"labels"`
	}
	wireFiled struct {
		URL string `json:"url"`
	}
)

// probeStatusError is a refusal of the probe's HTTP status, which Probe returns
// as is where a failed call is mapped to forge.ErrRepoNotFound.
type probeStatusError struct{ error }

func (e probeStatusError) Unwrap() error { return e.error }

// trackerChecks is the tracker axis's whole registry: every call of t runs
// through it. The bodies read t when they run, so t's grammar and lifecycle
// need not be set when the registry is built.
func trackerChecks(t *reads) connectorcall.Checks {
	return connectorcall.Checks{
		connectorprotocol.CheckDecode:      connectorcall.Decode,
		connectorprotocol.CheckIssueShape:  t.issueShapeBody,
		connectorprotocol.CheckIssueAnswer: connectorcall.Typed(t.issueAnswerBody),
		connectorprotocol.CheckCommentShape: connectorcall.Typed(func(_ numberedParams, r *wireComments) error {
			for i, c := range r.Comments {
				if !cleanText(c.Author) {
					return fmt.Errorf("%w: comment %d has an empty or control-character author", ErrAnswer, i)
				}
				if _, err := time.Parse(time.RFC3339, c.CreatedAt); err != nil {
					return fmt.Errorf("%w: comment %d has an unparseable createdAt %q", ErrAnswer, i, c.CreatedAt)
				}
				r.Comments[i].Association = forge.KnownAssociation(c.Association)
			}
			return nil
		}),
		connectorprotocol.CheckIdList: connectorcall.Typed(func(_ numberedParams, r *wireIDs) error {
			var ids []string
			seen := map[string]bool{}
			for _, id := range r.IDs {
				if !t.ids.valid(id) {
					return fmt.Errorf("%w: %s id %q is not %s", ErrAnswer, r.method, id, t.ids.name)
				}
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
			r.IDs = ids
			return nil
		}),
		connectorprotocol.CheckProbeStatus: connectorcall.Typed(func(p targetParams, r *wireProbeStatus) error {
			switch {
			case r.Status >= 200 && r.Status < 300:
				return nil
			case r.Status == 401 || r.Status == 403:
				return probeStatusError{fmt.Errorf("%w: probe of %s answered %d", forge.ErrAuthFailure, p.Target.Repo, r.Status)}
			default:
				return probeStatusError{fmt.Errorf("%w: probe of %s answered %d", forge.ErrRepoNotFound, p.Target.Repo, r.Status)}
			}
		}),
		connectorprotocol.CheckCreateMetaIssueType: connectorcall.Typed(func(p createMetaParams, r *wireCreateMeta) error {
			return forge.IssueTypeNotOffered(p.Target.Repo, p.IssueType, r.IssueTypes)
		}),
		connectorprotocol.CheckCountClamp: connectorcall.Typed(func(_ countParams, r *wireCount) error {
			if r.Count < 0 {
				return fmt.Errorf("%w: %s answered %d", ErrAnswer, CapCount, r.Count)
			}
			r.Count = min(r.Count, forge.ResultPageLimit)
			return nil
		}),
		connectorprotocol.CheckCountInterval: connectorcall.Typed(func(_ struct{}, r *wireInterval) error {
			if r.Seconds < 1 || r.Seconds > int(maxCountInterval/time.Second) {
				return fmt.Errorf("%w: %s answered %d seconds, want 1 to %d", ErrAnswer, CapCountInterval, r.Seconds, int(maxCountInterval/time.Second))
			}
			return nil
		}),
		// The cross-page state (repeat cursor, page of only seen issues, the
		// page and byte bounds) lives in walkLabeled; a body sees one page.
		connectorprotocol.CheckLabeledPaging: connectorcall.Typed(func(_ labeledParams, a *labeledAnswer) error {
			for _, w := range a.Issues {
				if err := w.check(t.ids, singleRead); err != nil {
					return err
				}
			}
			return nil
		}),
		connectorprotocol.CheckTransitionOutcome: connectorcall.Typed(func(_ transitionParams, r *transitionResult) error {
			switch r.Outcome {
			case transitionMoved, transitionUnavailable, transitionUnmapped:
				return nil
			}
			return fmt.Errorf("%w: tracker.transition answered outcome %q", ErrAnswer, r.Outcome)
		}),
		connectorprotocol.CheckLabelNames: connectorcall.Typed(func(_ targetParams, r *wireLabels) error {
			return checkLabels(r.Labels)
		}),
		connectorprotocol.CheckFiledURL: connectorcall.Typed(func(_ fileIssueParams, r *wireFiled) error {
			if reason := t.unusableFiledURL(r.URL); reason != "" {
				// Filing is not idempotent: the Connector answered success, so the
				// error says "filed" and the caller must not refile.
				return fmt.Errorf("%w: %w: tracker.fileIssue returned url %q: %s", ErrAnswer, forge.ErrFiledUnusableURL, r.URL, reason)
			}
			return nil
		}),
	}
}

// issueShapeBody checks the issues tracker.listIssues or tracker.searchIssues
// answered. A search hit outside the state or label asked for is dropped: the
// guest is asked to filter but is not trusted to, and a stray hit would count
// as a suppressing match.
func (t *reads) issueShapeBody(params, result any) error {
	r, ok := result.(*wireIssues)
	if !ok {
		return fmt.Errorf("connectortracker: issueShape takes an issue list, got %T: %w", result, connectorcall.ErrCoreBug)
	}
	switch p := params.(type) {
	case listIssuesParams:
		for _, w := range r.Issues {
			if err := w.check(t.ids, listRead); err != nil {
				return err
			}
		}
	case searchIssuesParams:
		// The guest is asked for limit but is not trusted to honour it.
		r.Issues = r.Issues[:min(len(r.Issues), max(p.Limit, 0))]
		var kept []wireIssue
		for _, w := range r.Issues {
			if err := w.check(t.ids, singleRead); err != nil {
				return err
			}
			if w.State == p.State && (p.Label == "" || slices.Contains(w.Labels, p.Label)) {
				kept = append(kept, w)
			}
		}
		r.Issues = kept
	default:
		return fmt.Errorf("connectortracker: issueShape takes list or search params, got %T: %w", params, connectorcall.ErrCoreBug)
	}
	return nil
}

func (t *reads) issueAnswerBody(p numberedParams, w *wireIssue) error {
	if err := w.check(t.ids, singleRead); err != nil {
		return err
	}
	if w.Number != p.Number {
		return fmt.Errorf("%w: asked for issue %s, got %s", ErrAnswer, p.Number, w.Number)
	}
	return nil
}
