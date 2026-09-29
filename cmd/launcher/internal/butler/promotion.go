package butler

import (
	"fmt"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/settle"
)

// promotion is the host-side auto-promotion gate for one Chore's findings
// (issue #3880, ADR 0056, issue #3993). Per ADR 0056, classes is the only
// trust gate -- the host-side allow-list (BUTLER_CHORE_CLASSES); the
// in-Box reviewer's concurrence and the file count read off the finding's
// own dedup terms are quality filters on an already-trusted class, not
// trust gates themselves. A Box's issue-intent payload is only ever an
// input to decide, never the gate itself, so a Box cannot widen its own
// allow-list, raise its own file limit, or grant itself more of the day's
// promotion room.
type promotion struct {
	// enabled is the sole on/off derivation every caller checks before
	// decide can promote anything -- see newPromotion.
	enabled bool
	// classes is this Chore's host-side finding-class allow-list (from
	// BUTLER_CHORE_CLASSES). Empty means no class is promotable -- an
	// unconfigured Chore reads as opted out, not "trust the Box".
	classes []string
	// maxFiles is the host limit on how many files a promoted finding may
	// touch. A finding naming zero files is never promotable regardless of
	// maxFiles (scope unknown).
	maxFiles int
	// label is the work kind's own configured dispatch label (LABEL,
	// Consumer-configurable) -- carried on a promoted finding alongside
	// "agent-butler-finding" so the work path picks it up (issue #3880).
	label string
}

// newPromotion builds a promotion from a Chore's allow-listed classes, the
// host's per-finding file limit, its per-day promotion budget, and the
// Consumer's work dispatch label. enabled requires all three of a positive
// per-day budget, a configured label, and at least one allow-listed class:
// any one of them unset reads as "promotion off", never as "trust whatever
// the unset one happens to be".
func newPromotion(classes []string, maxFiles, perDay int, label string) promotion {
	return promotion{
		enabled:  perDay > 0 && label != "" && len(classes) > 0,
		classes:  classes,
		maxFiles: maxFiles,
		label:    label,
	}
}

// decisionKind names what decide chose for one finding. It is an enum
// rather than a bool so a future third case -- a Patch decision, evaluated
// before Promote -- can sit alongside skip/promote without a signature
// change (issue #3993).
type decisionKind int

const (
	skip decisionKind = iota
	promote
)

// decision is decide's verdict on one finding.
type decision struct {
	kind   decisionKind
	labels []string // only set when kind is promote
	reason string   // short; read only by test failure messages
	files  int      // file count off the finding's dedup terms; only set when kind is promote
}

// decide is the promotion gate for one finding, checked in order: on/off,
// the trust gate (allow-listed class), the quality filters (file count,
// reviewer concurrence), then the shared per-sweep room. Each gate below
// decide is its own predicate method so it stays separately testable, and
// so a future patch gate has somewhere to sit alongside these. room is this
// call's remaining budget, not a field on p -- the settle step tracks it
// across findings in one sweep, spending it as findings promote.
func (p promotion) decide(f settle.Finding, room int) decision {
	if !p.enabled {
		return decision{kind: skip, reason: "promotion off"}
	}
	if !p.allowListed(f) {
		return decision{kind: skip, reason: "class not allow-listed"}
	}
	files := butlerFiles(f.DedupTerms)
	if !p.withinFileLimit(files) {
		return decision{kind: skip, reason: "file count outside host limit"}
	}
	if !p.concurred(f) {
		return decision{kind: skip, reason: "no reviewer concurrence"}
	}
	if room <= 0 {
		return decision{kind: skip, reason: "no room"}
	}
	return decision{kind: promote, labels: []string{p.label}, reason: "promoted", files: len(files)}
}

// allowListed is the trust gate (ADR 0056): f's class must be non-blank and
// on the host's own classes list.
func (p promotion) allowListed(f settle.Finding) bool {
	return f.Class != "" && slices.Contains(p.classes, f.Class)
}

// withinFileLimit: files comes from the Box's own dedup terms, so a finding
// naming zero files (scope unknown) or more than the host's maxFiles never
// promotes.
func (p promotion) withinFileLimit(files []string) bool {
	return len(files) >= 1 && len(files) <= p.maxFiles
}

// concurred: f.Concurrence is already sanitized (settle.parseIssueIntent),
// so this only needs to check it's non-blank.
func (p promotion) concurred(f settle.Finding) bool {
	return strings.TrimSpace(f.Concurrence) != ""
}

// promotionNote renders the visible note appended to a promoted finding's
// body, naming the class and quoting the reviewer's own words (issue #3880)
// rather than just asserting agreement -- a reader should be able to check
// the reviewer's claim, not just trust that it happened. f.Concurrence
// arrives already sanitized (settle.parseIssueIntent runs before a Finding
// ever reaches this package), so only oneLine's collapse is needed here to
// keep the reviewer's prose from breaking the note's single sentence.
func promotionNote(choreName string, f settle.Finding, policy promotion, nFiles int) string {
	concurrence := oneLine(f.Concurrence)
	return fmt.Sprintf(
		"**Auto-promoted** to `%s` by the butler: class `%s` is on the `%s` Chore's allow-list, it touches %d file(s) (host limit %d), and the in-Box reviewer agreed: `%s`",
		policy.label, f.Class, choreName, nFiles, policy.maxFiles, concurrence,
	)
}

// oneLine collapses s's internal whitespace, including any newline, down to
// single spaces and trims the ends -- Concurrence is Box-supplied reviewer
// prose (issue #3880) and must render as one line in host-authored note text.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// butlerBacklink renders the per-intent backlink appended to a filed
// finding's body: which Chore filed it, the Box-claimed class if any (issue
// #3870/#3880 -- named here regardless of whether the finding ends up
// promoted), and which files (the path before the first ':' in each dedup
// term, deduped, order kept) it concerns. The Files sentence is omitted
// entirely when terms yields no paths, rather than printing "Files: ".
func butlerBacklink(choreName string, f settle.Finding) string {
	lead := fmt.Sprintf("Filed by the butler's `%s` Chore.", choreName)
	if f.Class != "" {
		lead += fmt.Sprintf(" Class: `%s`.", f.Class)
	}
	files := butlerFiles(f.DedupTerms)
	if len(files) == 0 {
		return lead
	}
	quoted := make([]string, len(files))
	for i, path := range files {
		quoted[i] = "`" + path + "`"
	}
	return fmt.Sprintf("%s Files: %s.", lead, strings.Join(quoted, ", "))
}

// butlerFiles extracts the file path from each "path/to/file.go:Symbol" dedup
// term (the path before the first ':'), deduping while keeping first-seen
// order. A term with no ':' is taken whole.
func butlerFiles(dedupTerms []string) []string {
	seen := make(map[string]bool, len(dedupTerms))
	var files []string
	for _, term := range dedupTerms {
		path := term
		if i := strings.Index(term, ":"); i >= 0 {
			path = term[:i]
		}
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files
}
