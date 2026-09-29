package butler

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/settle"
)

// conventionalSubjectRE matches a Conventional Commits subject line
// (type[(scope)][!]: description); patchCommitSubject uses it to decide
// whether a finding's own title already reads as one.
var conventionalSubjectRE = regexp.MustCompile(`^[a-z]+(?:\([^)]*\))?!?: \S`)

// conventionalBangRE matches the breaking-change "!" in an already-
// conventional subject, capturing the type[(scope)] on one side and the
// ": " on the other so patchCommitSubject can drop just the "!" (issue
// #4074): a Box-authored "feat!: ..." title landing verbatim as a squash
// subject would otherwise drive an unintended release-please major bump.
var conventionalBangRE = regexp.MustCompile(`^([a-z]+(?:\([^)]*\))?)!(: )`)

// settled is what one settleRun.settle call reports back to Sweep: whether
// the done commit actually landed, and, only when it did, the counts Sweep's
// Outcome carries. Sweep learns "did it land" from this return value alone --
// it never re-reads the Ledger after settle to find out, since a lost race
// on the done commit and a crashed Box (no ready outcome at all) both leave
// nothing new to read back.
type settled struct {
	done                              bool
	filed, promoted, dropped, patched int
}

// settleRun is one Chore run's settle step: file each finding the Box reported, then write the Chore's
// done Ledger commit carrying the advanced lastSwept/cursor, the filed URLs,
// and the run's usage. No CI watch, no merge, and no tracker label
// transition -- the butler carries no tracker issue of its own, only a
// Ledger Chore.
type settleRun struct {
	it     forge.IssueTracker
	ledger ledger.Backend
	chore  string
	claim  ledger.Tip
	scope  chore.Scope
	now    func() time.Time
	room   chore.Room
	policy promotion

	// patch backs the patch rung (ADR 0057, issue #4074) -- see Runner.run
	// for how patch.forge's presence gates it.
	patch patchRung
}

// patchRung is what the patch rung needs (ADR 0057, issue #4074/#4076): tree
// commits a candidate's diff on top of the fresh base head, forge pushes the
// committed branch and opens the draft PR (and, on a failed push/PR-create,
// labels the already-filed issue for the fallback promote), base is the
// branch both target, and gate is the work merge gate each landed PR is
// handed to after the Chore's Done commit lands (issue #4076).
type patchRung struct {
	tree  Tree
	forge PatchForge
	base  string
	gate  PatchGate
}

// patchLanding pairs one landed patch's finding issue number with its PR
// URL -- the two things settle's post-Finish gate calls need (issue #4076).
type patchLanding struct {
	issueNum, prURL string
}

// newSettleRun constructs a settleRun for one Chore run. claim is the Ledger
// tip Claim produced at the start of this run -- ledger.Finish's compare-and-
// swap parent, unless settle first reserves promotion slots (issue #3926), in
// which case the reservation commit takes over as parent; scope is the run's
// computed Scope (internal/chore.NextScope), whose Head/NextCursor become the
// done commit's lastSwept/cursor on success. room is Sweep's chore.Room:
// room.Findings caps how many well-formed findings settle will file this
// sweep (0 means no cap), and room.Promotions seeds how many may
// auto-promote, only when policy.enabled -- room is computed once per Sweep
// and handed down, never re-walked here (issue #3994). policy is the
// host-side auto-promotion gate (issue #3880); its zero value never
// promotes anything. patch backs the patch rung (ADR 0057); see Runner.run
// for how patch.forge's presence gates it.
func newSettleRun(it forge.IssueTracker, backend ledger.Backend, choreName string, claim ledger.Tip, scope chore.Scope, now func() time.Time, room chore.Room, policy promotion, patch patchRung) *settleRun {
	return &settleRun{it: it, ledger: backend, chore: choreName, claim: claim, scope: scope, now: now, room: room, policy: policy, patch: patch}
}

