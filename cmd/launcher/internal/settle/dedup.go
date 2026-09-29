package settle

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// findingLabels is the finding-label set isFindingIssue and
// backlogDedupIndex both match against, so a new provenance label lands in
// one place rather than several (ADR 0056: "Host dedup covers closed
// findings, for every finding kind").
var findingLabels = func() []string {
	labels := make([]string, len(dispatchkind.All))
	for i, d := range dispatchkind.All {
		labels[i] = d.FindingLabel
	}
	// agent-butler-patch (ADR 0057) is not a Descriptor's FindingLabel: the
	// host adds it alongside agent-butler-finding when it lands a finding as
	// a patch PR. Folding in doctor.ButlerLabelNames() rather than a bare
	// literal ties this list to the source nix/checks/dispatch-labels.nix
	// extracts from -- for as long as ButlerLabelNames stays the
	// return-literal that check parses -- so a rename in lib/labels.nix
	// can't drift from dedup silently.
	seen := make(map[string]bool, len(labels))
	for _, l := range labels {
		seen[l] = true
	}
	for _, l := range doctor.ButlerLabelNames() {
		if !seen[l] {
			labels = append(labels, l)
			seen[l] = true
		}
	}
	return labels
}()

// dedupMarkerPrefix and dedupMarkerSuffix delimit the hidden marker line
// buildDedupMarker appends to a filed finding's body (issue #3609): carrying
// the intent's own DedupTerms lets a later run recover this issue's dedup key
// set from the backlog (backlogDedupIndex) without re-parsing prose.
const (
	dedupMarkerPrefix = "<!-- spindrift-dedup: "
	dedupMarkerSuffix = " -->"
)

// dedupPunctRun matches a run of site-key separators plus any touching space,
// so "Type.Field", "Type:Field", "type#field", "type_field", and
// "Type. Field" fold alike.
var dedupPunctRun = regexp.MustCompile(` ?[.:#/_][ .:#/_]*`)

// normalizeDedupKey folds s into a comparison key: trimmed, internal
// whitespace runs collapsed to one space, lowercased, each dedupPunctRun
// folded to one ":" (issue #3977), and any leading/trailing ":" trimmed.
// The fold target is never "-": "a-:b" would become "a--b", which
// normalizeDedupTerm rejects. Returns "" for a blank, whitespace-only, or
// separator-only s, and for one with no letter or digit (e.g. "-" or "!!"),
// which names no site and would only collide unrelated findings (issue
// #4019). Pure normalization only -- normalizeDedupTerm is the one to call
// when the result must also be safe to carry as a marker term.
func normalizeDedupKey(s string) string {
	k := strings.ToLower(strings.Join(strings.Fields(s), " "))
	k = dedupPunctRun.ReplaceAllString(k, ":")
	k = strings.Trim(k, ":")
	if !strings.ContainsFunc(k, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return ""
	}
	return k
}

// normalizeDedupTerm normalizes s per normalizeDedupKey and reports whether
// the result is usable as a dedup term. Rejects (drops, never rewrites) a
// term that is empty, contains "," -- the marker line's own field separator,
// so a term carrying one could never round-trip through
// buildDedupMarker/parseDedupMarker -- or contains "--", which inside
// buildDedupMarker's HTML comment could close it early (e.g. a term holding
// "foo-->bar") and corrupt the rest of the key set. Splitting or stripping
// the bad substring instead would either invent a bogus generic key or
// silently repair a malformed payload; dropping the whole term is the only
// safe move.
func normalizeDedupTerm(s string) (string, bool) {
	k := normalizeDedupKey(s)
	if k == "" || strings.Contains(k, ",") || strings.Contains(k, "--") {
		return "", false
	}
	return k, true
}

// UsableDedupTerm reports whether splitDedupTerms would keep s as a key, so
// the butler's file count (issue #4036) never counts a term the dedup key
// set drops.
func UsableDedupTerm(s string) bool {
	_, ok := normalizeDedupTerm(s)
	return ok
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

// isFindingIssue reports whether labels carries one of findingLabels, the
// provenance labels fileIssueIntentsDetailed files with. Dedup only ever
// consults finding issues: an ordinary backlog issue that happens to share
// a title or term must never suppress a filing.
func isFindingIssue(labels []string) bool {
	for _, l := range labels {
		if slices.Contains(findingLabels, l) {
			return true
		}
	}
	return false
}

// indexFindingIssues folds each finding-labelled issue in issues into index,
// keyed by its marker's dedup terms and valued "#<number>". A non-finding
// issue is skipped: dedup only ever consults an issue fileIssueIntentsDetailed
// itself filed. Later calls overwrite earlier ones for a shared key.
func indexFindingIssues(index map[string]string, issues []forge.Issue) {
	for _, iss := range issues {
		if !isFindingIssue(iss.Labels) {
			continue
		}
		ref := "#" + iss.Number
		for _, k := range parseDedupMarker(iss.Body) {
			index[k] = ref
		}
	}
}

// backlogDedupIndex maps every dedup key already covered to a
// human-readable reference: a "#<number>" for a key covered by a finding
// issue, open or closed -- closing a finding is a durable triage decision the
// host enforces (issue #3873) -- or a `this run's "<title>"` string for a key
// fileIssueIntentsDetailed adds in place as this same run files its own
// intents. A list failure is non-fatal: it warns, in the same style as the
// sibling ListLabels warning this call sits beside, and contributes nothing
// to the index -- exactly as an open-lookup failure did before #3873 -- so
// the closed and open lookups degrade independently: during an open-side
// outage, closed keys still suppress a refile. Closed is indexed first so an
// open issue, the one still actively tracked, wins a shared key. Every real
// adapter implements forge.LabeledBacklogLister; a tracker without it (a
// test fake) falls back to ListOpenIssues, open-only.
func backlogDedupIndex(it forge.IssueTracker, num string) map[string]string {
	index := make(map[string]string)
	lister, ok := it.(forge.LabeledBacklogLister)
	if !ok {
		issues, err := it.ListOpenIssues()
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: list open issues (unlabelled fallback) failed: %v\n", num, err)
			return index
		}
		indexFindingIssues(index, issues)
		return index
	}
	for _, state := range []forge.IssueState{forge.IssueClosed, forge.IssueOpen} {
		issues, err := lister.ListIssuesWithLabels(state, findingLabels)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: list %s finding issues failed: %v\n", num, strings.ToLower(string(state)), err)
		}
		indexFindingIssues(index, issues)
	}
	return index
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
