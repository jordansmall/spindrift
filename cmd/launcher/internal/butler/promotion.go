package butler

import (
	"fmt"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/glob"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/signalwire"
)

// promotion is the host-side auto-promotion gate for one Chore's findings
// (issue #3880, ADR 0056, issue #3993). classes, the host-side allow-list
// (BUTLER_CHORE_CLASSES), is the only trust gate; reviewer concurrence and
// the file count are quality filters on an already-trusted class. A Box's
// issue-intent payload is only ever an input to decide, so a Box cannot
// widen its own allow-list, raise its own file limit, or grant itself more
// of the day's promotion room.
type promotion struct {
	enabled bool
	// Empty classes means no class is promotable: an unconfigured Chore reads
	// as opted out, not "trust the Box".
	classes []string
	// A finding naming zero files is never promotable regardless of maxFiles
	// (scope unknown).
	maxFiles int
	// label is the work kind's dispatch label (LABEL), carried on a promoted
	// finding so the work path picks it up (issue #3880).
	label        string
	patchEnabled bool
	// patchClasses (BUTLER_PATCH_CLASSES) alone gates the patch branch (ADR
	// 0057); classes only gates the promote branch.
	patchClasses                  []string
	patchPathAllow, patchPathDeny []string
	// patchMaxLines counts added plus removed lines across the whole diff.
	patchMaxFiles, patchMaxLines int
}

// patchPolicy is the patch rung's own inputs to newPromotion (ADR 0057):
// BUTLER_PATCH_CLASSES, BUTLER_MAX_PATCHES_PER_DAY, BUTLER_PATCH_PATHS,
// BUTLER_PATCH_MAX_FILES and BUTLER_PATCH_MAX_LINES.
type patchPolicy struct {
	classes  []string
	perDay   int
	paths    string
	maxFiles int
	maxLines int
}

// promotionOffNote is the sweep-level reason settle logs when a promotion is
// not enabled; keep it in step with the enabled predicate in newPromotion.
const promotionOffNote = "promotion off for this Chore (needs a daily budget, a label, and a class allow-list)"

// newPromotion builds a promotion. enabled requires a positive per-day
// budget, a label, and at least one allow-listed class: any one unset reads
// as "promotion off", never as "trust whatever the unset one happens to be".
// patchEnabled applies the same rule to the patch rung, off patch.classes and
// patch.perDay alone; the bounds gate individual findings in patchBlocker.
func newPromotion(classes []string, maxFiles, perDay int, label string, patch patchPolicy) promotion {
	allow, deny := parsePatchPaths(patch.paths)
	return promotion{
		enabled:        perDay > 0 && label != "" && len(classes) > 0,
		classes:        classes,
		maxFiles:       maxFiles,
		label:          label,
		patchEnabled:   patch.perDay > 0 && len(patch.classes) > 0,
		patchClasses:   patch.classes,
		patchPathAllow: allow,
		patchPathDeny:  deny,
		patchMaxFiles:  patch.maxFiles,
		patchMaxLines:  patch.maxLines,
	}
}

// parsePatchPaths splits BUTLER_PATCH_PATHS' comma-separated glob list into
// its plain (allow) and "!"-prefixed (deny) entries (issue #4075).
func parsePatchPaths(paths string) (allow, deny []string) {
	for _, entry := range strings.Split(paths, ",") {
		entry = strings.TrimSpace(entry)
		switch {
		case entry == "":
			continue
		case strings.HasPrefix(entry, "!"):
			deny = append(deny, strings.TrimPrefix(entry, "!"))
		default:
			allow = append(allow, entry)
		}
	}
	return allow, deny
}

// decisionKind names what decide chose for one finding (ADR 0057). Patch is
// evaluated before promote, so a finding eligible for both patches.
type decisionKind int

const (
	skip decisionKind = iota
	promote
	patch
)

// decision is decide's verdict on one finding.
type decision struct {
	kind   decisionKind
	labels []string // only set when kind is promote
	reason string   // short; settle logs a skip's reason, except "promotion off", which it reports once per sweep
	files  int      // file count off the finding's dedup terms; only set when kind is promote
	// patchSkip is the first patch gate patchBlocker found failing, set only
	// when the rung was on and f.Patch was non-blank so a never-eligible
	// finding produces no skip noise (ADR 0057, issue #4075).
	patchSkip string
}

// decide is the promotion gate for one finding. With the rung on and f.Patch
// non-blank, patchBlocker is checked first and in full; patching needs no
// promotion.enabled and no promotion file limit. Any patch gate failing falls
// through to decidePromote, so an unusable Patch is judged as if it had none,
// with patchSkip naming the failing gate. room is the sweep's shared budget,
// which the settle step spends across findings.
func (p promotion) decide(f settle.Finding, room chore.Room) decision {
	if p.patchEnabled && f.Patch != "" {
		blocker := p.patchBlocker(f, room)
		if blocker == "" {
			return decision{kind: patch, reason: "patched"}
		}
		dec := p.decidePromote(f, room.Promotions)
		dec.patchSkip = blocker
		return dec
	}
	return p.decidePromote(f, room.Promotions)
}

