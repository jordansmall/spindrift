package butler

import (
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/settle"
)

// conventionalSubjectRE matches a Conventional Commits subject line
// (type[(scope)][!]: description); patchCommitSubject uses it to decide
// whether a finding's own title already reads as one.
var conventionalSubjectRE = regexp.MustCompile(`^[a-z]+(?:\([^)]*\))?!?: \S`)

// conventionalBangRE matches the breaking-change "!" in an already-
// conventional subject so patchCommitSubject can drop just the "!" (issue
// #4074): a Box-authored "feat!: ..." squash subject would otherwise drive an
// unintended release-please major bump.
var conventionalBangRE = regexp.MustCompile(`^([a-z]+(?:\([^)]*\))?)!(: )`)

// settled is what one settleRun.settle call reports back to Sweep: whether
// the done commit actually landed, and, only when it did, the counts Sweep's
// Outcome carries. Sweep never re-reads the Ledger to learn this: a lost race
// on the done commit and a crashed Box both leave nothing new to read back.
type settled struct {
	done                              bool
	filed, promoted, dropped, patched int
}

// settleRun is one Chore run's settle step: file each finding the Box
// reported, then write the Chore's done Ledger commit carrying the advanced
// lastSwept/cursor, the filed URLs, and the run's usage. No tracker label
// transition: the butler carries no tracker issue of its own.
type settleRun struct {
	it     forge.IssueTracker
	ledger ledger.Backend
	chore  string
	claim  ledger.Tip
	scope  chore.Scope
	now    func() time.Time
	room   chore.Room
	policy promotion

	out      io.Writer
	progress settle.Progress

	// labels are extra provenance labels every finding of this Chore wears
	// beside agent-butler-finding; settle's filing derives the matching
	// hidden-marker term from them.
	labels []string

	kindLabels kindLabels

	// patch backs the patch rung (ADR 0057, issue #4074); patch.forge's
	// presence gates it (Runner.run).
	patch patchRung

	// records is the stored digest of a records-scoped sweep; nil for a code
	// Chore.
	records *recordsSnapshot
}

// patchRung is what the patch rung needs (ADR 0057, issue #4074): tree
// commits a candidate's diff on the fresh base head, forge pushes it and
// opens the draft PR, and gate is the work merge gate each landed PR is
// handed to after the Chore's Done commit lands (issue #4076).
type patchRung struct {
	tree  Tree
	forge PatchForge
	// branchPrefix is BRANCH_PREFIX, the input to forge.AgentBranchName.
	branchPrefix string
	base         string
	gate         PatchGate
}

// kindLabels carries the butler kind row's finding and patch labels.
type kindLabels struct {
	finding, patch string
}

// patchLanding pairs one landed patch's finding issue number with its PR
// URL -- the two things settle's post-Finish gate calls need (issue #4076).
type patchLanding struct {
	issueNum, prURL string
}

// newSettleRun constructs a settleRun for one Chore run. claim is the Ledger
// tip Claim produced at the start of this run -- ledger.Finish's
// compare-and-swap parent, unless settle first reserves promotion slots
// (issue #3926), in which case the reservation commit takes over as parent.
// scope's Head/NextCursor become the done commit's lastSwept/cursor on
// success. room is computed once per Sweep and handed down, never re-walked
// here (issue #3994); room.Findings 0 means no cap. policy's zero value never
// promotes anything (issue #3880).
func newSettleRun(it forge.IssueTracker, backend ledger.Backend, choreName string, claim ledger.Tip, scope chore.Scope, now func() time.Time, room chore.Room, policy promotion, patch patchRung, labels kindLabels, out io.Writer) *settleRun {
	return &settleRun{out: out, progress: settle.StdoutProgress{}, kindLabels: labels, it: it, ledger: backend, chore: choreName, claim: claim, scope: scope, now: now, room: room, policy: policy, patch: patch}
}

