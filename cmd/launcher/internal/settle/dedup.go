package settle

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dedupmarker"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// findingLabels is the finding-label set isFindingIssue and
// backlogDedupIndex both match against, so a new provenance label lands in
// one place rather than several (ADR 0056: "Host dedup covers closed
// findings, for every finding kind").
var findingLabels = func() []string {
	// A kind may share another's FindingLabel (recover relays a work Box's
	// findings), so dedup rather than list one twice.
	seen := make(map[string]bool, len(dispatchkind.All))
	labels := make([]string, 0, len(dispatchkind.All))
	for _, d := range dispatchkind.All {
		if !seen[d.FindingLabel] {
			seen[d.FindingLabel] = true
			labels = append(labels, d.FindingLabel)
		}
	}
	// agent-butler-patch (ADR 0057) is no Descriptor's FindingLabel (the host
	// adds it beside agent-butler-finding on a patch PR); reading it from
	// doctor.ButlerLabelNames(), the return literal
	// nix/checks/dispatch-labels.nix parses, keeps a lib/labels.nix rename
	// from drifting past dedup silently.
	for _, l := range doctor.ButlerLabelNames() {
		if !seen[l] {
			labels = append(labels, l)
			seen[l] = true
		}
	}
	return labels
}()

// choreTermPrefix opens the dedup-marker term naming the Chore that filed a
// finding (ADR 0062). PriorFindings lists by it, and on a tracker whose label
// absence is not authoritative (IssuesWearLabels false) the merge hold reads
// it too. "=" is not a dedupPunctRun separator, so the term survives
// normalization. Dedup matching skips it: every finding of a Chore carries it.
const choreTermPrefix = "chore="

// choreTerm is the marker term naming Chore name.
func choreTerm(name string) string { return choreTermPrefix + name }

// isProvenanceTerm reports whether k records where a finding came from rather
// than a site to dedup on, so the index and the overlap match both skip it.
// Only a catalog Chore's term counts: a Box-supplied "chore=foo" is a site.
func isProvenanceTerm(k string) bool {
	name, ok := strings.CutPrefix(k, choreTermPrefix)
	return ok && slices.ContainsFunc(catalogRows(), func(r chore.Row) bool { return r.Name == name })
}

// dedupPunctRun matches a run of site-key separators plus any touching space,
// so "Type.Field", "Type:Field", "type#field", "type_field", and
// "Type. Field" fold alike.
var dedupPunctRun = regexp.MustCompile(` ?[.:#/_][ .:#/_]*`)

// normalizeDedupKey folds s into a comparison key: whitespace collapsed,
// lowercased, each dedupPunctRun folded to ":" (issue #3977), outer ":"
// trimmed. The fold target is never "-", since "a-:b" would become the
// rejected "a--b". A key with no letter or digit names no site and would only
// collide unrelated findings, so it is "" (issue #4019). Use
// normalizeDedupTerm when the result must be safe as a marker term.
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
// the result is usable as a dedup term. It drops, never rewrites, an empty
// term, one with "," (the marker's field separator, so it cannot round-trip),
// or one with "--", which could close the marker's HTML comment early.
// Stripping the bad part would invent a bogus key or repair a malformed one.
func normalizeDedupTerm(s string) (string, bool) {
	k := normalizeDedupKey(s)
	if k == "" || strings.Contains(k, ",") || strings.Contains(k, "--") {
		return "", false
	}
	return k, true
}

