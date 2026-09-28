package butler

import (
	"fmt"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/settle"
)

// PromotionPolicy is the host-side auto-promotion gate for one Chore's
// findings (issue #3880, ADR 0056), moved verbatim from settle.PromotionPolicy
// (issue #3990) -- the Box's issue-intent payload is only ever an input to
// eligible, never the gate itself, so a Box cannot widen its own allow-list,
// raise its own file limit, or grant itself more of the day's promotion room.
type PromotionPolicy struct {
	// Classes is this Chore's host-side finding-class allow-list (from
	// BUTLER_CHORE_CLASSES). Nil/empty means no class is promotable -- an
	// unconfigured Chore reads as opted out, not "trust the Box".
	Classes []string
	// MaxFiles is the host limit on how many files a promoted finding may
	// touch. A finding naming zero files is never promotable regardless of
	// MaxFiles (scope unknown), and MaxFiles itself must be >=1 for anything
	// to promote.
	MaxFiles int
	// Room reports how many promotions remain today; evaluated at most once
	// per settle call (concurrent runs make a value fetched earlier stale by
	// the time this run would spend it). nil, or a func returning <=0, means
	// no room: promotion is off regardless of the other three gates.
	Room func() int
	// Label is the work kind's own configured dispatch label (LABEL,
	// Consumer-configurable) -- carried on a promoted finding alongside
	// "agent-butler-finding" so the work path picks it up (issue #3880).
	// Empty means unconfigured: never promote, rather than guess a name.
	Label string
}

// eligible reports whether f clears every promotion gate but room -- room is
// this call's shared, mutable per-sweep remaining counter, not p's Room field
// itself, so the settle step checks it separately alongside eligible.
// f.Concurrence is already sanitized (settle.parseIssueIntent), so eligible
// only needs to check it's non-blank.
func (p PromotionPolicy) eligible(f settle.Finding, files []string) bool {
	if p.Label == "" {
		return false
	}
	if f.Class == "" || !slices.Contains(p.Classes, f.Class) {
		return false
	}
	if len(files) < 1 || len(files) > p.MaxFiles {
		return false
	}
	return strings.TrimSpace(f.Concurrence) != ""
}

// promotionNote renders the visible note appended to a promoted finding's
// body, naming the class and quoting the reviewer's own words (issue #3880)
// rather than just asserting agreement -- a reader should be able to check
// the reviewer's claim, not just trust that it happened. f.Concurrence
// arrives already sanitized (settle.parseIssueIntent runs before a Finding
// ever reaches this package), so only oneLine's collapse is needed here to
// keep the reviewer's prose from breaking the note's single sentence.
func promotionNote(choreName string, f settle.Finding, policy PromotionPolicy, nFiles int) string {
	concurrence := oneLine(f.Concurrence)
	return fmt.Sprintf(
		"**Auto-promoted** to `%s` by the butler: class `%s` is on the `%s` Chore's allow-list, it touches %d file(s) (host limit %d), and the in-Box reviewer agreed: `%s`",
		policy.Label, f.Class, choreName, nFiles, policy.MaxFiles, concurrence,
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
