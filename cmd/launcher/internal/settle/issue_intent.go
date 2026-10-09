package settle

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/rivo/uniseg"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/signalwire"
)

// issueIntent is one decoded SPINDRIFT_ISSUE_INTENT payload (issue #2018):
// the wire shape plus Labels, the Box's own request, parsed but never passed
// to PostIssue -- the caller's provenanceLabel picks the labels (issue #1949).
type issueIntent struct {
	signalwire.IssueIntent
	Labels []string `json:"labels"`
}

// ensureLabel creates name from meta unless known already lists it, and
// reports whether the label now exists. gh label create has no --force and
// rejects an existing name, so a failed create is re-checked against a fresh
// ListLabels before giving up. A label found or created is recorded in known,
// so later intents in one filing pass skip the create.
func ensureLabel(it forge.IssueTracker, name string, meta doctor.LabelMeta, known *[]string) bool {
	if slices.Contains(*known, name) {
		return true
	}
	if err := it.CreateLabel(name, meta.Description, meta.Color); err != nil {
		if reListed, rerr := it.ListLabels(); rerr == nil && slices.Contains(reListed, name) {
			*known = append(*known, name)
			return true
		}
		fmt.Fprintf(os.Stderr, "    ?? create label %q failed: %v\n", name, err)
		return false
	}
	*known = append(*known, name)
	return true
}

// ensureTypeLabel ensure-creates typ's mapped label and returns the label to
// add to the filed issue, or "" when typ is empty, unrecognized, or the create
// failed. The mapping is the closed host-side doctor.FindingTypeLabels (#2594,
// ADR 0041).
func ensureTypeLabel(it forge.IssueTracker, typ string, known *[]string) string {
	meta, ok := doctor.FindingTypeLabels[typ]
	if !ok || !ensureLabel(it, typ, meta, known) {
		return ""
	}
	return typ
}

// ensureFilingLabel ensure-creates a label a host-filed issue carries (issue
// #4400): GitHub's `gh issue create --label` fails outright on a missing
// label, which would drop every finding. A label with no known metadata passes
// through untouched. It never drops the label: the provenance label must stay
// on the issue so closed-provenance dedup suppression holds, so a failed create
// surfaces as PostIssue's own failure.
func ensureFilingLabel(it forge.IssueTracker, name string, known *[]string) {
	meta, ok := doctor.LabelMetaFor(name)
	if ok {
		ensureLabel(it, name, meta, known)
	}
}

// maxConcurrenceLen bounds Concurrence in grapheme clusters, the
// visible-length unit, so truncation never splits a user-perceived
// character (an emoji ZWJ sequence, a base rune and its combining marks).
// maxConcurrenceBytes is the cap that actually bounds size: a single
// grapheme cluster can carry unboundedly many combining marks, so a
// cluster count alone doesn't stop one finding's note from ballooning
// arbitrarily -- the reason signalwire.ValidClass bounds Class applies here
// too, this text is Box-supplied prose interpolated into a host-authored
// note. 1024 keeps the note a small slice of GitHub's 65,536-character
// issue-body cap.
const (
	maxConcurrenceLen   = 200
	maxConcurrenceBytes = 1024
)

// sanitizeConcurrence truncates s to at most maxConcurrenceLen grapheme
// clusters and at most maxConcurrenceBytes bytes, cutting only at a
// grapheme-cluster boundary, and neutralizes backticks. Only an oversized
// first cluster yields "", which fails closed: eligible rejects an empty
// concurrence. An oversized later cluster just ends the result at the
// preceding boundary, an ordinary truncated fragment. internal/butler's
// promotionNote quotes the result inside a markdown code span; with every
// backtick gone the Box text can't close that span early, so the span keeps
// swallowing whatever formatting -- @mentions, links, emphasis, inline HTML
// -- the Box text tries to spoof.
func sanitizeConcurrence(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	lastEnd := 0
	gr := uniseg.NewGraphemes(s)
	for n := 0; gr.Next(); n++ {
		_, to := gr.Positions()
		if n == maxConcurrenceLen || to > maxConcurrenceBytes {
			break
		}
		lastEnd = to
	}
	return s[:lastEnd]
}