// DedupFiles extracts the file path from each "path/to/file.go:Symbol" dedup
// term (the path before the first ':'), deduping while keeping first-seen
// order. A term with no ':' is taken whole. A term the dedup key set would
// drop, or whose path names no site (e.g. "-:Foo"), contributes no file, so
// junk terms never satisfy decide's file-count floor (issue #4036). The path
// stays raw: the normalized key folds case and separators.
func DedupFiles(dedupTerms []string) []string {
	seen := make(map[string]bool, len(dedupTerms))
	var files []string
	for _, term := range dedupTerms {
		path, _, hasSymbol := strings.Cut(term, ":")
		if seen[path] || !UsableDedupTerm(term) || (hasSymbol && !UsableDedupTerm(path)) {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files
}

// UsableDedupTerm reports whether splitDedupTerms would keep s as a key, so
// the butler's file count (issue #4036) never counts a term the dedup key
// set drops.
func UsableDedupTerm(s string) bool {
	_, ok := normalizeDedupTerm(s)
	return ok
}

// splitDedupTerms partitions terms into a normalized dedup key set and the raw
// terms normalizeDedupTerm rejected, for the caller's warning. The title is
// never a key (issue #3609): distinct findings can share a formulaic title. An
// empty key set never matches the backlog; the in-run byte-identity dedup
// (issue #2068) still catches an identical retry.
func splitDedupTerms(terms []string) (keys map[string]bool, dropped []string) {
	keys = make(map[string]bool)
	for _, t := range terms {
		if k, ok := normalizeDedupTerm(t); ok {
			keys[k] = true
		} else if normalizeDedupKey(t) != "" {
			// A blank term carries nothing to lose, so only a rejected
			// non-blank one warns.
			dropped = append(dropped, t)
		}
	}
	return keys, dropped
}

// buildDedupMarker renders terms as the hidden marker line appended to a
// filed finding's body (issue #3609), from which backlogDedupIndex recovers
// the key set later. It returns "" when no term is usable; the caller then
// appends a bare dedupmarker.Bare so the launcher's marker is always last.
//
// Findings filed before #3609 carry no marker, a permanent but bounded gap
// now that closed findings are indexed too (#3873). They are deliberately not
// backfilled (issue #3813): keys guessed from prose could suppress a genuinely
// new finding indefinitely, worse than a visible duplicate. The Filer's own
// prose search covers them.
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
	return dedupmarker.Build(kept)
}

// parseDedupMarker recovers the normalized dedup terms from the LAST marker
// line in body, or nil. The filer appends its own marker last, so an earlier
// one is untrusted prose (a finding quoting the format) and must not hijack
// the key set (issue #3609). normalizeDedupTerm drops any piece a hand-rolled
// marker smuggles in.
func parseDedupMarker(body string) []string {
	var terms []string
	for _, t := range dedupmarker.Parse(body) {
		if k, ok := normalizeDedupTerm(t); ok {
			terms = append(terms, k)
		}
	}
	return terms
}

// carriesChoreMarker reports whether body's last dedup marker line carries
// the term naming Chore name. Like parseDedupMarker it ignores earlier marker
// lines, so a finding quoting the marker format cannot claim the term.
func carriesChoreMarker(body, name string) bool {
	return slices.Contains(parseDedupMarker(body), choreTerm(name))
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
// itself filed. Later calls overwrite earlier ones for a shared key. An issue
// that notPlannedExpired for chore at notPlannedBefore is skipped too.
func indexFindingIssues(index map[string]string, issues []forge.Issue, chore string, notPlannedBefore time.Time) {
	for _, iss := range issues {
		if !isFindingIssue(iss.Labels) {
			continue
		}
		if notPlannedExpired(iss, chore, notPlannedBefore) {
			continue
		}
		ref := "#" + iss.Number
		for _, k := range parseDedupMarker(iss.Body) {
			if isProvenanceTerm(k) {
				continue
			}
			index[k] = ref
		}
	}
}

// notPlannedExpired reports whether iss is a finding of chore closed
// not-planned at or before cutoff (ADR 0062 §6: a rejected idea may return
// once its cooldown ends). A zero cutoff, an unknown ClosedAt, or an empty
// chore never expires one, and a finding carrying another Chore's marker (or
// none, as a code finding) never does.
func notPlannedExpired(iss forge.Issue, chore string, cutoff time.Time) bool {
	return chore != "" && !cutoff.IsZero() && iss.ClosedNotPlanned && !iss.ClosedAt.IsZero() &&
		!iss.ClosedAt.After(cutoff) && carriesChoreMarker(iss.Body, chore)
}

// backlogDedupIndex maps every dedup key already covered to a reference: a
// "#<number>" finding issue, open or closed, since closing a finding is a
// durable triage decision (issue #3873) unless notPlannedExpired; the caller
// adds `this run's "<title>"` entries as it files. A failed list warns and
// adds nothing, so the closed and open lookups degrade independently. Closed
// is indexed first so the open issue wins a shared key. Only a test fake
// lacks forge.LabeledBacklogLister (jira and forgejo refuse a Connector
// without tracker.labeled), and it falls back to open issues only.
func backlogDedupIndex(it forge.IssueTracker, num, chore string, notPlannedBefore time.Time) map[string]string {
	index := make(map[string]string)
	lister, ok := forge.As[forge.LabeledBacklogLister](it)
	if !ok {
		issues, err := it.ListOpenIssues()
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: list open issues (unlabelled fallback) failed: %v\n", num, err)
			return index
		}
		indexFindingIssues(index, issues, chore, notPlannedBefore)
		return index
	}
	for _, state := range []forge.IssueState{forge.IssueClosed, forge.IssueOpen} {
		issues, err := lister.ListIssuesWithLabels(state, findingLabels)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    ?? #%s: list %s finding issues failed: %v\n", num, strings.ToLower(string(state)), err)
		}
		indexFindingIssues(index, issues, chore, notPlannedBefore)
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
	// refs names the distinct references covering them, in covered's
	// sorted order so it is deterministic despite map order.
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
	total := 0
	for k := range keys {
		if isProvenanceTerm(k) {
			continue
		}
		total++
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
	ov.full = total > 0 && len(ov.covered) == total
	return ov
}

// dedupKeyLineSpec parses key's trailing ":<line>" or ":<lo>-<hi>" segment
// (normalizeDedupKey never folds the range's "-", issue #4108) into the
// file prefix and line range. ok is false when key names no line site.
func dedupKeyLineSpec(key string) (prefix string, lo, hi int, ok bool) {
	i := strings.LastIndex(key, ":")
	if i <= 0 || i == len(key)-1 {
		return "", 0, 0, false
	}
	prefix, spec := key[:i], key[i+1:]
	if dash := strings.IndexByte(spec, '-'); dash >= 0 {
		loStr, hiStr := spec[:dash], spec[dash+1:]
		loN, okLo := parseDedupLineDigits(loStr)
		hiN, okHi := parseDedupLineDigits(hiStr)
		if !okLo || !okHi || loN <= 0 || hiN < loN {
			return "", 0, 0, false
		}
		return prefix, loN, hiN, true
	}
	n, okN := parseDedupLineDigits(spec)
	if !okN || n <= 0 {
		return "", 0, 0, false
	}
	return prefix, n, n, true
}

// parseDedupLineDigits parses s as a line number. Unlike strconv.Atoi it
// rejects a sign ("+80"), so a non-site key cannot pass as a line site (issue
// #4108); Atoi's error then fires only on overflow.
func parseDedupLineDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// dedupLineOverlap reports whether a and b name the same file (per
// dedupKeyLineSpec's prefix) with intersecting line ranges. A key that is not
// a line site (dedupKeyLineSpec's ok false) never overlaps.
func dedupLineOverlap(a, b string) bool {
	pa, loA, hiA, okA := dedupKeyLineSpec(a)
	pb, loB, hiB, okB := dedupKeyLineSpec(b)
	if !okA || !okB || pa != pb {
		return false
	}
	return loA <= hiB && loB <= hiA
}

// applyRunLineOverlap aliases each key not in dedupIndex to the ref of a
// runKeys entry with an overlapping line range in the same file, so
// "...go:80-83" and "...go:80" dedup as one (issue #4108). runKeys holds only
// this run's filings: against the backlog, nearby findings in a long-lived
// file are distinct. The smallest overlapping run key wins, for determinism.
// Aliases join runKeys too, so a later key overlapping only an alias chains
// onto the same ref, but only after the loop: joining mid-loop would let a key
// chain onto its own sibling, making the result depend on map order.
func applyRunLineOverlap(dedupIndex, runKeys map[string]string, keys map[string]bool) {
	aliases := make(map[string]string)
	for k := range keys {
		if _, ok := dedupIndex[k]; ok {
			continue
		}
		var best string
		for rk := range runKeys {
			if !dedupLineOverlap(k, rk) {
				continue
			}
			if best == "" || rk < best {
				best = rk
			}
		}
		if best != "" {
			aliases[k] = runKeys[best]
		}
	}
	for k, ref := range aliases {
		dedupIndex[k] = ref
		runKeys[k] = ref
	}
}

// DedupPolicy is a filing's dedup shape (ADR 0062 §6); its zero value is site
// keys with permanent suppression, what research and the code Chores use.
type DedupPolicy struct {
	// Terms, when set, replaces a finding's own DedupTerms as both its dedup
	// key set and its marker terms.
	Terms func(Finding) []string
	// Chore and NotPlannedBefore: a closed-not-planned finding of Chore
	// (carrying its chore=<name> marker term) closed at or before
	// NotPlannedBefore no longer suppresses; zero never expires.
	Chore            string
	NotPlannedBefore time.Time
}

// PriorFinding is a finding issue of one Chore as the backlog holds it.
type PriorFinding struct {
	Issue forge.Issue
	// Open reports the issue was listed open; false means closed.
	Open bool
	// Terms are the dedup marker's terms that are not provenance terms, in
	// marker order; the Chore reads its own pattern key out of them.
	Terms []string
}

// PriorFindings lists chore's findings in the backlog, open and closed,
// recognised by the chore term in their dedup marker so trackers with no
// label registry list them too. A list failure returns the other lookup's
// findings beside the first error.
func PriorFindings(it forge.IssueTracker, chore string) ([]PriorFinding, error) {
	var out []PriorFinding
	collect := func(issues []forge.Issue, open bool) {
		for _, iss := range issues {
			if !isFindingIssue(iss.Labels) || !carriesChoreMarker(iss.Body, chore) {
				continue
			}
			f := PriorFinding{Issue: iss, Open: open}
			for _, k := range parseDedupMarker(iss.Body) {
				if !isProvenanceTerm(k) {
					f.Terms = append(f.Terms, k)
				}
			}
			out = append(out, f)
		}
	}
	lister, ok := forge.As[forge.LabeledBacklogLister](it)
	if !ok {
		issues, err := it.ListOpenIssues()
		collect(issues, true)
		return out, err
	}
	var firstErr error
	for _, state := range []forge.IssueState{forge.IssueOpen, forge.IssueClosed} {
		issues, err := lister.ListIssuesWithLabels(state, findingLabels)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("list %s finding issues: %w", strings.ToLower(string(state)), err)
		}
		collect(issues, state == forge.IssueOpen)
	}
	return out, firstErr
}