// settle files result's findings, if any, then writes the Chore's done
// Ledger commit. A crashed run -- no outcome line, or a non-ready status --
// files nothing and writes no Ledger commit, so the claim stands and
// lastSwept/cursor stay put for the next run (ADR 0056). Rejected signal
// lines are warned about before either crash guard, so a crashed run's
// dropped lines are never silently lost (issue #3990).
func (s *settleRun) settle(d dispatch.Dispatcher, result dispatch.Result) settled {
	num := dispatchkey.Chore(s.chore).String()
	settle.RecordSettleWarnings(d, num, "", result)

	if !result.Resolved.Found {
		// A Pass that failed before its Box ran (a seed-mirror error, say)
		// carries the reason in Err; without it the note hides the cause.
		note := result.FailureNote()
		if note == "" {
			note = "no ready outcome line"
		}
		s.fail(d, num, note)
		return settled{}
	}
	o := result.Resolved.Outcome
	if o.Status != outcome.StatusReady {
		note := o.Note
		if note == "" {
			note = "status=" + o.Status
		}
		s.fail(d, num, note)
		return settled{}
	}

	var drops []ledger.Drop
	if s.records != nil {
		err := s.records.load()
		if err == nil {
			result.IssueIntents, drops, err = s.validateFindings(num, result.IssueIntents)
		}
		if err != nil {
			s.fail(d, num, err.Error())
			return settled{}
		}
	}

	finishParent := s.claim
	var promoted []string
	var landings []patchLanding
	// A Digest with a DedupTerms hook keys on the pattern a finding names. A
	// records Chore sets the cutoff from its Digest's cooldown knob, but only
	// its own findings expire on it; a code Chore keeps site keys, forever.
	// The cutoff must stay now-cooldown: a zero NotPlannedBefore never
	// expires, but a zero cooldown would expire every not-planned close.
	var dedup settle.DedupPolicy
	if s.records != nil {
		dedup = settle.DedupPolicy{Chore: s.chore, Terms: s.records.digest.DedupTerms, NotPlannedBefore: s.now().Add(-s.records.knobs.Cooldown)}
	}
	filing := settle.FileButlerFindings(s.progress, s.it, num, result, s.room.Findings, s.kindLabels.finding, dedup, func(kept []settle.Finding) func(settle.Finding) settle.Decoration {
		// An off policy reserves none of the day's shared promotion room.
		// room.Promotions is Sweep's snapshot, so a promotion landed by a
		// concurrent run mid-sweep is not seen (soft cap, chore.Room's doc).
		remaining := 0
		if s.policy.enabled {
			remaining = s.room.Promotions
		}
		// Unlike remaining, patchesLeft needs no guard: decide's patch gate
		// checks patchEnabled itself.
		patchesLeft := s.room.Patches

		// Reserve the slots this run intends to spend before filing anything
		// (issues #3926, #4111): if the done commit never lands, the
		// reservation still counts against DayTotals, so a lost Finish can
		// never let a Chore promote or patch past the day's budget. A patch
		// candidate that is also promotion-eligible holds a promotion slot
		// too (its fallback needs one), so a lost Finish over-counts
		// promotions, never under-counts either budget.
		eligible, patchCount := 0, 0
		for _, f := range kept {
			// Patch room zero: a patch candidate whose landing later fails
			// falls back to promote, so it still needs a promotion slot.
			if s.policy.decide(f, promoteOnlyRoom(remaining)).kind == promote {
				eligible++
			}
			if patchesLeft > 0 && s.policy.decide(f, chore.Room{Promotions: remaining, Patches: patchesLeft}).kind == patch {
				patchCount++
			}
		}
		n := min(remaining, eligible)
		p := min(patchesLeft, patchCount)
		remaining, patchesLeft = n, p
		if n > 0 || p > 0 {
			reserved, err := ledger.Reserve(s.ledger, s.chore, s.claim, ledger.Reservation{Promotions: n, Patches: p}, s.now())
			if err != nil {
				fmt.Fprintf(s.out, "    #%s  status=reserve-failed  !! %v\n", num, err)
				remaining, patchesLeft = 0, 0
			} else {
				finishParent = reserved
			}
		}

		decorate := func(f settle.Finding) settle.Decoration {
			backlink := butlerBacklink(s.chore, f)
			dec := s.policy.decide(f, chore.Room{Promotions: remaining, Patches: patchesLeft})
			if dec.patchSkip != "" {
				fmt.Fprintf(s.out, "    #%s  status=patch-skipped  note=%s\n", num, dec.patchSkip)
			}
			if dec.kind == patch {
				subject := patchCommitSubject(f.Title)
				pc, err := s.patch.tree.CommitPatch(s.patch.base, ScannedCommit(s.scope.Head), f.Patch, subject)
				if err != nil {
					// A stale/rebased diff: file exactly as if it had no
					// Patch at all, re-deciding with patch room zeroed so
					// decide can only promote or skip from here (issue #4074).
					fmt.Fprintf(s.out, "    #%s  status=patch-apply-failed  !! %v\n", num, err)
					dec = s.policy.decide(f, promoteOnlyRoom(remaining))
				} else {
					return settle.Decoration{
						Backlink:    backlink,
						ExtraLabels: s.extraLabels(s.kindLabels.patch),
						// Spend the slot, push, and open the PR only after
						// PostIssue succeeds: a failed post must not push a
						// branch or open a PR for an issue that never landed.
						OnFiled: func(url string) {
							// Either way the issue was filed wearing the patch
							// label, so spend the slot; with no usable issue
							// number there is no PR to open or issue to close.
							if url == "" {
								fmt.Fprintf(s.out, "    #%s  status=patch-failed  !! filed, but the tracker returned an unusable URL: no issue number to open the patch PR against\n", num)
								patchesLeft--
								return
							}
							issueNum, err := issueNumberFromURL(url)
							if err != nil {
								fmt.Fprintf(s.out, "    #%s  status=patch-failed  !! %v\n", num, err)
								patchesLeft--
								return
							}
							head := forge.AgentBranchName(s.patch.branchPrefix, issueNum)
							prURL, err := s.landPatch(num, pc, subject, head, f, url, issueNum)
							if err != nil {
								fmt.Fprintf(s.out, "    #%s  status=patch-failed  !! %v\n", num, err)
								if s.fallBackToPromote(num, issueNum, f, remaining) {
									remaining--
									promoted = append(promoted, url)
								}
								return
							}
							patchesLeft--
							landings = append(landings, patchLanding{issueNum: issueNum, prURL: prURL})
						},
					}
				}
			}
			if dec.kind != promote {
				// Logged from OnFiled so only a finding that actually filed
				// reports why it was not promoted.
				return settle.Decoration{Backlink: backlink, ExtraLabels: s.extraLabels(), OnFiled: func(string) { s.logPromoteSkip(num, dec) }}
			}
			note := promotionNote(s.chore, f, s.policy, dec.files)
			// Spend the slot only in OnFiled, after PostIssue succeeds, so a
			// failed post frees it back to the rest of the sweep.
			return settle.Decoration{Backlink: backlink + "\n\n" + note, ExtraLabels: s.extraLabels(dec.labels...), OnFiled: func(url string) {
				remaining--
				if url == "" {
					url = unusableFiledRef
				}
				promoted = append(promoted, url)
			}}
		}
		if s.records == nil || s.records.digest.Evidence == nil {
			return decorate
		}
		rendered, evidence := s.records.rendered, s.records.digest.Evidence
		// Appended last, after any promotion note, so the visible body ends
		// with the host-rendered rows rather than the model's transcription.
		return func(f settle.Finding) settle.Decoration {
			dec := decorate(f)
			if ev := evidence(rendered, f.Cites); ev != "" {
				dec.Backlink += "\n\n" + strings.TrimRight(ev, "\n")
			}
			return dec
		}
	})

	// One line per sweep, not per finding: with promotion off every finding
	// would otherwise repeat the same reason. Gated on a finding actually
	// filing, so a sweep whose findings were all deduped or failed stays quiet.
	filedCount := len(filing.Filed) + filing.Unusable
	if !s.policy.enabled && filedCount > 0 {
		fmt.Fprintf(s.out, "    #%s  status=promotion-off  note=%s\n", num, promotionOffNote)
	}

	if filing.Unusable > 0 {
		fmt.Fprintf(s.out, "    #%s  status=filed-unusable-url  note=%d finding(s) were filed but the tracker returned an unusable URL; not refiled\n", num, filing.Unusable)
	}

	// Every finding failing to file leaves the claim standing like a crash:
	// the findings are re-found next run, and the non-zero exit feeds the
	// daemon breaker instead of spending a Box per sweep forever. Not when a
	// finding was filed with an unusable URL: it exists on the tracker, so
	// re-finding and refiling it next run is what must not happen.
	if filedCount == 0 && filing.Failed > 0 {
		s.fail(d, num, fmt.Sprintf("all %d finding(s) failed to file -- see the logged issue-intent filing errors; a missing provenance label is one cause (spindrift doctor --butler)", filing.Failed))
		return settled{}
	}

	// Validation drops count with the per-sweep cap's so the Ledger's Dropped
	// total matches the note and Drops lists only the validation drops.
	dropped := filing.Dropped + len(drops)

	patched := make([]string, len(landings))
	for i, l := range landings {
		patched[i] = l.prURL
	}
	state := ledger.State{
		LastSwept: s.scope.Head,
		Cursor:    s.scope.NextCursor,
		Filed:     append(slices.Clone(filing.Filed), slices.Repeat([]string{unusableFiledRef}, filing.Unusable)...),
		Promoted:  promoted,
		Patched:   patched,
		Usage:     d.CumulativeUsage(),
		Dropped:   dropped,
		Drops:     drops,
	}
	if s.records != nil {
		state.Snapshot = &s.records.ref
	}
	if _, err := ledger.Finish(s.ledger, s.chore, finishParent, state, s.now()); err != nil {
		fmt.Fprintf(s.out, "    #%s  status=ledger-finish-failed  !! %v\n", num, err)
		s.settled(d, dispatchrecord.ReasonLedgerFinishFailed, fmt.Sprintf("ledger finish failed: %v", err))
		// Still gate the already-open draft PRs, or they orphan (issue
		// #4118). #4076's ordering has nothing left to protect: a lost CAS
		// means a rival owns the Chore, and any other error leaves this claim
		// standing until stale-claim takeover anyway.
		s.adoptLandings(landings)
		return settled{}
	}

	note := fmt.Sprintf("%d filed", filedCount)
	if len(promoted) > 0 {
		note = fmt.Sprintf("%d filed, %d promoted", filedCount, len(promoted))
	}
	if len(patched) > 0 {
		note = fmt.Sprintf("%s, %d patched", note, len(patched))
	}
	if dropped > 0 {
		note = fmt.Sprintf("%s, %d dropped", note, dropped)
	}
	s.settled(d, dispatchrecord.ReasonFindingsFiled, note)
	fmt.Fprintf(s.out, "    #%s  status=%s  note=%s\n", num, o.Status, note)

	// Gate the patches only after the Done commit landed: a crash while
	// polling CI must never leave the Chore's claim standing (issue #4076).
	s.adoptLandings(landings)

	return settled{done: true, filed: filedCount, promoted: len(promoted), dropped: dropped, patched: len(patched)}
}