// parseIssueIntent decodes one raw SPINDRIFT_ISSUE_INTENT payload, already
// base64-decoded and nonce-verified by outcome.AllIssueIntentLinesInLog, and
// re-validates it defensively: the log carrier reaches settle unchecked, and
// a socket payload already passed Validate, plus the socket's own type
// requirement and its blank-dedup-term pruning. Any reject skips the whole
// intent, so an illegal Class is never silently dropped from one.
func parseIssueIntent(raw string) (issueIntent, *signalwire.Reject) {
	var in issueIntent
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return issueIntent{}, &signalwire.Reject{Status: "invalid_json", Reason: "malformed issue-intent payload"}
	}
	if rej := in.Validate(); rej != nil {
		return issueIntent{}, rej
	}
	in.Concurrence = sanitizeConcurrence(in.Concurrence)
	return in, nil
}

// filedIntent is the outcome of filing one issue-intent: a URL on success,
// Failed with the intent's own body so a caller can degrade it into inline
// comment text rather than dropping it silently, or Skipped when a dedup key
// already matched an open or closed finding issue, or an earlier intent this
// same run (issue #3609) -- never posted, so it has no URL and no Body to
// render.
type filedIntent struct {
	Title   string
	URL     string
	Failed  bool
	Body    string
	Skipped bool
	// DupRef is a comma-joined, display-only list of every reference
	// covering the intent -- each "#<number>" (an open or closed backlog
	// finding issue) or this run's "<title>" (an intent this run filed,
	// matched exactly or by overlapping line range), possibly mixed in one
	// value -- so a Skipped entry can name what it matched (issue #3811).
	// It is a string, not a typed reference set, because its one renderer,
	// buildSkippedIssuesSection, never branches on kind; split it only for
	// a renderer that must treat a backlog "#<number>" differently from a
	// this-run peer (issue #3831).
	DupRef string
}

// fileIssueIntentsDetailed returns one filedIntent per well-formed payload in
// payload order, success or failure, and appends a non-empty bodyBacklink to
// each body before filing. Labels are host-derived, the provenanceLabel plus
// any ensureTypeLabel match, never the payload's own (issue #1949). A package
// function, not a *Settle method, so ResearchSettle can call it (issue #2590).
func fileIssueIntentsDetailed(it forge.IssueTracker, num string, result dispatch.Result, provenanceLabel, bodyBacklink string) []filedIntent {
	return fileIssueIntentsDetailedFunc(it, num, result, provenanceLabel, func(issueIntent) (string, []string, func(string)) { return bodyBacklink, nil, nil })
}

