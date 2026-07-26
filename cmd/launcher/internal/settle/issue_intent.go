package settle

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// issueIntentLabels is the fixed label set the Launcher applies to every
// issue a SPINDRIFT_ISSUE_INTENT line files, regardless of what the
// payload's own Labels field requests (issue #1949's
// do-not-trust-the-agent-target invariant, extended from destination repo to
// labels): a read-only Box holds no write token and cannot be trusted to
// pick which labels a filed issue carries any more than which repo it
// targets.
var issueIntentLabels = []string{"agent-review-finding"}

// issueIntent is the decoded shape of a single SPINDRIFT_ISSUE_INTENT
// payload (issue #2018): title and body are used verbatim to file the
// issue; Labels and DedupTerms are the Box's own request, parsed but never
// passed through to PostIssue — see issueIntentLabels.
type issueIntent struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Labels     []string `json:"labels"`
	DedupTerms []string `json:"dedupTerms"`
}

// parseIssueIntent decodes a single raw SPINDRIFT_ISSUE_INTENT payload (the
// JSON text outcome.AllIssueIntentLinesInLog already base64-decoded and
// nonce-verified). Returns ok=false for malformed JSON or a blank title —
// the shape fileIssueIntents must skip rather than file an empty-titled
// issue.
func parseIssueIntent(raw string) (issueIntent, bool) {
	var in issueIntent
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return issueIntent{}, false
	}
	if strings.TrimSpace(in.Title) == "" {
		return issueIntent{}, false
	}
	return in, true
}

// fileIssueIntents files every issue-intent payload in result.IssueIntents
// via the tracker's forge.HostPostedIssueFiler (issue #2018) — the
// host-mediated issue-filing relay channel, the fourth alongside
// branch→bundle, PR→intent line, and comment→comment line (ADR 0034).
// Unlike those three, this is 1-to-many: every verified payload files its
// own issue with the Launcher's own write-credentialed tracker, never the
// read-only Box's.
//
// Both the destination repo and the applied labels are derived host-side:
// the repo is implicit in which tracker instance s.it already is (PostIssue
// takes no repo argument for a payload to redirect), and the labels are
// always issueIntentLabels, never the payload's own Labels field (issue
// #1949). A malformed payload, or a tracker that doesn't implement
// HostPostedIssueFiler, is skipped/no-op rather than failing the caller —
// this is a best-effort side channel, not part of the run's own landing
// decision.
//
// Returns the URLs of every issue successfully filed, in payload order.
func (s *Settle) fileIssueIntents(num string, result dispatch.Result) []string {
	if !result.IssueIntentsFound {
		return nil
	}
	filer, ok := s.it.(forge.HostPostedIssueFiler)
	if !ok {
		return nil
	}
	var urls []string
	for _, raw := range result.IssueIntents {
		in, ok := parseIssueIntent(raw)
		if !ok {
			fmt.Fprintf(os.Stderr, "    ?? #%s: skipping malformed issue-intent payload\n", num)
			continue
		}
		url, err := filer.PostIssue(in.Title, in.Body, issueIntentLabels)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: issue-intent file failed: %v\n", num, err)
			continue
		}
		urls = append(urls, url)
	}
	return urls
}