// patchBlocker runs the patch rung's gate chain and returns the first failing
// gate's reason, or "" once every gate clears. Every diff file must be a
// plain modification (no add, delete, rename, mode change, or binary
// content). decide has already confirmed the rung is on and f.Patch is
// non-blank.
func (p promotion) patchBlocker(f settle.Finding, room chore.Room) string {
	if !p.patchClassListed(f) {
		return "class not on patch allow-list"
	}
	files, err := signalwire.ParseUnifiedDiff(f.Patch)
	if err != nil {
		return "diff does not parse: " + oneLine(err.Error())
	}
	for _, df := range files {
		if df.Change != "" {
			return fmt.Sprintf("diff is not modification-only: %s %s", oneLine(df.Path), df.Change)
		}
	}
	paths := make(map[string]struct{}, len(files))
	for _, df := range files {
		paths[df.Path] = struct{}{}
	}
	if len(paths) > p.patchMaxFiles {
		return fmt.Sprintf("diff touches %d files, over patch file cap %d", len(paths), p.patchMaxFiles)
	}
	lines := 0
	for _, df := range files {
		lines += df.Added + df.Removed
	}
	if lines > p.patchMaxLines {
		return fmt.Sprintf("diff changes %d lines, over patch line cap %d", lines, p.patchMaxLines)
	}
	for _, df := range files {
		if !p.patchPathAdmitted(df.Path) {
			return fmt.Sprintf("%s outside patch paths", oneLine(df.Path))
		}
	}
	site := settle.DedupFiles(f.DedupTerms)
	for _, df := range files {
		if !slices.Contains(site, df.Path) {
			return fmt.Sprintf("%s not among the finding's site keys", oneLine(df.Path))
		}
	}
	if !p.concurred(f) {
		return "no reviewer concurrence"
	}
	if room.Patches <= 0 {
		return "no patch room"
	}
	return ""
}

// patchPathAdmitted reports whether path matches at least one allow entry and
// no deny entry. An empty allow list (BUTLER_PATCH_PATHS unset or all-deny)
// admits nothing.
func (p promotion) patchPathAdmitted(path string) bool {
	matched := false
	for _, pat := range p.patchPathAllow {
		if glob.Match(pat, path) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, pat := range p.patchPathDeny {
		if glob.Match(pat, path) {
			return false
		}
	}
	return true
}

// decidePromote is the promote/skip gate: on/off, the trust gate, the quality
// filters, then the remaining promotion room.
func (p promotion) decidePromote(f settle.Finding, room int) decision {
	if !p.enabled {
		return decision{kind: skip, reason: "promotion off"}
	}
	if !p.allowListed(f) {
		return decision{kind: skip, reason: "class not allow-listed"}
	}
	files := settle.DedupFiles(f.DedupTerms)
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

// patchClassListed is the patch rung's trust gate (ADR 0057).
func (p promotion) patchClassListed(f settle.Finding) bool {
	return f.Class != "" && slices.Contains(p.patchClasses, f.Class)
}

// withinFileLimit: files comes from the Box's own dedup terms, so a finding
// naming zero files (scope unknown) or more than the host's maxFiles never
// promotes.
func (p promotion) withinFileLimit(files []string) bool {
	return len(files) >= 1 && len(files) <= p.maxFiles
}

// concurred: f.Concurrence is already sanitized by settle's intent parsing.
func (p promotion) concurred(f settle.Finding) bool {
	return strings.TrimSpace(f.Concurrence) != ""
}

// promotionNote renders the visible note appended to a promoted finding's
// body, quoting the reviewer's own words (issue #3880) so a reader can check
// the claim rather than trust that agreement happened.
func promotionNote(choreName string, f settle.Finding, policy promotion, nFiles int) string {
	concurrence := oneLine(f.Concurrence)
	return fmt.Sprintf(
		"**Auto-promoted** to `%s` by the butler: class `%s` is on the `%s` Chore's allow-list, it touches %d file(s) (host limit %d), and the in-Box reviewer agreed: `%s`",
		policy.label, f.Class, choreName, nFiles, policy.maxFiles, concurrence,
	)
}

// oneLine collapses s's whitespace to single spaces: Box-supplied prose
// (issue #3880) must render as one line in host-authored note text.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// butlerBacklink renders the per-intent backlink appended to a filed
// finding's body: which Chore filed it, the Box-claimed class if any
// (issues #3870, #3880), and which files it concerns.
func butlerBacklink(choreName string, f settle.Finding) string {
	lead := fmt.Sprintf("Filed by the butler's `%s` Chore.", choreName)
	if f.Class != "" {
		lead += fmt.Sprintf(" Class: `%s`.", f.Class)
	}
	files := settle.DedupFiles(f.DedupTerms)
	if len(files) == 0 {
		return lead
	}
	quoted := make([]string, len(files))
	for i, path := range files {
		quoted[i] = "`" + path + "`"
	}
	return fmt.Sprintf("%s Files: %s.", lead, strings.Join(quoted, ", "))
}