// settle files result's findings, if any, then writes the Chore's done
// Ledger commit. A crashed run -- no outcome line, or an outcome line whose
// status isn't "ready" (e.g. blocked) -- files nothing and writes no Ledger
// commit at all, so the claim stands and lastSwept/cursor stay put for the
// next run to resume from (ADR 0056). Rejected signal lines are warned about
// first, before either crash guard, so a crashed run's dropped comment/
// issue-intent/pr-intent lines are never silently lost (issue #3990).
func (s *settleRun) settle(d dispatch.Dispatcher, result dispatch.Result) settled {
	num := dispatchkey.Chore(s.chore).String()
	settle.LogRejectedSignals(num, result)

	if !result.Resolved.Found {
		s.fail(num, "no ready outcome line")
		return settled{}
	}
	o := result.Resolved.Outcome
	if o.Status != outcome.StatusReady {
		note := o.Note
		if note == "" {
			note = "status=" + o.Status
		}
		s.fail(num, note)
		return settled{}
	}

	finishParent := s.claim
	var promoted []string
	// landings pairs each landed patch's issue number with its PR URL for the
	// gate calls after ledger.Finish below; the Ledger's Patched URLs derive
	// from it.
	var landings []patchLanding
	filed, dropped := settle.FileButlerFindings(s.it, num, result, s.room.Findings, func(kept []settle.Finding) func(settle.Finding) settle.Decoration {
		// An unconfigured/off policy can never promote, so it reserves none
		// of the day's shared promotion room. room.Promotions is Sweep's own
		// snapshot, handed down rather than re-walked here (issue #3994): a
		// promotion landed by a concurrent run mid-sweep is not seen (soft
		// cap, chore.Room's own doc).
		remaining := 0
		if s.policy.enabled {
			remaining = s.room.Promotions
		}
		// Unlike remaining, patchesLeft needs no guard: decide's patch gate
		// checks patchEnabled itself.
		patchesLeft := s.room.Patches

		// Reserve the slots this run intends to spend before filing anything
		// (issue #3926): if the done commit below never lands -- the claim
		// was lost to a takeover, or the push itself fails -- the
		// reservation still counts against DayTotals, so a lost Finish can
		// never let a Chore promote past the day's budget. finishParent
		// moves to the reservation tip on success so the done commit's own
		// CAS is checked against it, not the stale claim. Patch candidates
		// count as promotion-eligible here too (see eligible's loop below),
		// so a patch that lands but whose own Finish is lost still leaves
		// the reservation spent -- over-counting that day's promotions,
		// never under-counting them (issue #4074).
		if remaining > 0 {
			eligible := 0
			for _, f := range kept {
				// Patch room is left zero here: a patch candidate whose
				// landing later fails falls back to promote (decide's own
				// patch-gate-fails contract), so it still needs a reserved
				// promotion slot -- this count must not skip it just
				// because it might patch instead.
				if s.policy.decide(f, promoteOnlyRoom(remaining)).kind == promote {
					eligible++
				}
			}
			n := min(remaining, eligible)
			if n > 0 {
				reserved, err := ledger.Reserve(s.ledger, s.chore, s.claim, n, s.now())
				if err != nil {
					fmt.Printf("    #%s  status=promotion-reserve-failed  !! %v\n", num, err)
					remaining = 0
				} else {
					remaining = n
					finishParent = reserved
				}
			}
		}

		return func(f settle.Finding) settle.Decoration {
			backlink := butlerBacklink(s.chore, f)
			dec := s.policy.decide(f, chore.Room{Promotions: remaining, Patches: patchesLeft})
			if dec.patchSkip != "" {
				fmt.Printf("    #%s  status=patch-skipped  note=%s\n", num, dec.patchSkip)
			}
			if dec.kind == patch {
				subject := patchCommitSubject(f.Title)
				pc, err := s.patch.tree.CommitPatch(s.patch.base, f.Patch, subject)
				if err != nil {
					// A stale/rebased diff: file exactly as if it had no
					// Patch at all, re-deciding with patch room zeroed so
					// decide can only promote or skip from here (issue #4074).
					fmt.Printf("    #%s  status=patch-skipped  !! %v\n", num, err)
					dec = s.policy.decide(f, promoteOnlyRoom(remaining))
				} else {
					return settle.Decoration{
						Backlink:    backlink,
						ExtraLabels: []string{dispatchkind.Butler.PatchLabel},
						// Spend the slot, push, and open the PR only in
						// OnFiled, after PostIssue succeeds -- a failed post
						// must not push a branch or open a PR for an issue
						// that never landed (mirrors the promote path below).
						OnFiled: func(url string) {
							issueNum, err := issueNumberFromURL(url)
							if err != nil {
								// No issue number to label or close: the
								// finding stays filed as-is.
								fmt.Printf("    #%s  status=patch-failed  !! %v\n", num, err)
								return
							}
							head := s.patch.forge.AgentBranch(issueNum)
							prURL, err := s.landPatch(pc, subject, head, f, url, issueNum)
							if err != nil {
								fmt.Printf("    #%s  status=patch-failed  !! %v\n", num, err)
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
				return settle.Decoration{Backlink: backlink}
			}
			note := promotionNote(s.chore, f, s.policy, dec.files)
			// Spend the slot only in OnFiled, after PostIssue succeeds, so a
			// failed post frees it back to the rest of the sweep.
			return settle.Decoration{Backlink: backlink + "\n\n" + note, ExtraLabels: dec.labels, OnFiled: func(url string) {
				remaining--
				promoted = append(promoted, url)
			}}
		}
	})

	patched := make([]string, len(landings))
	for i, l := range landings {
		patched[i] = l.prURL
	}
	state := ledger.State{
		LastSwept: s.scope.Head,
		Cursor:    s.scope.NextCursor,
		Filed:     filed,
		Promoted:  promoted,
		Patched:   patched,
		Usage:     d.CumulativeUsage(),
		Dropped:   dropped,
	}
	if _, err := ledger.Finish(s.ledger, s.chore, finishParent, state, s.now()); err != nil {
		fmt.Printf("    #%s  status=ledger-finish-failed  !! %v\n", num, err)
		report.Settled(dispatchkey.Chore(s.chore), forge.Failed.String(), fmt.Sprintf("ledger finish failed: %v", err))
		return settled{}
	}

	note := fmt.Sprintf("%d filed", len(filed))
	if len(promoted) > 0 {
		note = fmt.Sprintf("%d filed, %d promoted", len(filed), len(promoted))
	}
	if len(patched) > 0 {
		note = fmt.Sprintf("%s, %d patched", note, len(patched))
	}
	if dropped > 0 {
		note = fmt.Sprintf("%s, %d dropped", note, dropped)
	}
	report.Settled(dispatchkey.Chore(s.chore), forge.Complete.String(), note)
	fmt.Printf("    #%s  status=%s  note=%s\n", num, o.Status, note)

	// Only now, after the Done commit above has actually landed, hand each
	// patch to the work merge gate: a crash while polling CI must never
	// leave the Chore's claim standing (issue #4076).
	for _, l := range landings {
		s.patch.gate.SettleAdopted(nil, l.issueNum, 0, l.prURL)
	}

	return settled{done: true, filed: len(filed), promoted: len(promoted), dropped: dropped, patched: len(patched)}
}

// landPatch pushes pc's committed branch to head and opens a draft PR for
// it, returning the PR's URL. Called only from OnFiled, after the finding
// issue itself has actually filed (issueNum), so the PR body can close it.
func (s *settleRun) landPatch(pc PatchCommit, subject, head string, f settle.Finding, findingURL, issueNum string) (string, error) {
	if err := s.patch.forge.PushBranch(pc.Dir, pc.Ref, head); err != nil {
		return "", err
	}
	body := patchPRBody(s.chore, f, findingURL, issueNum)
	prURL, _, err := s.patch.forge.CreateDraftPR(subject, body, s.patch.base, head)
	if err != nil {
		return "", err
	}
	return prURL, nil
}

// fallBackToPromote is landPatch's failure path (issue #4074): a finding
// already filed with dispatchkind.Butler.PatchLabel, whose push or PR-create
// then failed, is judged exactly as decidePromote would judge it with no
// Patch, via s.patch.forge's own forge.IssueLabeler rather than PostIssue's
// own labels (the issue already exists) -- reachable only because
// butlerPatchForge (cmd/launcher/butler.go) refuses to turn the rung on
// without an IssueLabeler in the first place. Reports whether it promoted --
// the caller's OnFiled spends the shared remaining/promoted state itself on
// true, the same way the ordinary promote path does. A failed AddLabels
// leaves the finding filed with only its patch label -- logged, never
// fatal, like every other best-effort step in this file.
func (s *settleRun) fallBackToPromote(chorenum, issueNum string, f settle.Finding, remaining int) bool {
	fb := s.policy.decide(f, promoteOnlyRoom(remaining))
	if fb.kind != promote {
		return false
	}
	if err := s.patch.forge.AddLabels(issueNum, fb.labels); err != nil {
		fmt.Printf("    #%s  status=patch-fallback-label-failed  !! %v\n", chorenum, err)
		return false
	}
	if err := s.it.Comment(issueNum, promotionNote(s.chore, f, s.policy, fb.files)); err != nil {
		fmt.Printf("    #%s  status=patch-fallback-comment-failed  !! %v\n", chorenum, err)
	}
	return true
}

// promoteOnlyRoom zeroes the patch half of a chore.Room so a re-decide call
// can only promote or skip a finding that has already left the patch path
// (spent, skipped, or failed to land) -- never offered patch room a second
// time for the same finding (issue #4074).
func promoteOnlyRoom(promotions int) chore.Room {
	return chore.Room{Promotions: promotions}
}

// issueNumberFromURL returns url's trailing path segment -- a filed issue's
// number on the github/forgejo trackers -- for AgentBranch and
// IssueLabeler.AddLabels, neither of which take a URL. It errors when that
// segment is not a positive decimal number: the local tracker's PostIssue
// returns "local:" + slug, which must never become a branch name or a
// Closes reference (issue #4074).
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

// patchCommitSubject derives a Conventional Commits subject for a patch's
// commit (and, doubling as the draft PR's title) from the finding's own
// title: used as-is when it already reads as one, else prefixed so the
// commit history stays Conventional-Commits-clean regardless of what a Box
// happened to title the finding. A leading breaking-change "!" is always
// stripped -- the butler never decides a finding is breaking, so the
// squash title must never carry the marker release-please reads as a major
// bump -- and the result is capped at patchSubjectMaxLen (issue #4074).
func patchCommitSubject(title string) string {
	subject := oneLine(title)
	if !conventionalSubjectRE.MatchString(subject) {
		subject = "chore(butler): " + subject
	}
	subject = conventionalBangRE.ReplaceAllString(subject, "$1$2")
	return truncateSubject(subject, patchSubjectMaxLen)
}

// truncateSubject caps s at limit runes (never splitting a multi-byte rune),
// replacing the last rune with "…" when it had to cut so the result still
// reads as visibly truncated rather than silently clipped.
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

// fail prints a status=failed line and reports the run failed. It applies no
// tracker transition: the butler has no tracker issue to move. Neither files
// findings nor writes a Ledger commit, so the claim stands and
// lastSwept/cursor stay at the prior run's values for the next run to resume
// from.
func (s *settleRun) fail(num, note string) {
	report.Settled(dispatchkey.Chore(s.chore), forge.Failed.String(), note)
	fmt.Printf("    #%s  status=failed  note=%s\n", num, note)
}