// adoptLandings hands each landed patch's draft PR to the work merge gate.
func (s *settleRun) adoptLandings(landings []patchLanding) {
	for _, l := range landings {
		s.patch.gate.SettlePatch(dispatchkey.Chore(s.chore), l.issueNum, l.prURL)
	}
}

// landPatch pushes pc's committed branch to head and opens a draft PR for
// it, returning the PR's URL. Called only from OnFiled, after the finding
// issue itself has actually filed (issueNum), so the PR body can close it. A
// CreateDraftPR error is ambiguous -- a timeout or 5xx can arrive after the
// server already created the PR (issue #4112) -- so an open PR found for head
// afterwards counts as landed, and only a confirmed-absent one gets the
// pushed branch deleted before the error falls back to promote.
func (s *settleRun) landPatch(chorenum string, pc PatchCommit, subject, head string, f settle.Finding, findingURL, issueNum string) (string, error) {
	if err := s.patch.forge.PushBranch(pc.Dir, pc.Ref, head, s.patch.base); err != nil {
		return "", err
	}
	body := patchPRBody(s.chore, f, findingURL, issueNum)
	prURL, _, createErr := s.patch.forge.CreateDraftPR(subject, body, s.patch.base, head)
	if createErr == nil {
		return prURL, nil
	}
	pr, found, lookupErr := s.patch.forge.OpenPRForBranch(head)
	if lookupErr != nil {
		// Branch kept: a PR this lookup could not see may be live, and
		// deleting its head branch would close it.
		fmt.Fprintf(s.out, "    #%s  status=patch-pr-lookup-failed  !! %v\n", chorenum, lookupErr)
		return "", createErr
	}
	if found {
		// head is agent/issue-N for the issue just filed, so no earlier run
		// can own a PR on it -- adopting whatever is open there is safe.
		fmt.Fprintf(s.out, "    #%s  status=patch-pr-adopted  !! %v\n", chorenum, createErr)
		return pr.URL, nil
	}
	// Residual race: if the timed-out create lands on the server only after
	// this lookup said "not found", the delete below closes it and the
	// finding still gets promoted -- the same outcome as before #4112.
	if delErr := s.patch.forge.DeleteBranch(head, s.patch.base); delErr != nil {
		fmt.Fprintf(s.out, "    #%s  status=patch-branch-cleanup-failed  !! %v\n", chorenum, delErr)
	}
	return "", createErr
}

