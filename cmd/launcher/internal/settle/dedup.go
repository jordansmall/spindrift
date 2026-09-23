package settle

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// findingLabelReview and findingLabelResearch are the two provenance labels
// fileIssueIntentsDetailed's callers pass (gate.go's work path and
// research.go's research path), named here for isFindingIssue to match the
// open backlog against. The two call sites keep their own string literals
// rather than using these: nix/checks/dispatch-labels.nix extracts the label
// straight out of gate.go's call as source text, and a constant there
// extracts as nothing.
const (
	findingLabelReview   = "agent-review-finding"
	findingLabelResearch = "agent-research-finding"
)

// dedupMarkerPrefix and dedupMarkerSuffix delimit the hidden marker line
// buildDedupMarker appends to a filed finding's body (issue #3609): carrying
// the intent's own DedupTerms lets a later run recover this issue's dedup key
// set from the open backlog (backlogDedupIndex) without re-parsing prose.
const (
	dedupMarkerPrefix = "<!-- spindrift-dedup: "
	dedupMarkerSuffix = " -->"
)

// normalizeDedupKey folds s into a comparison key: trimmed, internal
// whitespace runs collapsed to one space, lowercased. Returns "" for a blank
// or whitespace-only s. Pure normalization only -- normalizeDedupTerm is the
// one to call when the result must also be safe to carry as a marker term.
func normalizeDedupKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// normalizeDedupTerm normalizes s per normalizeDedupKey and reports whether
// the result is usable as a dedup term. Rejects (drops, never rewrites) a
// term that is empty, contains "," -- the marker line's own field separator,
// so a term carrying one could never round-trip through
// buildDedupMarker/parseDedupMarker -- or contains "--", which inside
// buildDedupMarker's HTML comment could close it early (e.g. a term holding
// "-->") and corrupt the rest of the key set. Splitting or stripping the bad
// substring instead would either invent a bogus generic key or silently
// repair a malformed payload; dropping the whole term is the only safe move.
func normalizeDedupTerm(s string) (string, bool) {
	k := normalizeDedupKey(s)
	if k == "" || strings.Contains(k, ",") || strings.Contains(k, "--") {
		return "", false
	}
	return k, true
}

// splitDedupTerms partitions terms into a dedup key set -- terms normalized,
// invalid or blank ones dropped -- and the raw (un-normalized) terms
// normalizeDedupTerm rejected, so fileIssueIntentsDetailed can warn about a
// dropped term without re-running the normalize loop itself. The title is
// deliberately excluded from the key set (issue #3609 review): two distinct
// findings can share a formulaic title (e.g. a conventional-commit prefix),
// and keying on prose would merge them. Terms carrying nothing usable
// therefore yield an empty key set that never matches the backlog index --
// the in-run byte-identity dedup (issue #2068) still covers a byte-identical
// retry within one run.
func splitDedupTerms(terms []string) (keys map[string]bool, dropped []string) {
	keys = make(map[string]bool)
	for _, t := range terms {
		if k, ok := normalizeDedupTerm(t); ok {
			keys[k] = true
		} else if normalizeDedupKey(t) != "" {
			// A blank term (empty after normalization) is not a drop worth
			// warning about -- it carries no information to lose. Only a
			// non-blank term that normalizeDedupTerm still rejected (comma
			// or "--") is a real degradation.
			dropped = append(dropped, t)
		}
	}
	return keys, dropped
}

