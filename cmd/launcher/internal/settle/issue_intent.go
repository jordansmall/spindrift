package settle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/rivo/uniseg"

	"spindrift.dev/launcher/internal/dedupmarker"
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

// maxConcurrenceLen bounds Concurrence in grapheme clusters, so truncation
// never splits a user-perceived character. maxConcurrenceBytes is what
// actually bounds size, since one cluster can carry unboundedly many combining
// marks, and this Box-supplied prose lands in a host-authored note; 1024 is a
// small slice of GitHub's 65,536-character body cap.
const (
	maxConcurrenceLen   = 200
	maxConcurrenceBytes = 1024
)

// sanitizeConcurrence truncates s to both caps at a grapheme-cluster boundary
// and neutralizes backticks. Only an oversized first cluster yields "", which
// fails closed: internal/butler rejects a blank concurrence. With no backtick
// left, the text cannot close the code span promotionNote quotes it in, so
// spoofed @mentions, links or HTML stay inert.
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
// base64-decoded and nonce-verified, and re-validates it: the log carrier
// reaches settle unchecked. Any reject skips the whole intent, so an illegal
// Class is never silently dropped from one.
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

// ParseIssueIntent reports raw's wire intent when filing would accept it,
// sharing parseIssueIntent's gate so a caller vetting an intent ahead of
// filing cannot diverge from what filing skips.
func ParseIssueIntent(raw string) (signalwire.IssueIntent, bool) {
	in, rej := parseIssueIntent(raw)
	return in.IssueIntent, rej == nil
}

// filedIntent is the outcome of filing one issue-intent: a URL on success,
// Failed with the body kept so a caller can render it inline, Unusable when
// the tracker filed it but its URL failed core's check, or Skipped on a dedup
// match (issue #3609), with neither URL nor Body.
type filedIntent struct {
	Title   string
	URL     string
	Failed  bool
	Body    string
	Skipped bool
	// Unusable means PostIssue returned an error wrapping
	// forge.ErrFiledUnusableURL: the issue exists on the tracker with no URL
	// to show, so unlike Failed it must never be refiled. Body is kept for
	// inline rendering. Exclusive with Failed and Skipped.
	Unusable bool
	// DupRef is a comma-joined, display-only list of every reference
	// covering the intent, backlog "#<number>" issues and this run's
	// "<title>" peers mixed, so a Skipped entry names its match (issue
	// #3811). A string because its one renderer never branches on kind
	// (issue #3831).
	DupRef string
}

// degradeIssueIntents turns each well-formed payload into a Failed filedIntent
// with its body kept, for a settle that cannot file them, so the finding
// renders inline rather than vanishing. reason names why filing is refused and
// is warned once when any finding degrades.
func degradeIssueIntents(num string, result dispatch.Result, reason string) []filedIntent {
	var out []filedIntent
	for _, raw := range result.IssueIntents {
		in, rej := parseIssueIntent(raw)
		if rej != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: skipping invalid issue-intent payload: %s\n", num, rej.Reason)
			continue
		}
		out = append(out, filedIntent{Title: in.Title, Failed: true, Body: in.Body})
	}
	if len(out) > 0 {
		fmt.Fprintf(os.Stderr, "    ?? #%s: refusing to file %d issue intent(s): %s\n", num, len(out), reason)
	}
	return out
}

// fileIssueIntentsDetailed returns one filedIntent per well-formed payload in
// payload order, success or failure, and appends a non-empty bodyBacklink to
// each body before filing. Labels are host-derived, the provenanceLabel plus
// any ensureTypeLabel match, never the payload's own (issue #1949). A package
// function, not a *Settle method, so ResearchSettle can call it (issue #2590).
func fileIssueIntentsDetailed(progress Progress, it forge.IssueTracker, num string, result dispatch.Result, provenanceLabel, bodyBacklink string) []filedIntent {
	return fileIssueIntentsDetailedFunc(progress, it, num, result, provenanceLabel, DedupPolicy{}, func(issueIntent) (string, []string, func(string)) { return bodyBacklink, nil, nil })
}