// fallBackToPromote is landPatch's failure path (issue #4074): a finding
// already filed with the patch label is judged as decidePromote would judge
// it with no Patch, labelled through s.patch.forge's IssueLabeler since the
// issue already exists (butlerPatchForge refuses to turn the rung on without
// one). Reports whether it promoted; the caller spends the shared promotion
// state on true. A failed AddLabels is logged, never fatal.
func (s *settleRun) fallBackToPromote(chorenum, issueNum string, f settle.Finding, remaining int) bool {
	fb := s.policy.decide(f, promoteOnlyRoom(remaining))
	if fb.kind != promote {
		s.logPromoteSkip(chorenum, fb)
		return false
	}
	if err := s.patch.forge.AddLabels(issueNum, fb.labels); err != nil {
		fmt.Fprintf(s.out, "    #%s  status=patch-fallback-label-failed  !! %v\n", chorenum, err)
		return false
	}
	if err := s.it.Comment(issueNum, promotionNote(s.chore, f, s.policy, fb.files)); err != nil {
		fmt.Fprintf(s.out, "    #%s  status=patch-fallback-comment-failed  !! %v\n", chorenum, err)
	}
	return true
}

// logPromoteSkip prints the first gate that kept a filed finding from being
// promoted. Promotion being off is reported once per sweep instead, so it is
// never repeated per finding.
func (s *settleRun) logPromoteSkip(num string, dec decision) {
	if !s.policy.enabled {
		return
	}
	fmt.Fprintf(s.out, "    #%s  status=promote-skipped  note=%s\n", num, dec.reason)
}