// fileIssueIntentsDetailedFunc is fileIssueIntentsDetailed's per-intent sibling
// (issue #3875): decorate is computed from each intent rather than fixed
// once, so a caller like internal/butler's settle step can name that intent's
// own files in its backlink. It also returns extraLabels -- labels beyond the
// provenance and type labels this function already applies, e.g. the Butler
// Runner's own auto-promotion gate (issue #3880) adding "ready-for-agent" --
// appended after them. The third return, onFiled, is called with the filed
// issue's URL only after PostIssue actually succeeds -- never on a failed or
// skipped intent -- so a caller that spends shared state (the Runner's
// per-run promotion room) on deciding extraLabels can defer committing that
// spend, and record which URL it was spent on, until the filing it was for
// is real: a failed PostIssue must not burn the day's promotion room.
// fileIssueIntentsDetailed delegates to this with a constant closure,
// rather than the other way around, so callers with a fixed backlink keep the
// plain (it, num, result, provenanceLabel, bodyBacklink string) shape.
func fileIssueIntentsDetailedFunc(it forge.IssueTracker, num string, result dispatch.Result, provenanceLabel string, decorate func(issueIntent) (bodyBacklink string, extraLabels []string, onFiled func(url string))) []filedIntent {
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
	// round trips. A listErr is non-fatal: ensureLabel falls back to its own
	// create-then-recheck path.
	existingLabels, listErr := it.ListLabels()
	if listErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: list labels failed: %v\n", num, listErr)
	}
	// Backlog dedup keys (issue #3609; closed findings too as of #3873), built
	// at most once per pass for the same reason ListLabels is hoisted -- N
	// findings would otherwise cost N pairs of backlog list round trips -- but
	// lazily, since a payload whose intents carry no keys can never match
	// anything the list returns. backlogDedupIndex yields a non-nil map even
	// on failure, so the nil check below is a true once-only memo. Grown in
	// place as this run files its own intents, so a duplicate pair within one
	// payload dedups too, not just against the backlog. runKeys is that
	// within-run subset, kept apart so applyRunLineOverlap never matches a
	// line overlap against the backlog (issue #4108).
	var dedupIndex map[string]string
	runKeys := make(map[string]string)
	var out []filedIntent
	for _, raw := range result.IssueIntents {
		in, rej := parseIssueIntent(raw)
		if rej != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: skipping invalid issue-intent payload: %s\n", num, rej.Reason)
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
		applyRunLineOverlap(dedupIndex, runKeys, keys)
		ov := matchDedup(dedupIndex, keys)
		if ov.full {
			// Every covering ref, not just the first: per-site dedup lets
			// several references -- backlog issues, this run's peers, or
			// both -- jointly cover one intent, so a DupRef naming one of
			// them would point a reader at a partial answer.
			ref := strings.Join(ov.refs, ", ")
			fmt.Printf("    #%s  skipped duplicate issue-intent: %q (already tracked: %s)\n", num, in.Title, ref)
			out = append(out, filedIntent{Title: in.Title, Skipped: true, DupRef: ref})
			continue
		}
		// A partial overlap still files (issue #3808): the tally stays an
		// ordinary ok, but the run's output would otherwise go silent on the
		// fact that some of the finding's sites were already tracked -- the
		// same visibility #3609 required for a full skip. The covered keys
		// are bracketed because both halves of the line are lists: without
		// it, "site a, site b via #501, #502" reads as one four-item list.
		if ov.partial() {
			fmt.Printf("    #%s  filing issue-intent %q despite partial dedup overlap (already tracked: [%s] via %s)\n", num, in.Title, strings.Join(ov.covered, ", "), strings.Join(ov.refs, ", "))
		}
		link, extraLabels, onFiled := decorate(in)
		body := in.Body
		if link != "" {
			body = in.Body + "\n\n" + link
		}
		// The launcher's own marker goes last unconditionally, even carrying
		// no terms: parseDedupMarker is last-wins, so omitting it would let a
		// marker line quoted in body prose speak for this issue's key set.
		// On a partial overlap the marker deliberately carries the
		// already-covered term too: both issues are then valid "already
		// tracked" answers, and the next run sees full coverage and skips.
		marker := buildDedupMarker(in.DedupTerms)
		if marker == "" {
			marker = dedupMarkerPrefix + dedupMarkerSuffix
		}
		body = body + "\n\n" + marker
		ensureFilingLabel(it, provenanceLabel, &existingLabels)
		labels := []string{provenanceLabel}
		if l := ensureTypeLabel(it, in.Type, &existingLabels); l != "" {
			labels = append(labels, l)
		}
		for _, l := range extraLabels {
			ensureFilingLabel(it, l, &existingLabels)
		}
		labels = append(labels, extraLabels...)
		url, err := filer.PostIssue(in.Title, body, labels)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: issue-intent file failed: %v\n", num, err)
			out = append(out, filedIntent{Title: in.Title, Failed: true, Body: in.Body})
			continue
		}
		// Added only on a successful filing (not on failure): a transient
		// PostIssue error must not suppress a retry of the same intent within
		// this run.
		//
		// markdownInlineText, which buildSkippedIssuesSection applies to the
		// dedup ref, is the primary guard against a line break in this
		// reference; %q also keeps it newline-free.
		ref := fmt.Sprintf("this run's %q", in.Title)
		for k := range keys {
			dedupIndex[k] = ref
			runKeys[k] = ref
		}
		if onFiled != nil {
			onFiled(url)
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

// postSkippedComment posts a standalone "## Skipped (deduplicated)" comment
// for num when filed carries a dedup skip, a no-op otherwise. The work path
// (gate.go), unlike research.go, has no filed-issues verdict comment to append
// the section to, so it posts standalone -- see buildSkippedIssuesSection for
// why the skip needs a comment at all. Best-effort, matching postUsageComment's
// log-but-don't-propagate contract.
func postSkippedComment(it forge.IssueTracker, num string, filed []filedIntent) {
	section := buildSkippedIssuesSection(filed)
	if section == "" {
		return
	}
	if err := it.Comment(num, section); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not post skipped-issues comment: %v\n", num, err)
	}
}
