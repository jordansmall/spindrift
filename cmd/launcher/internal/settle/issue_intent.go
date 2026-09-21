package settle

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// issueIntent is the decoded shape of one SPINDRIFT_ISSUE_INTENT payload
// (issue #2018). Labels and DedupTerms are the Box's own request, parsed but
// never passed to PostIssue; the caller's provenanceLabel picks the labels.
type issueIntent struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Labels     []string `json:"labels"`
	DedupTerms []string `json:"dedupTerms"`
	// Type is an optional finding-type token (issue #2594 / ADR 0041), never
	// used directly as a label; see ensureTypeLabel.
	Type string `json:"type"`
}

// ensureTypeLabel ensure-creates typ's mapped label and returns the label to
// add to the filed issue, or "" when typ is empty, unrecognized, or the create
// failed. gh label create has no --force and rejects an existing name, so a
// failed create is re-checked against a fresh ListLabels before giving up. The
// mapping is the closed host-side doctor.FindingTypeLabels (#2594, ADR 0041).
func ensureTypeLabel(it forge.IssueTracker, typ string, existing []string) string {
	meta, ok := doctor.FindingTypeLabels[typ]
	if !ok {
		return ""
	}
	if slices.Contains(existing, typ) {
		return typ
	}
	if err := it.CreateLabel(typ, meta.Description, meta.Color); err != nil {
		if reListed, rerr := it.ListLabels(); rerr == nil && slices.Contains(reListed, typ) {
			return typ
		}
		fmt.Fprintf(os.Stderr, "    ?? create label %q failed: %v\n", typ, err)
		return ""
	}
	return typ
}

// parseIssueIntent decodes one raw SPINDRIFT_ISSUE_INTENT payload, already
// base64-decoded and nonce-verified by outcome.AllIssueIntentLinesInLog.
// Returns ok=false for malformed JSON or a blank title.
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

// filedIntent is the outcome of filing one issue-intent: a URL on success, or
// Failed with the intent's own body so a caller can degrade it into inline
// comment text rather than dropping it silently.
type filedIntent struct {
	Title  string
	URL    string
	Failed bool
	Body   string
}

// fileIssueIntentsDetailed returns one filedIntent per well-formed payload in
// payload order, success or failure, and appends a non-empty bodyBacklink to
// each body before filing. Labels are host-derived, the provenanceLabel plus
// any ensureTypeLabel match, never the payload's own (issue #1949). A package
// function, not a *Settle method, so ResearchSettle can call it (issue #2590).
func fileIssueIntentsDetailed(it forge.IssueTracker, num string, result dispatch.Result, provenanceLabel, bodyBacklink string) []filedIntent {
	if !result.IssueIntentsFound {
		return nil
	}
	// Filing is a best-effort side channel, never part of the run's landing
	// decision, so a tracker that cannot file is a no-op rather than an error.
	filer, ok := it.(forge.HostPostedIssueFiler)
	if !ok {
		return nil
	}
	// Hoisted out of the loop: N findings would otherwise cost N ListLabels
	// round trips. A listErr is non-fatal: ensureTypeLabel falls back to its
	// own create-then-recheck path.
	existingLabels, listErr := it.ListLabels()
	if listErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: list labels failed: %v\n", num, listErr)
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
		labels := []string{provenanceLabel}
		if l := ensureTypeLabel(it, in.Type, existingLabels); l != "" {
			labels = append(labels, l)
		}
		url, err := filer.PostIssue(in.Title, body, labels)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: issue-intent file failed: %v\n", num, err)
			out = append(out, filedIntent{Title: in.Title, Failed: true, Body: in.Body})
			continue
		}
		out = append(out, filedIntent{Title: in.Title, URL: url})
	}
	return out
}

// filedTally is one run's issue-filing volume. A failed filing is counted
// apart from a successful one so a run that tried and failed does not read as
// a quiet run (issue #3608).
type filedTally struct {
	ok     int
	failed int
}

// tallyFiled counts filed by filedIntent.Failed. A nil/empty slice yields the
// zero tally, not an error.
func tallyFiled(filed []filedIntent) filedTally {
	var t filedTally
	for _, f := range filed {
		if f.Failed {
			t.failed++
		} else {
			t.ok++
		}
	}
	return t
}

// String renders as comma-joined name:count pairs (issue #3608) rather than
// a fixed "ok/failed" shape, so a later skipped:<N> count slots in as one
// more pair instead of a breaking format change.
func (t filedTally) String() string {
	return fmt.Sprintf("ok:%d,failed:%d", t.ok, t.failed)
}

// reportFiled prints the filing tally for issue num, always — even a zero
// tally (filed=ok:0,failed:0) — so "reached filing and filed nothing" reads
// differently in the transcript than "never reached filing" (no line at
// all).
func reportFiled(num string, filed []filedIntent) {
	fmt.Printf("    #%s  filed=%s\n", num, tallyFiled(filed))
}