// promoteOnlyRoom zeroes the patch half of a chore.Room so a finding that has
// left the patch path is never offered patch room a second time (issue #4074).
func promoteOnlyRoom(promotions int) chore.Room {
	return chore.Room{Promotions: promotions}
}

// issueNumberFromURL returns url's trailing path segment, a filed issue's
// number, for forge.AgentBranchName and IssueLabeler.AddLabels, neither of
// which takes a URL. It errors when that segment is not a positive decimal
// number: the local tracker's PostIssue returns "local:" + slug, which must
// never become a branch name or a Closes reference (issue #4074).
func issueNumberFromURL(url string) (string, error) {
	tail := url[strings.LastIndex(url, "/")+1:]
	if n, err := strconv.Atoi(tail); err != nil || n <= 0 {
		return "", fmt.Errorf("issue url %q does not end in an issue number", url)
	}
	return tail, nil
}

// patchSubjectMaxLen is the /commit skill's own subject ceiling, applied
// here too since patchCommitSubject's result also becomes the draft PR/
// squash title (issue #4074).
const patchSubjectMaxLen = 72

// unusableFiledRef stands in for the URL of a finding the tracker filed but
// returned an unusable URL for. Fixed text, never the Connector's own URL,
// which is untrusted; the ledger counts list entries.
const unusableFiledRef = "(filed; the tracker returned an unusable URL)"

// patchCommitSubject derives a Conventional Commits subject for a patch's
// commit and draft PR title from the finding's own title, prefixing it when
// it does not already read as one. A breaking-change "!" is always stripped:
// the butler never decides a finding is breaking (issue #4074).
func patchCommitSubject(title string) string {
	subject := oneLine(title)
	if !conventionalSubjectRE.MatchString(subject) {
		subject = "chore(butler): " + subject
	}
	subject = conventionalBangRE.ReplaceAllString(subject, "$1$2")
	return truncateSubject(subject, patchSubjectMaxLen)
}