// buildDedupMarker renders terms as the hidden marker line appended to a
// filed finding's body, or "" when terms carries no usable term (every entry
// invalid or blank after normalizeDedupTerm). "" does not mean "append
// nothing": fileIssueIntentsDetailed substitutes a bare
// dedupMarkerPrefix+dedupMarkerSuffix, which parseDedupMarker reads back as
// zero keys, so the launcher's own marker is always the body's last one.
func buildDedupMarker(terms []string) string {
	var kept []string
	for _, t := range terms {
		if k, ok := normalizeDedupTerm(t); ok {
			kept = append(kept, k)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return dedupMarkerPrefix + strings.Join(kept, ", ") + dedupMarkerSuffix
}

// parseDedupMarker recovers the normalized dedup terms from the LAST marker
// line in a body, or nil when no such line is present. Last wins because the
// filer always appends its own marker last (issue_intent.go); an earlier
// marker line is untrusted body prose (e.g. a finding quoting the marker
// format in a fenced code block) and must not hijack the key set (issue
// #3609). A comma-splitting hand-rolled marker could smuggle a "," or "--"
// through a single raw term; normalizeDedupTerm drops any such split piece
// rather than trusting it.
func parseDedupMarker(body string) []string {
	lines := strings.Split(body, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, dedupMarkerPrefix) || !strings.HasSuffix(line, dedupMarkerSuffix) {
			continue
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(line, dedupMarkerPrefix), dedupMarkerSuffix)
		var terms []string
		for _, t := range strings.Split(inner, ",") {
			if k, ok := normalizeDedupTerm(t); ok {
				terms = append(terms, k)
			}
		}
		return terms
	}
	return nil
}

// isFindingIssue reports whether labels carries one of the two provenance
// labels fileIssueIntentsDetailed files with. Dedup only ever consults
// finding issues: an ordinary backlog issue that happens to share a title or
// term must never suppress a filing.
func isFindingIssue(labels []string) bool {
	for _, l := range labels {
		if l == findingLabelReview || l == findingLabelResearch {
			return true
		}
	}
	return false
}

// backlogDedupIndex maps every dedup key already covered to a
// human-readable reference: a "#<number>" for a key covered by an open
// finding issue, built once per filing pass from listBacklogForDedup (issue
// #3609, review), or a `this run's "<title>"` string for a key
// fileIssueIntentsDetailed adds in place as this same run files its own
// intents. A list failure is non-fatal: it warns, in the same style as the
// sibling ListLabels warning this call sits beside, and the pass falls back
// to intra-run-only dedup.
func backlogDedupIndex(it forge.IssueTracker, num string) map[string]string {
	index := make(map[string]string)
	issues, err := listBacklogForDedup(it)
	if err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: list open issues failed: %v\n", num, err)
		return index
	}
	for _, iss := range issues {
		if !isFindingIssue(iss.Labels) {
			continue
		}
		ref := "#" + iss.Number
		for _, k := range parseDedupMarker(iss.Body) {
			index[k] = ref
		}
	}
	return index
}

// listBacklogForDedup prefers forge.LabeledBacklogLister, scoped to the two
// finding labels, which unlike ListOpenIssues on GitHub carries Body and
// isn't truncated to the oldest page (issue #3609 review). Adapters without
// the capability (forgejo, jira, local) already populate Body and walk every
// page via ListOpenIssues, so they fall back to it unchanged; isFindingIssue
// still filters the result either way, since the fallback read is
// unlabelled.
func listBacklogForDedup(it forge.IssueTracker) ([]forge.Issue, error) {
	if lister, ok := it.(forge.LabeledBacklogLister); ok {
		return lister.ListOpenIssuesWithLabels([]string{findingLabelReview, findingLabelResearch})
	}
	return it.ListOpenIssues()
}

// dedupOverlap records how much of an intent's dedup key set the index
// already tracks. A multi-site finding emits one key per site (filer socket
// spec), so a hit on one key proves only that site is tracked -- suppressing
// the whole intent would leave the uncovered site tracked nowhere (issue
// #3808).
type dedupOverlap struct {
	// covered is the subset of the intent's keys already tracked, sorted.
	covered []string
	// refs names the distinct issues covering them -- more than one when
	// the finding's sites are tracked by separate backlog issues. It is
	// built by walking covered in its sorted order, which is what makes it
	// deterministic despite Go's randomized map order.
	refs []string
	// full is true only when every key is covered and there was at least
	// one: an empty key set must never read as "already tracked".
	full bool
}

// partial reports the middle arm of the none/partial/full decision: some of
// the intent's sites are already tracked, but not all, so the intent still
// files and says so.
func (ov dedupOverlap) partial() bool { return !ov.full && len(ov.covered) > 0 }

// matchDedup partitions keys against index, returning how much of the set
// index already tracks.
func matchDedup(index map[string]string, keys map[string]bool) dedupOverlap {
	var ov dedupOverlap
	for k := range keys {
		if _, ok := index[k]; ok {
			ov.covered = append(ov.covered, k)
		}
	}
	sort.Strings(ov.covered)
	seen := make(map[string]bool)
	for _, k := range ov.covered {
		ref := index[k]
		if !seen[ref] {
			seen[ref] = true
			ov.refs = append(ov.refs, ref)
		}
	}
	ov.full = len(keys) > 0 && len(ov.covered) == len(keys)
	return ov
}