// fileIssueIntentsDetailedFunc is fileIssueIntentsDetailed's per-intent sibling
// (issue #3875): decorate runs per intent, returning its backlink, extraLabels
// appended after the provenance and type labels (e.g. the Butler's
// auto-promotion "ready-for-agent", issue #3880), and onFiled, called with the
// URL once the issue exists on the tracker, so a failed filing never burns the
// Butler's per-run promotion room. An unusable-URL filing calls it with "": the
// issue still wears extraLabels, so the callback must still commit its spend.
func fileIssueIntentsDetailedFunc(progress Progress, it forge.IssueTracker, num string, result dispatch.Result, provenanceLabel string, dedup DedupPolicy, decorate func(issueIntent) (bodyBacklink string, extraLabels []string, onFiled func(url string))) []filedIntent {
	if !result.IssueIntentsFound {
		return nil
	}
	// Filing stays best-effort: a tracker that cannot file degrades each
	// finding inline (issue #6343).
	filer, ok := forge.As[forge.HostPostedIssueFiler](it)
	if !ok {
		return degradeIssueIntents(num, result, fmt.Sprintf("tracker %T cannot file issues host-side (no HostPostedIssueFiler capability)", it))
	}
	// A blank label means the settle was built without its kind's descriptor;
	// filing would leave each finding outside the label dedup and triage key on.
	if provenanceLabel == "" {
		return degradeIssueIntents(num, result, "no provenance label (a construction bug: the settle was built without a dispatch kind's finding label)")
	}
	// Listed once per pass, not per finding. A listErr is non-fatal:
	// ensureLabel falls back to create-then-recheck.
	existingLabels, listErr := it.ListLabels()
	if listErr != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: list labels failed: %v\n", num, listErr)
	}
	// Backlog dedup keys (issue #3609; closed findings too, #3873), built
	// lazily at most once per pass: an intent with no keys can never match.
	// backlogDedupIndex never returns nil, so the nil check is a true memo.
	// dedupIndex grows as this run files, so duplicates within one payload
	// dedup too; runKeys is that within-run subset, so line overlap never
	// matches the backlog (issue #4108).
	var dedupIndex map[string]string
	runKeys := make(map[string]string)
	var out []filedIntent
	for _, raw := range result.IssueIntents {
		in, rej := parseIssueIntent(raw)
		if rej != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: skipping invalid issue-intent payload: %s\n", num, rej.Reason)
			continue
		}
		terms := in.DedupTerms
		if dedup.Terms != nil {
			terms = dedup.Terms(in.finding())
		}
		keys, dropped := splitDedupTerms(terms)
		if len(dropped) > 0 {
			fmt.Fprintf(os.Stderr, "    ?? #%s: dropped unusable dedup term(s) %q\n", num, dropped)
		}
		if len(keys) == 0 {
			fmt.Fprintf(os.Stderr, "    ?? #%s: no usable dedup term for %q: filing without dedup\n", num, in.Title)
		} else if dedupIndex == nil {
			dedupIndex = backlogDedupIndex(it, num, dedup.Chore, dedup.NotPlannedBefore)
		}
		applyRunLineOverlap(dedupIndex, runKeys, keys)
		ov := matchDedup(dedupIndex, keys)
		if ov.full {
			// Every covering ref: per-site dedup lets several references
			// jointly cover one intent.
			ref := strings.Join(ov.refs, ", ")
			progress.Step(Step{Key: num, Detail: fmt.Sprintf("skipped duplicate issue-intent: %q (already tracked: %s)", in.Title, ref)})
			out = append(out, filedIntent{Title: in.Title, Skipped: true, DupRef: ref})
			continue
		}
		// A partial overlap still files (issue #3808) but says so. The
		// covered keys are bracketed because both halves are lists: "site a,
		// site b via #501, #502" would read as one four-item list.
		if ov.partial() {
			progress.Step(Step{Key: num, Detail: fmt.Sprintf("filing issue-intent %q despite partial dedup overlap (already tracked: [%s] via %s)", in.Title, strings.Join(ov.covered, ", "), strings.Join(ov.refs, ", "))})
		}
		link, extraLabels, onFiled := decorate(in)
		body := in.Body
		if link != "" {
			body = in.Body + "\n\n" + link
		}
		// The launcher's marker goes last even with no terms: parseDedupMarker
		// is last-wins, so omitting it would let a marker quoted in body prose
		// speak for this issue. On a partial overlap it keeps the covered terms
		// too, so the next run sees full coverage and skips. A Chore's finding
		// also carries its choreTerm, so PriorFindings and the merge hold can
		// recognise it without the label; deriving it from the label keeps the
		// two from diverging.
		markerTerms := terms
		for _, row := range catalogRows() {
			if wearsFindingLabel(row, extraLabels) {
				markerTerms = append(slices.Clone(markerTerms), choreTerm(row.Name))
			}
		}
		marker := buildDedupMarker(markerTerms)
		if marker == "" {
			marker = dedupmarker.Bare
		}
		body = dedupmarker.Append(body, marker)
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
		// Only a filed issue indexes its keys: a transient PostIssue error must
		// not suppress a retry within this run, but an unusable URL means the
		// tracker did file it, so a same-keyed intent must not file twice. %q
		// keeps the ref newline-free, backing up markdownInlineText.
		unusable := errors.Is(err, forge.ErrFiledUnusableURL)
		if err == nil || unusable {
			ref := fmt.Sprintf("this run's %q", in.Title)
			for k := range keys {
				dedupIndex[k] = ref
				runKeys[k] = ref
			}
		}
		switch {
		case unusable:
			fmt.Fprintf(os.Stderr, "    ?? #%s: issue-intent %q was filed, but the tracker's reply is unusable: %v\n", num, in.Title, err)
			if onFiled != nil {
				onFiled("")
			}
			out = append(out, filedIntent{Title: in.Title, Unusable: true, Body: in.Body})
		case err != nil:
			fmt.Fprintf(os.Stderr, "    ?? #%s: issue-intent file failed: %v\n", num, err)
			out = append(out, filedIntent{Title: in.Title, Failed: true, Body: in.Body})
		default:
			if onFiled != nil {
				onFiled(url)
			}
			out = append(out, filedIntent{Title: in.Title, URL: url})
		}
	}
	return out
}