// truncateSubject caps s at limit runes, replacing the last rune with "…"
// when it had to cut so the result reads as visibly truncated.
func truncateSubject(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

// patchedNote is promotionNote's patch-rung sibling (ADR 0057): quotes the
// reviewer's own concurrence the same way, naming the patch allow-list
// rather than the promotion one.
func patchedNote(choreName string, f settle.Finding) string {
	return fmt.Sprintf(
		"**Patched** by the butler: class `%s` is on the `%s` Chore's patch allow-list, and the in-Box reviewer agreed: `%s`",
		f.Class, choreName, oneLine(f.Concurrence),
	)
}

// patchPRBody renders a landed patch's draft PR body: the patched note, the
// finding issue's own URL for context, and Closes #N so merging the PR
// closes the finding it patches.
func patchPRBody(choreName string, f settle.Finding, findingURL, issueNum string) string {
	return fmt.Sprintf("%s\n\n%s\n\nCloses #%s", patchedNote(choreName, f), findingURL, issueNum)
}

// fail prints a status=failed line and reports the run failed. It writes no
// Ledger commit, so the claim stands and lastSwept/cursor stay put for the
// next run to resume from.
func (s *settleRun) fail(d dispatch.Dispatcher, num, note string) {
	s.settled(d, dispatchrecord.ReasonChoreFailed, note)
	fmt.Fprintf(s.out, "    #%s  status=failed  note=%s\n", num, note)
}

// settled is the Chore Dispatch's one terminal settle: it appends
// dispatch_settled to the Dispatch's primary Pass log and reports it.
func (s *settleRun) settled(d dispatch.Dispatcher, reason, note string) {
	settle.SettledBy(d, dispatchkey.Chore(s.chore), settle.ForReason(reason), note)
}

// extraLabels is the Decoration label set for a finding: the Chore's own
// provenance labels plus whatever this finding's landing path adds.
func (s *settleRun) extraLabels(more ...string) []string {
	return append(slices.Clone(s.labels), more...)
}

// dropUnknownCite is the Ledger drop reason for a finding that cites nothing
// or an anchor the stored digest does not hold.
const dropUnknownCite = "unknown-cite"

// citeDrop returns dropUnknownCite unless cites is non-empty and every one is
// an anchor: a finding must trace back to rows the host served.
func citeDrop(cites []string, anchors map[string]bool) string {
	if len(cites) == 0 {
		return dropUnknownCite
	}
	for _, c := range cites {
		if !anchors[c] {
			return dropUnknownCite
		}
	}
	return ""
}

// validateFindings drops every finding in raw that cites nothing, cites an
// anchor outside the stored digest, or the Digest's Validate hook refuses,
// returning the rest and a Drop for each. A payload that filing would reject
// (settle.ParseIssueIntent) passes through for filing to skip, so its
// unvetted fields never reach a Drop. A tracked-files error fails the settle
// so the next sweep retries.
func (s *settleRun) validateFindings(num string, raw []string) ([]string, []ledger.Drop, error) {
	var tracked map[string]bool
	trackedFiles := func() (map[string]bool, error) {
		if tracked != nil {
			return tracked, nil
		}
		files, err := s.records.tree.TrackedFiles(s.scope.Head)
		if err != nil {
			return nil, fmt.Errorf("list tracked files at %s: %w", s.scope.Head, err)
		}
		tracked = make(map[string]bool, len(files))
		for _, f := range files {
			tracked[f] = true
		}
		return tracked, nil
	}
	anchors := s.records.digest.Anchors(s.records.rendered)
	var kept []string
	var drops []ledger.Drop
	for _, r := range raw {
		in, ok := settle.ParseIssueIntent(r)
		if !ok {
			kept = append(kept, r)
			continue
		}
		reason := citeDrop(in.Cites, anchors)
		if reason == "" && s.records.digest.Validate != nil {
			var err error
			reason, err = s.records.digest.Validate(in, s.records.rendered, s.records.knobs.Limits, trackedFiles)
			if err != nil {
				return nil, nil, err
			}
		}
		if reason == "" {
			kept = append(kept, r)
			continue
		}
		fmt.Fprintf(s.out, "    #%s  dropped %s finding %q: %s\n", num, s.chore, in.Title, reason)
		drops = append(drops, ledger.Drop{Title: in.Title, Reason: reason})
	}
	return kept, drops, nil
}
