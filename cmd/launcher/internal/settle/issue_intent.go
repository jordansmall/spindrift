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
// (issue #2018). Labels is the Box's own request, parsed but never passed to
// PostIssue; the caller's provenanceLabel picks the labels. DedupTerms is
// also the Box's own request, but unlike Labels it does reach the filed
// issue: it keys the host-side dedup check (dedup.go, issue #3609) and is
// written into the filed body's hidden marker so a later run can recover it.
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

// filedIntent is the outcome of filing one issue-intent: a URL on success,
// Failed with the intent's own body so a caller can degrade it into inline
// comment text rather than dropping it silently, or Skipped when a dedup key
// already matched an open finding issue or an earlier intent this same run
// (issue #3609) -- never posted, so it has no URL and no Body to render.
type filedIntent struct {
	Title   string
	URL     string
	Failed  bool
	Body    string
	Skipped bool
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
	// Backlog dedup keys (issue #3609), built at most once per pass for the
	// same reason ListLabels is hoisted -- N findings would otherwise cost N
	// ListOpenIssues round trips -- but lazily, since a payload whose intents
	// carry no keys can never match anything the list returns.
	// backlogDedupIndex yields a non-nil map even on failure, so the nil check
	// below is a true once-only memo. Grown in place as this run files its own
	// intents, so a duplicate pair within one payload dedups too, not just
	// against the open backlog.
	var dedupIndex map[string]string
	var out []filedIntent
	for _, raw := range result.IssueIntents {
		in, ok := parseIssueIntent(raw)
		if !ok {
			fmt.Fprintf(os.Stderr, "    ?? #%s: skipping malformed issue-intent payload\n", num)
			continue
		}
		keys, dropped := splitDedupTerms(in.DedupTerms)
		if len(dropped) > 0 {
			fmt.Fprintf(os.Stderr, "    ?? #%s: dropped unusable dedup term(s) %q\n", num, dropped)
		}
		if len(keys) == 0 {
			fmt.Fprintf(os.Stderr, "    ?? #%s: no usable dedup term for %q: filing without dedup\n", num, in.Title)
		} else if dedupIndex == nil {
			dedupIndex = backlogDedupIndex(it, num)
		}
		if ref, dup := matchDedup(dedupIndex, keys); dup {
			fmt.Printf("    #%s  skipped duplicate issue-intent: %q (already tracked: %s)\n", num, in.Title, ref)
			out = append(out, filedIntent{Title: in.Title, Skipped: true})
			continue
		}
		body := in.Body
		if bodyBacklink != "" {
			body = in.Body + "\n\n" + bodyBacklink
		}
		// The launcher's own marker goes last unconditionally, even carrying
		// no terms: parseDedupMarker is last-wins, so omitting it would let a
		// marker line quoted in body prose speak for this issue's key set.
		marker := buildDedupMarker(in.DedupTerms)
		if marker == "" {
			marker = dedupMarkerPrefix + dedupMarkerSuffix
		}
		body = body + "\n\n" + marker
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
		// Added only on a successful filing (not on failure): a transient
		// PostIssue error must not suppress a retry of the same intent within
		// this run.
		ref := fmt.Sprintf("this run's %q", in.Title)
		for k := range keys {
			dedupIndex[k] = ref
		}
		out = append(out, filedIntent{Title: in.Title, URL: url})
	}
	return out
}

// filedTally is one run's issue-filing volume. A failed filing is counted
// apart from a successful one so a run that tried and failed does not read as
// a quiet run (issue #3608). skipped counts a dedup match (issue #3609)
// separately from both: neither a success nor a failure, so folding it into
// either would misreport which of the three actually happened.
type filedTally struct {
	ok      int
	failed  int
	skipped int
}

// tallyFiled counts filed by filedIntent.Failed/Skipped. A nil/empty slice
// yields the zero tally, not an error.
func tallyFiled(filed []filedIntent) filedTally {
	var t filedTally
	for _, f := range filed {
		switch {
		case f.Skipped:
			t.skipped++
		case f.Failed:
			t.failed++
		default:
			t.ok++
		}
	}
	return t
}

// String renders as comma-joined name:count pairs (issue #3608) rather than
// a fixed "ok/failed" shape, so skipped:<N> (issue #3609) slots in as one
// more pair instead of a breaking format change.
func (t filedTally) String() string {
	return fmt.Sprintf("ok:%d,failed:%d,skipped:%d", t.ok, t.failed, t.skipped)
}

// reportFiled prints the filing tally for issue num, always — even a zero
// tally (filed=ok:0,failed:0,skipped:0) — so "reached filing and filed
// nothing" reads differently in the transcript than "never reached filing"
// (no line at all).
func reportFiled(num string, filed []filedIntent) {
	fmt.Printf("    #%s  filed=%s\n", num, tallyFiled(filed))
}
