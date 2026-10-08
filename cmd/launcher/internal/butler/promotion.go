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
	// patchEnabled is the patch rung's own on/off derivation, mirroring
	// enabled -- see newPromotion. A finding never patches while this is
	// false, regardless of anything else about it.
	patchEnabled bool
	// patchClasses is this Chore's host-side patch-class allow-list (from
	// BUTLER_PATCH_CLASSES), checked independently of classes: decide's
	// patch branch gates on patchClasses alone (ADR 0057) -- classes plays
	// no part in it; classes only gates the ordinary promote branch.
	patchClasses []string
	// patchPathAllow/patchPathDeny are BUTLER_PATCH_PATHS' comma-separated
	// glob list split into its plain and "!"-prefixed entries (ADR 0057): a
	// diff path is admitted only when it matches at least one allow entry
	// and no deny entry -- see patchPathAdmitted.
	patchPathAllow, patchPathDeny []string
	// patchMaxFiles/patchMaxLines are BUTLER_PATCH_MAX_FILES/
	// BUTLER_PATCH_MAX_LINES: the host limits on how many files a patch
	// candidate's diff may touch and how many lines (added plus removed) it
	// may change in total, checked independently of maxFiles/the promote
	// branch's own file count.
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

// newPromotion builds a promotion from a Chore's allow-listed classes, the
// host's per-finding file limit, its per-day promotion budget, the
// Consumer's work dispatch label, and the patch rung's own patchPolicy.
// enabled requires all three of a positive per-day budget, a configured
// label, and at least one allow-listed class: any one of them unset reads as
// "promotion off", never as "trust whatever the unset one happens to be".
// patchEnabled mirrors that same rule for the patch rung, independently, off
// patch.classes and patch.perDay alone; the bounds gate individual findings
// in patchBlocker, not this on/off derivation.
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
// its plain (allow) and "!"-prefixed (deny) entries, trimming whitespace
// around each and dropping blank ones -- the schema doc's own grammar for
// the field (issue #4075).
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

// decisionKind names what decide chose for one finding: skip it, promote it
// to a labeled work issue, or land it as a patch PR (ADR 0057) -- patch is
// evaluated before promote, so a finding eligible for both patches rather
// than promotes.
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
	// patchSkip is the first patch gate patchBlocker found failing, only set
	// when the rung was on and f.Patch was non-blank (so a finding that was
	// never patch-eligible to begin with produces no skip noise) -- settle
	// logs it verbatim (ADR 0057, issue #4075).
	patchSkip string
}

// decide is the promotion gate for one finding. Once the rung is on and
// f.Patch is non-blank, the patch gate (patchBlocker) is checked first and
// in full: only when every one of its gates holds does decide return patch,
// and it never touches promote's own gates or room to do so (patching needs
// no promotion.enabled and no promotion file limit). Any single patch gate
// failing falls straight through to decidePromote, so a finding whose Patch
// happens to be unusable is judged exactly as if it had none, with only
// patchSkip added to name the failing gate. room is this sweep's shared
// budget, not a field on p -- the settle step tracks it across findings in
// one sweep, spending it as findings promote or patch.
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

// patchBlocker runs the patch rung's own gate chain, in order: class on the
// patch allow-list, the diff parses and every file is a plain modification
// (no add, delete, rename, mode change, or binary content), the file cap,
// the line cap, every path admitted by patchPaths, every path among the
// finding's own site-key paths (butlerFiles), reviewer concurrence, then
// patch room. Returns the first gate's reason, one short line naming it, or
// "" once every gate clears. Called only from decide, which has already
// confirmed the rung is on and f.Patch is non-blank -- neither check is
// repeated or named here.
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
	site := butlerFiles(f.DedupTerms)
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

// patchPathAdmitted reports whether path clears BUTLER_PATCH_PATHS: it must
// match at least one plain (allow) entry and no "!"-prefixed (deny) entry.
// An empty allow list (BUTLER_PATCH_PATHS unset or all-deny) admits nothing.
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

// decidePromote is the promote/skip gate, checked in order: on/off, the
// trust gate (allow-listed class), the quality filters (file count,
// reviewer concurrence), then the shared per-sweep promotion room. Each
// gate below is its own predicate method so it stays separately testable.
// room is this call's remaining promotion budget.
func (p promotion) decidePromote(f settle.Finding, room int) decision {
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

// patchClassListed is the patch rung's own trust gate, mirroring allowListed
// but checked against p.patchClasses (BUTLER_PATCH_CLASSES) rather than
// p.classes (ADR 0057).
func (p promotion) patchClassListed(f settle.Finding) bool {
	return f.Class != "" && slices.Contains(p.patchClasses, f.Class)
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
// promoted), and which files (per butlerFiles: the path before the first ':'
// in each usable dedup term, deduped, order kept) it concerns. The Files
// sentence is omitted entirely when terms yields no paths, rather than
// printing "Files: ".
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
// order. A term with no ':' is taken whole. A term settle's dedup key set
// would drop, or whose path names no site (e.g. "-:Foo"), contributes no
// file, so junk terms never satisfy decide's file-count floor (issue #4036).
// The path stays raw: the normalized key folds case and separators.
func butlerFiles(dedupTerms []string) []string {
	seen := make(map[string]bool, len(dedupTerms))
	var files []string
	for _, term := range dedupTerms {
		path, _, hasSymbol := strings.Cut(term, ":")
		if seen[path] || !settle.UsableDedupTerm(term) || (hasSymbol && !settle.UsableDedupTerm(path)) {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files
}
