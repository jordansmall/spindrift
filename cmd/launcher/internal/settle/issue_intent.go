package settle

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// issueIntent is the decoded shape of a single SPINDRIFT_ISSUE_INTENT
// payload (issue #2018): title and body are used verbatim to file the
// issue; Labels and DedupTerms are the Box's own request, parsed but never
// passed through to PostIssue — see fileIssueIntents' provenanceLabel
// parameter.
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

// filedIntent is the per-payload outcome of filing one issue-intent: either
// a successful filing (URL set) or a failure (Failed=true, Body set to
// the intent's own body) a caller can degrade into inline comment text
// rather than silently dropping.
type filedIntent struct {
	Title  string
	URL    string
	Failed bool
	Body   string
}

// fileIssueIntents files every issue-intent payload in result.IssueIntents
// via its forge.HostPostedIssueFiler (issue #2018) — the host-mediated
// issue-filing relay channel, the fourth alongside branch→bundle, PR→intent
// line, and comment→comment line (ADR 0034). Unlike those three, this is
// 1-to-many: every verified payload files its own issue with the Launcher's
// own write-credentialed tracker, never the read-only Box's.
//
// A thin wrapper over fileIssueIntentsDetailed (issue #2592) that keeps its
// original URL-only return shape for its existing tests and as a
// narrower-surface convenience: it extracts the successes' URLs and drops
// any failures, exactly as before this routine grew a detailed sibling.
// gate.go's own call site (gate.go) ignores the return value entirely — it's
// a bare statement, not assigned to anything — so only the tests actually
// consume the []string shape.
//
// Returns the URLs of every issue successfully filed, in payload order.
func fileIssueIntents(it forge.IssueTracker, num string, result dispatch.Result, provenanceLabel string) []string {
	detailed := fileIssueIntentsDetailed(it, num, result, provenanceLabel, "")
	var urls []string
	for _, d := range detailed {
		if !d.Failed {
			urls = append(urls, d.URL)
		}
	}
	return urls
}

// fileIssueIntentsDetailed mirrors fileIssueIntents but returns one
// filedIntent per well-formed payload in payload order — success or
// failure — instead of only the successes' URLs, and optionally appends
// bodyBacklink to each filed issue's body (e.g. "Filed from research on
// #123") before calling PostIssue, when bodyBacklink is non-empty. A
// malformed or blank-title payload is skipped exactly as fileIssueIntents
// skips it — it never had a title to file or degrade with.
//
// A package-level function rather than a *Settle method (issue #2590):
// ResearchSettle (research.go) is a distinct struct in this same package,
// holding its own forge.IssueTracker but no Config and no push-only
// forge.CodeForge, so it has no way to call a *Settle method without
// fabricating a fake *Settle. Taking it explicitly instead gives both
// Settle.Settle (via gate.go) and a future research-settle caller the same
// visible seam.
//
// Both the destination repo and the applied labels are derived host-side:
// the repo is implicit in which tracker instance it already is (PostIssue
// takes no repo argument for a payload to redirect), and the labels are
// always the caller-supplied provenanceLabel, never the payload's own Labels
// field (issue #1949's do-not-trust-the-agent-target invariant, extended
// from destination repo to labels: a read-only Box holds no write token and
// cannot be trusted to pick which labels a filed issue carries any more than
// which repo it targets). provenanceLabel lets each caller record its own
// origin — e.g. the work-path settle gate always passes
// "agent-review-finding" (issue #2590 parameterized this so a future
// research-settle caller can pass a different label without this routine
// changing again). A malformed payload, or a tracker that doesn't implement
// HostPostedIssueFiler, is skipped/no-op rather than failing the caller —
// this is a best-effort side channel, not part of the run's own landing
// decision.
func fileIssueIntentsDetailed(it forge.IssueTracker, num string, result dispatch.Result, provenanceLabel, bodyBacklink string) []filedIntent {
	if !result.IssueIntentsFound {
		return nil
	}
	filer, ok := it.(forge.HostPostedIssueFiler)
	if !ok {
		return nil
	}
	var out []filedIntent
	for _, raw := range result.IssueIntents {
		in, ok := parseIssueIntent(raw)
		if !ok {
			fmt.Fprintf(os.Stderr, "    ?? #%s: skipping malformed issue-intent payload\n", num)
			continue
		}
		body := in.Body
		if bodyBacklink != "" {
			body = in.Body + "\n\n" + bodyBacklink
		}
		url, err := filer.PostIssue(in.Title, body, []string{provenanceLabel})
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: issue-intent file failed: %v\n", num, err)
			out = append(out, filedIntent{Title: in.Title, Failed: true, Body: in.Body})
			continue
		}
		out = append(out, filedIntent{Title: in.Title, URL: url})
	}
	return out
}