// filedTally is one run's issue-filing volume. Failures count apart so a run
// that tried and failed does not read as quiet (issue #3608), a dedup
// skip (issue #3609) is neither success nor failure, and an unusable-URL
// filing exists on the tracker so it is not a failure either.
type filedTally struct {
	ok       int
	failed   int
	skipped  int
	unusable int
}

// tallyFiled counts filed by filedIntent.Failed/Skipped/Unusable.
func tallyFiled(filed []filedIntent) filedTally {
	var t filedTally
	for _, f := range filed {
		switch {
		case f.Skipped:
			t.skipped++
		case f.Unusable:
			t.unusable++
		case f.Failed:
			t.failed++
		default:
			t.ok++
		}
	}
	return t
}

// String renders comma-joined name:count pairs (issues #3608, #3609), so a new
// count is one more pair rather than a format change.
func (t filedTally) String() string {
	return fmt.Sprintf("ok:%d,failed:%d,skipped:%d,unusable:%d", t.ok, t.failed, t.skipped, t.unusable)
}

// reportFiled prints the filing tally for issue num, always — even a zero
// tally (filed=ok:0,failed:0,skipped:0,unusable:0) — so "reached filing and filed
// nothing" reads differently in the transcript than "never reached filing"
// (no line at all).
func reportFiled(progress Progress, num string, filed []filedIntent) {
	progress.Step(Step{Key: num, Detail: fmt.Sprintf("filed=%s", tallyFiled(filed))})
}

// postSkippedComment posts a standalone "## Skipped (deduplicated)" comment
// for num when filed carries a dedup skip: unlike research, the work path has
// no verdict comment to append it to. Best-effort.
func postSkippedComment(it forge.IssueTracker, num string, filed []filedIntent) {
	section := buildSkippedIssuesSection(filed)
	if section == "" {
		return
	}
	if err := it.Comment(num, section); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not post skipped-issues comment: %v\n", num, err)
	}
}
