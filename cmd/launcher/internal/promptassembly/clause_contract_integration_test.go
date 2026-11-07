//go:build integration

package promptassembly_test

import (
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/signalwire"
)

// clauseContracts binds spindrift's own prompt fragments; a Consumer override
// of a fragment keeps free wording.
var clauseContracts = []clauseContract{
	// commit-rework-orchestrator.md
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "Run-state handoff",
		reason:   "the fold-or-not decision reads the seeded handoff section, so the fragment must name it (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "Last reviewer verdict:",
		reason:   "no env knob marks a rework pass, so the fragment keys off the handoff's verdict line; bare handoff presence is too coarse (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "git commit --amend",
		reason:   "a rework pass folds a fix into the branch-tip commit rather than adding a follow-up commit (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "autosquash fixup",
		reason:   "a rework pass folds a fix into an older commit with an autosquash fixup (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "rewriting that history here is expected",
		reason:   "the agent must know amending the branch's earlier commits is sanctioned on a rework pass (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "small focused commits",
		reason:   "a first-slice pass keeps the several-small-commits preference (issue #2698).",
	},
	{
		fragment: "commit-rework-orchestrator.md",
		clause:   "force-push",
		absent:   true,
		reason:   "the fragment is ungated and a host-mediated local Box relays a bundle rather than force-pushing, so it must stay forge-neutral (issue #5285).",
	},

	// review-loop-orchestrator.md
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "/tmp/pass-summary.md",
		reason:   "the pass summary goes to its own file, apart from dispositions and decisions (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "/tmp/dispositions.md",
		reason:   "dispositions go to a file separate from the pass summary, at the path --dispositions-path defaults to (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "fixed in commit",
		from:     "a second, separate file",
		to:       "a third, separate file",
		reason:   "a fixed finding's disposition line must cite the fixing commit (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "won't-fix",
		from:     "a second, separate file",
		to:       "a third, separate file",
		reason:   "a deliberately unfixed finding's disposition line must say so (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "pasted diff hunks",
		from:     "a second, separate file",
		to:       "a third, separate file",
		reason:   "the dispositions file stays a terse reference, not pasted diff content (issue #2550).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "/tmp/decisions.md",
		reason:   "decisions go to a file separate from both the pass summary and the dispositions, at the path --decisions-path defaults to (issue #2695).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "chose",
		from:     "a third, separate file",
		reason:   "each decision line records what was chosen (issue #2695).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "rejected",
		from:     "a third, separate file",
		reason:   "each decision line records the rejected alternative (issue #2695).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "constraint",
		from:     "a third, separate file",
		reason:   "each decision line records the constraint that drove the choice (issue #2695).",
	},
	{
		fragment: "review-loop-orchestrator.md",
		clause:   "pasted diff hunks",
		from:     "a third, separate file",
		reason:   "the decisions file stays a terse reference, not pasted diff content (issue #2695).",
	},

	// caveman-default.md
	{
		fragment: "caveman-default.md",
		clause:   "Code, commands, error messages, and commit messages are exempt and stay verbatim",
		reason:   "code, commands, error messages, and commit messages stay exempt from compression (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "commit messages are always full human-quality prose",
		reason:   "commit messages are always full human-quality prose (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "Never route these through `/caveman`",
		reason:   "marker grammar routed through /caveman is forbidden (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "the `note=` field of the SPINDRIFT_OUTCOME line is exempt",
		reason:   "the note= field is human-quality prose, exempt like a commit message (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "The payload those signals carry is exempt on that same tier, whichever carrier takes it",
		reason:   "a signal payload is exempt whichever carrier takes it (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "the draft PR's title and body, and a filed issue's title and body, stay full human-quality prose",
		reason:   "PR and filed-issue titles and bodies stay full human-quality prose (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "as the text you hand `driver-exec signal pr-intent` or `driver-exec signal issue-intent` under the `socket` carrier, where no marker line renders at all",
		reason:   "the socket-carrier payload is named, since no marker line renders under it (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "Where a marker line is the carrier, it must keep its required shape exactly intact",
		reason:   "the shape requirement is scoped to marker lines, not every exemption (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "the outcome line's key=value pairs",
		reason:   "the outcome line's key=value pairs must stay intact (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "the PR-intent line's nonce and base64 payload as one unbroken token",
		reason:   "the PR-intent line's nonce and base64 payload must stay one unbroken token (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "never reworded, reflowed, or line-wrapped",
		reason:   "exempted marker lines are never reworded, reflowed, or line-wrapped (issue #2710).",
	},
	{
		fragment: "caveman-default.md",
		clause:   "The verdict line must additionally remain the first line of the agent's final message",
		reason:   "the verdict line must stay the first line of the final message (issue #2710).",
	},

	// caveman-default-review.md
	{
		fragment: "caveman-default-review.md",
		clause:   "every finding written under `## Blocking` or `## Non-blocking`",
		reason:   "Blocking and Non-blocking findings stay exempt on the review-only tier (issue #3265).",
	},
	{
		fragment: "caveman-default-review.md",
		clause:   "The `## Probed (APPROVE only)` lines are exempt on the same tier",
		reason:   "Probed lines are exempt on the same tier (issue #3265).",
	},
	{
		fragment: "caveman-default-review.md",
		clause:   "but text a human reads cold",
		reason:   "the Probed exemption rationale names text a human reads cold (issue #3265).",
	},
	{
		fragment: "caveman-default-review.md",
		clause:   "compressed, the Probed section stops being evidence",
		reason:   "the Probed exemption rationale says a compressed section stops being evidence (issue #3265).",
	},
	{
		fragment: "caveman-default-review.md",
		clause:   "Write those lines in full human-quality prose too",
		reason:   "the reviewer must get the full-prose directive, not just its rationale, which alone would survive a paragraph that inverts the order (issue #3265).",
	},

	// filer-file-relay.md
	{
		fragment: "filer-file-relay.md",
		clause:   "'{\"title\":\"...\",\"body\":\"...\",\"dedupTerms\":[\"path/to/file.go:Symbol\"]}'",
		reason:   "the untyped payload's dedupTerms example keeps the path/to/file.go:Symbol site key, the one concrete shape a filer must match (issue #4108).",
	},
	{
		fragment: "filer-file-relay.md",
		clause:   "'{\"title\":\"...\",\"body\":\"...\",\"dedupTerms\":[\"path/to/file.go:Symbol\"],\"type\":\"bug\"}'",
		reason:   "the typed payload's dedupTerms example keeps the path/to/file.go:Symbol site key, the one concrete shape a filer must match (issue #4108).",
	},
	{
		fragment: "filer-file-relay.md",
		clause:   "(a line key from step 2 still goes stale once the code around it moves)",
		reason:   "the carrier prose must not imply any key it is handed is durable (issue #3812).",
	},

	// filer-file-relay-socket.md
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "(a line key from step 2 still goes stale once the code around it moves)",
		reason:   "the carrier prose must not imply any key it is handed is durable (issue #3812).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "no `-body` flag",
		reason:   "the send has no -body flag; the body goes on stdin or -body-file (issues #3866, #3909).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "`bug`, `enhancement`, `chore`",
		reason:   "the three -type values are named (issues #3866, #3909).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "title with backticks needs single quotes, not double",
		reason:   "a title with backticks must be single-quoted, or the shell runs it as a command substitution (issues #3866, #3909).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "title containing a single quote needs `'\\''` in its place",
		reason:   "a title with an apostrophe needs the '\\'' escape (issues #3866, #3909).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "exit code is the acceptance",
		reason:   "the exit code is the acceptance of the send (issues #3866, #3909).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
		reason:   "the filer, who runs the send, is told to run it bare so a pipe cannot mask a rejection (issues #3866, #3909).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "driver-exec signal issue-intent -title '<title>' -type",
		reason:   "the heredoc example opens with the title and -type flags (issues #3866, #3909).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "driver-exec signal issue-intent -title '<title>' -type bug -dedup 'path/to/file.go:Symbol' <<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the heredoc example carries -dedup, the one concrete site-key shape a filer must match (issue #3866).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "-body-file",
		absent:   true,
		to:       "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the first send example is the stdin heredoc, so the -body-file fallback must not appear before it (issue #3866).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "-body-file",
		from:     "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the fragment must still name -body-file after the heredoc example, as the fallback for a body already in a file (issue #3866).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "\nSPINDRIFT_SIGNAL_EOF\n",
		verbatim: true,
		reason:   "the closing delimiter must sit alone at column one: a copied, indented closing delimiter never matches, so bash never terminates the heredoc (issue #3866).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "<<EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "<<'EOF'",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "<<\"EOF\"",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "filer-file-relay-socket.md",
		clause:   "<<-EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},

	// file-issues-relay-socket.md
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "no `-body` flag",
		reason:   "the send has no -body flag; the body goes on stdin or -body-file (issues #3866, #3909).",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "`bug`, `enhancement`, `chore`",
		reason:   "the three -type values are named (issues #3866, #3909).",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "title with backticks needs single quotes, not double",
		reason:   "a title with backticks must be single-quoted, or the shell runs it as a command substitution (issues #3866, #3909).",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "title containing a single quote needs `'\\''` in its place",
		reason:   "a title with an apostrophe needs the '\\'' escape (issues #3866, #3909).",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "exit code is the acceptance",
		reason:   "the exit code is the acceptance of the send (issues #3866, #3909).",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "The filer runs the command bare — never through `tail`, `head`, or `2>&1 |`",
		reason:   "the relay summaries stay third-person about the filer's bare send, since the reader never runs it (issues #3866, #3909).",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "The filer sends each in one call, the body on stdin through a quoted `SPINDRIFT_SIGNAL_EOF` heredoc (`-body-file` only as a fallback for a body already in a file). The flags are exactly `-title`, `-type`, `-dedup` (repeatable),",
		reason:   "the send paragraph's opening, up to the common flags: the three relay summaries duplicate the filer's send paragraph, so each must say it alike (issues #3866, #3909).",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "there is no `-body` flag. `-type` is exactly one of `bug`, `enhancement`, `chore`. A title with backticks needs single quotes, not double. A title containing a single quote needs `'\\''` in its place. The filer runs the command bare — never through `tail`, `head`, or `2>&1 |` — because the exit code is the acceptance.",
		reason:   "the send paragraph's close, from the no -body note on: the three relay summaries duplicate the filer's send paragraph, so each must say it alike (issues #3866, #3909).",
	},
	{
		fragment: "file-issues-relay-socket.md",
		clause:   "(repeatable), and `-body-file` — there is no `-body` flag",
		reason:   "the file-issues and research relay summaries name exactly -body-file as the last flag, alike (issues #3866, #3909).",
	},

	// research-file-issues-relay-socket.md
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "no `-body` flag",
		reason:   "the send has no -body flag; the body goes on stdin or -body-file (issues #3866, #3909).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "`bug`, `enhancement`, `chore`",
		reason:   "the three -type values are named (issues #3866, #3909).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "title with backticks needs single quotes, not double",
		reason:   "a title with backticks must be single-quoted, or the shell runs it as a command substitution (issues #3866, #3909).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "title containing a single quote needs `'\\''` in its place",
		reason:   "a title with an apostrophe needs the '\\'' escape (issues #3866, #3909).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "exit code is the acceptance",
		reason:   "the exit code is the acceptance of the send (issues #3866, #3909).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "The filer runs the command bare — never through `tail`, `head`, or `2>&1 |`",
		reason:   "the relay summaries stay third-person about the filer's bare send, since the reader never runs it (issues #3866, #3909).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "The filer sends each in one call, the body on stdin through a quoted `SPINDRIFT_SIGNAL_EOF` heredoc (`-body-file` only as a fallback for a body already in a file). The flags are exactly `-title`, `-type`, `-dedup` (repeatable),",
		reason:   "the send paragraph's opening, up to the common flags: the three relay summaries duplicate the filer's send paragraph, so each must say it alike (issues #3866, #3909).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "there is no `-body` flag. `-type` is exactly one of `bug`, `enhancement`, `chore`. A title with backticks needs single quotes, not double. A title containing a single quote needs `'\\''` in its place. The filer runs the command bare — never through `tail`, `head`, or `2>&1 |` — because the exit code is the acceptance.",
		reason:   "the send paragraph's close, from the no -body note on: the three relay summaries duplicate the filer's send paragraph, so each must say it alike (issues #3866, #3909).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "(repeatable), and `-body-file` — there is no `-body` flag",
		reason:   "the file-issues and research relay summaries name exactly -body-file as the last flag, alike (issues #3866, #3909).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "**File findings.** Hand any finding worth tracking as follow-up work — beyond the issue's own verdict — to the filer subagent: a bug, a missing feature, a perf chore your exploration surfaced. It is pre-provisioned; pass it each finding verbatim, plus this issue's number for provenance.",
		reason:   "the file-findings paragraph; the research log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issue #3947).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "Research never writes to the Issue Tracker itself — the filer is relay-only here.",
		reason:   "research never writes to the tracker itself; the research log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issue #3947).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "instead of filing directly, and the launcher files each one host-side once you exit, applying the `agent-research-finding` label and a \"Filed from research on #<N>\" backlink itself — no issue URL is known yet.",
		reason:   "the launcher files host-side and applies the provenance label and backlink; the research log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issue #3947).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "Best-effort: filing must never block the verdict. - On success (the filer reports `QUEUED`), just note in the verdict that the finding was queued for filing — never fabricate an issue URL; the launcher appends a \"Filed issues\" section with the real links right after your comment posts. - On failure (the filer errors, times out, or returns nothing usable), describe the finding directly in the verdict body instead.",
		reason:   "filing is best-effort, with the failure fallback into the verdict body; the research log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issue #3947).",
	},
	{
		fragment: "research-file-issues-relay-socket.md",
		clause:   "It sends each one over the Signal socket via `driver-exec signal issue-intent` instead of filing directly",
		reason:   "the socket twin's mechanism clause, the one place besides its send paragraph the twins differ (issue #3947).",
	},

	// butler-file-issues-relay-socket.md
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "no `-body` flag",
		reason:   "the send has no -body flag; the body goes on stdin or -body-file (issues #3866, #3909).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "`bug`, `enhancement`, `chore`",
		reason:   "the three -type values are named (issues #3866, #3909).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "title with backticks needs single quotes, not double",
		reason:   "a title with backticks must be single-quoted, or the shell runs it as a command substitution (issues #3866, #3909).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "title containing a single quote needs `'\\''` in its place",
		reason:   "a title with an apostrophe needs the '\\'' escape (issues #3866, #3909).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "exit code is the acceptance",
		reason:   "the exit code is the acceptance of the send (issues #3866, #3909).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "The filer runs the command bare — never through `tail`, `head`, or `2>&1 |`",
		reason:   "the relay summaries stay third-person about the filer's bare send, since the reader never runs it (issues #3866, #3909).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "The filer sends each in one call, the body on stdin through a quoted `SPINDRIFT_SIGNAL_EOF` heredoc (`-body-file` only as a fallback for a body already in a file). The flags are exactly `-title`, `-type`, `-dedup` (repeatable),",
		reason:   "the send paragraph's opening, up to the common flags: the three relay summaries duplicate the filer's send paragraph, so each must say it alike (issues #3866, #3909).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "there is no `-body` flag. `-type` is exactly one of `bug`, `enhancement`, `chore`. A title with backticks needs single quotes, not double. A title containing a single quote needs `'\\''` in its place. The filer runs the command bare — never through `tail`, `head`, or `2>&1 |` — because the exit code is the acceptance.",
		reason:   "the send paragraph's close, from the no -body note on: the three relay summaries duplicate the filer's send paragraph, so each must say it alike (issues #3866, #3909).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "`-body-file`, `-class`, `-concurrence`, and `-patch-file` (the last two only when the finding has one)",
		reason:   "-class, -concurrence and -patch-file are butler-only flags, the one place the butler's send paragraph differs (issues #3866, #3880).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "(`path/to/file.go:Symbol`, where Symbol is the symbol it names or else the nearest enclosing one, or `path/to/file.go:<line>` only when no symbol encloses the site)",
		reason:   "the dedup key is the nearest enclosing symbol, a bare line only when none encloses; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "A Markdown site has no symbol: key it `path/to/file.md:<anchor>`, the anchor of the nearest heading above it (lowercased, punctuation dropped but hyphens and underscores kept, then each run of spaces and hyphens folded to one hyphen, a repeated heading taking a `-1`, `-2` suffix: `#### The pi Driver` is `the-pi-driver`), never a line number or a free word. Text above a file's first heading keys on the bare path `path/to/file.md`.",
		reason:   "a Markdown site keys on its heading anchor, never a line number; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "**File findings.** Hand every finding worth tracking to the filer subagent: a bug, a missing feature, a perf chore your sweep surfaced. It is pre-provisioned; pass it each finding verbatim. This dispatch carries no tracker issue number for provenance — instead, give the filer the finding's dedup/site key",
		reason:   "the opening of the file-findings paragraph; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "Also give the filer the finding's class, and, only when the `reviewer` agreed on it (see PROMOTION CANDIDATES above), the reviewer's one-line concurrence, and, when the finding is a patch candidate the reviewer approved, its diff as the finding's patch.",
		reason:   "the class, concurrence and patch instruction; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "The butler never writes to the Issue Tracker itself — the filer is relay-only here.",
		reason:   "the butler never writes to the tracker itself; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "and the launcher files each one host-side once you exit, applying the `agent-butler-finding` label itself — no issue URL is known yet.",
		reason:   "the launcher files host-side and applies the provenance label; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "Best-effort: filing must never block the sweep. - On success (the filer reports `QUEUED`), just count it toward this run's OUTCOME note — never fabricate an issue URL.",
		reason:   "filing is best-effort and never blocks the sweep; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "If the filer reports its class was rejected as off the class list, re-delegate the finding with a class from the valid ones the rejection names (see FINDING CLASSES above) — never drop it for that.",
		reason:   "a class-rejected finding is re-delegated with a valid class, never dropped (issue #4766); the twin test also keeps the log twin in step.",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "On any other failure (the filer errors, times out, or returns nothing usable), drop the finding",
		reason:   "every other failure still drops the finding, so the class exemption stays narrow (issue #4766).",
	},
	{
		fragment: "butler-file-issues-relay-socket.md",
		clause:   "It sends each one over the Signal socket via `driver-exec signal issue-intent` instead of filing directly",
		reason:   "the socket twin's mechanism clause, the one place besides its send paragraph the twins differ (issue #3927).",
	},

	// open-pr-create-outbox-socket.md
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "-body-file",
		absent:   true,
		to:       "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the first send example is the stdin heredoc, so the -body-file fallback must not appear before it (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "-body-file",
		from:     "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the fragment must still name -body-file after the heredoc example, as the fallback for a body already in a file (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "\nSPINDRIFT_SIGNAL_EOF\n",
		verbatim: true,
		reason:   "the closing delimiter must sit alone at column one: a copied, indented closing delimiter never matches, so bash never terminates the heredoc (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "<<EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "<<'EOF'",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "<<\"EOF\"",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "<<-EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "driver-exec signal pr-intent -title '<conventional title>' <<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the heredoc opens with the title flag and no -body-file, a one-call stdin send (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "There is no `-nonce` flag, and the run's nonce goes neither on this command nor on the outcome line.",
		reason:   "the send has no -nonce flag (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
		reason:   "a piped send hides a rejection behind the last command's exit code (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "The command's exit code is the acceptance",
		reason:   "the exit code is the acceptance of the send (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "Sending the command again replaces the earlier intent rather than racing it",
		reason:   "a corrected resend replaces the earlier intent (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "A title containing a single quote needs `'\\''` in its place",
		reason:   "a title with an apostrophe needs the '\\'' escape (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "The closing `SPINDRIFT_SIGNAL_EOF` must sit alone at column one — an indented copy never ends the heredoc.",
		reason:   "the closing delimiter's column-one rule: the two PR-intent fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "The flags are exactly `-title` (required) and `-body-file` (optional). There is no `-nonce` flag, and the run's nonce goes neither on this command nor on the outcome line. `-body-file <file>` is the fallback for a body that already sits in a file; without it the body is read from stdin. The title is its own flag, so the launcher never has to guess where the title ends; quote a title containing backticks in single quotes, so the shell does not run them as a command substitution. A title containing a single quote needs `'\\''` in its place (closing the quote, an escaped quote, reopening it). The quotes around the delimiter keep `$` and backticks in the body literal, and the distinctive delimiter stops a bare `EOF` line in quoted issue or comment text from ending the body early.",
		reason:   "the flags paragraph: the two PR-intent fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`. The command's exit code is the acceptance, and a pipe replaces it with the last piped command's own. A zero exit prints `signal pr-intent accepted: <n> bytes, <hash>, sequence <n>`, and the intent is taken. A non-zero exit prints `signal pr-intent rejected (<status>): <reason>` — the intent was NOT taken, so read the reason, fix it, and send the command again.",
		reason:   "the acceptance paragraph: the two PR-intent fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "open-pr-create-outbox-socket.md",
		clause:   "Sending the command again replaces the earlier intent rather than racing it, so correcting a title or body costs one more call, never the run.",
		reason:   "the resend sentence: the two PR-intent fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},

	// if-blocked-pr-outbox-socket.md
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "-body-file",
		absent:   true,
		to:       "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the first send example is the stdin heredoc, so the -body-file fallback must not appear before it (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "-body-file",
		from:     "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the fragment must still name -body-file after the heredoc example, as the fallback for a body already in a file (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "\nSPINDRIFT_SIGNAL_EOF\n",
		verbatim: true,
		reason:   "the closing delimiter must sit alone at column one: a copied, indented closing delimiter never matches, so bash never terminates the heredoc (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "<<EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "<<'EOF'",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "<<\"EOF\"",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "<<-EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "driver-exec signal pr-intent -title '<conventional title>' <<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the heredoc opens with the title flag and no -body-file, a one-call stdin send (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "There is no `-nonce` flag, and the run's nonce goes neither on this command nor on the outcome line.",
		reason:   "the send has no -nonce flag (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
		reason:   "a piped send hides a rejection behind the last command's exit code (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "The command's exit code is the acceptance",
		reason:   "the exit code is the acceptance of the send (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "Sending the command again replaces the earlier intent rather than racing it",
		reason:   "a corrected resend replaces the earlier intent (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "A title containing a single quote needs `'\\''` in its place",
		reason:   "a title with an apostrophe needs the '\\'' escape (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "If it never exits zero, even after you fixed each rejection's reason and sent it again — here or before you reached IF BLOCKED — stop resending and go on to step 5, naming the rejection in `note=` alongside what's done and what remains.",
		reason:   "only the IF BLOCKED step names a step 5 for the resend loop to exit to; the open step's exit is the OUTCOME fragment (issue #6256).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "The closing `SPINDRIFT_SIGNAL_EOF` must sit alone at column one — an indented copy never ends the heredoc.",
		reason:   "the closing delimiter's column-one rule: the two PR-intent fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "The flags are exactly `-title` (required) and `-body-file` (optional). There is no `-nonce` flag, and the run's nonce goes neither on this command nor on the outcome line. `-body-file <file>` is the fallback for a body that already sits in a file; without it the body is read from stdin. The title is its own flag, so the launcher never has to guess where the title ends; quote a title containing backticks in single quotes, so the shell does not run them as a command substitution. A title containing a single quote needs `'\\''` in its place (closing the quote, an escaped quote, reopening it). The quotes around the delimiter keep `$` and backticks in the body literal, and the distinctive delimiter stops a bare `EOF` line in quoted issue or comment text from ending the body early.",
		reason:   "the flags paragraph: the two PR-intent fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`. The command's exit code is the acceptance, and a pipe replaces it with the last piped command's own. A zero exit prints `signal pr-intent accepted: <n> bytes, <hash>, sequence <n>`, and the intent is taken. A non-zero exit prints `signal pr-intent rejected (<status>): <reason>` — the intent was NOT taken, so read the reason, fix it, and send the command again.",
		reason:   "the acceptance paragraph: the two PR-intent fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "if-blocked-pr-outbox-socket.md",
		clause:   "Sending the command again replaces the earlier intent rather than racing it, so correcting a title or body costs one more call, never the run.",
		reason:   "the resend sentence: the two PR-intent fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},

	// research-verdict-relay-socket.md
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "-body-file",
		absent:   true,
		to:       "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the first send example is the stdin heredoc, so the -body-file fallback must not appear before it (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "-body-file",
		from:     "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the fragment must still name -body-file after the heredoc example, as the fallback for a body already in a file (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "\nSPINDRIFT_SIGNAL_EOF\n",
		verbatim: true,
		reason:   "the closing delimiter must sit alone at column one: a copied, indented closing delimiter never matches, so bash never terminates the heredoc (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "<<EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "<<'EOF'",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "<<\"EOF\"",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "<<-EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "driver-exec signal comment <<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "driver-exec signal comment is the verb, with the verdict body as a stdin heredoc (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "The only flag is `-body-file` (optional)",
		reason:   "the only flag is -body-file (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   fmt.Sprintf("a hard ceiling of %d bytes per field", signalwire.MaxBodyBytes),
		reason:   "the socket enforces signalwire.MaxBodyBytes per field, and the fragment must name it (issue #3726).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "A body over that ceiling is refused outright, never truncated",
		reason:   "an oversize body is refused, never truncated, so the agent must not expect half a verdict to post (issue #3726).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
		reason:   "a piped send hides a rejection behind the last command's exit code (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "The command's exit code is the acceptance",
		reason:   "the exit code is the acceptance of the send (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "The closing `SPINDRIFT_SIGNAL_EOF` must sit alone at column one — an indented copy never ends the heredoc.",
		reason:   "the closing delimiter's column-one rule: the shared and local research-verdict fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   fmt.Sprintf("The only flag is `-body-file` (optional): `-body-file <file>` is the fallback for a body that already sits in a file; without it the body is read from stdin. The quotes around the delimiter keep `$` and backticks in the body literal, and the distinctive delimiter stops a bare `EOF` line in quoted issue or comment text from ending the body early. The body is the verdict comment, structured per the sections below. It goes over the socket whole — no marker line, no base64, and none of the log carrier's 8 KB result cap to budget against — up to a hard ceiling of %d bytes per field. A body over that ceiling is refused outright, never truncated, so a verdict that overruns it costs a retry rather than posting half.", signalwire.MaxBodyBytes),
		reason:   "the flags and ceiling paragraph: the shared and local research-verdict fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`. The command's exit code is the acceptance, and a pipe replaces it with the last piped command's own. A zero exit prints `signal comment accepted: <n> bytes, <hash>, sequence <n>`, and the comment is taken. A non-zero exit prints `signal comment rejected (<status>): <reason>` — the signal was NOT taken, so read the reason, fix it, and send the command again.",
		reason:   "the acceptance paragraph: the shared and local research-verdict fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "research-verdict-relay-socket.md",
		clause:   "Sending the command again replaces the earlier body rather than racing it, so correcting a verdict costs one more call, never the run.",
		reason:   "the resend sentence: the shared and local research-verdict fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},

	// local/research-verdict-relay-socket.md
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "-body-file",
		absent:   true,
		to:       "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the first send example is the stdin heredoc, so the -body-file fallback must not appear before it (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "-body-file",
		from:     "<<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "the fragment must still name -body-file after the heredoc example, as the fallback for a body already in a file (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "\nSPINDRIFT_SIGNAL_EOF\n",
		verbatim: true,
		reason:   "the closing delimiter must sit alone at column one: a copied, indented closing delimiter never matches, so bash never terminates the heredoc (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "<<EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "<<'EOF'",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "<<\"EOF\"",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "<<-EOF",
		absent:   true,
		reason:   "a bare EOF delimiter lets a bare EOF line in quoted issue or comment text end the heredoc early and run the rest as shell; the delimiter must be SPINDRIFT_SIGNAL_EOF (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "driver-exec signal comment <<'SPINDRIFT_SIGNAL_EOF'",
		reason:   "driver-exec signal comment is the verb, with the verdict body as a stdin heredoc (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "The only flag is `-body-file` (optional)",
		reason:   "the only flag is -body-file (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   fmt.Sprintf("a hard ceiling of %d bytes per field", signalwire.MaxBodyBytes),
		reason:   "the socket enforces signalwire.MaxBodyBytes per field, and the fragment must name it (issue #3726).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "A body over that ceiling is refused outright, never truncated",
		reason:   "an oversize body is refused, never truncated, so the agent must not expect half a verdict to post (issue #3726).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`",
		reason:   "a piped send hides a rejection behind the last command's exit code (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "The command's exit code is the acceptance",
		reason:   "the exit code is the acceptance of the send (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "The closing `SPINDRIFT_SIGNAL_EOF` must sit alone at column one — an indented copy never ends the heredoc.",
		reason:   "the closing delimiter's column-one rule: the shared and local research-verdict fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   fmt.Sprintf("The only flag is `-body-file` (optional): `-body-file <file>` is the fallback for a body that already sits in a file; without it the body is read from stdin. The quotes around the delimiter keep `$` and backticks in the body literal, and the distinctive delimiter stops a bare `EOF` line in quoted issue or comment text from ending the body early. The body is the verdict comment, structured per the sections below. It goes over the socket whole — no marker line, no base64, and none of the log carrier's 8 KB result cap to budget against — up to a hard ceiling of %d bytes per field. A body over that ceiling is refused outright, never truncated, so a verdict that overruns it costs a retry rather than posting half.", signalwire.MaxBodyBytes),
		reason:   "the flags and ceiling paragraph: the shared and local research-verdict fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "Run the command bare — never through `tail`, `head`, or `2>&1 |`. The command's exit code is the acceptance, and a pipe replaces it with the last piped command's own. A zero exit prints `signal comment accepted: <n> bytes, <hash>, sequence <n>`, and the comment is taken. A non-zero exit prints `signal comment rejected (<status>): <reason>` — the signal was NOT taken, so read the reason, fix it, and send the command again.",
		reason:   "the acceptance paragraph: the shared and local research-verdict fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},
	{
		fragment: "local/research-verdict-relay-socket.md",
		clause:   "Sending the command again replaces the earlier body rather than racing it, so correcting a verdict costs one more call, never the run.",
		reason:   "the resend sentence: the shared and local research-verdict fragments duplicate the send's closing prose, so each must say it alike (issue #3866).",
	},

	// outcome-ready-means-outbox-socket.md
	{
		fragment: "outcome-ready-means-outbox-socket.md",
		clause:   "no intent is queued: follow IF BLOCKED below instead, which ends in `status=blocked` with a `note=` naming the rejection.",
		reason:   "a send that never exited zero must route to IF BLOCKED rather than claim ready (issue #3866).",
	},

	// butler-file-issues-relay.md
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "(`path/to/file.go:Symbol`, where Symbol is the symbol it names or else the nearest enclosing one, or `path/to/file.go:<line>` only when no symbol encloses the site)",
		reason:   "the dedup key is the nearest enclosing symbol, a bare line only when none encloses; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "A Markdown site has no symbol: key it `path/to/file.md:<anchor>`, the anchor of the nearest heading above it (lowercased, punctuation dropped but hyphens and underscores kept, then each run of spaces and hyphens folded to one hyphen, a repeated heading taking a `-1`, `-2` suffix: `#### The pi Driver` is `the-pi-driver`), never a line number or a free word. Text above a file's first heading keys on the bare path `path/to/file.md`.",
		reason:   "a Markdown site keys on its heading anchor, never a line number; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "**File findings.** Hand every finding worth tracking to the filer subagent: a bug, a missing feature, a perf chore your sweep surfaced. It is pre-provisioned; pass it each finding verbatim. This dispatch carries no tracker issue number for provenance — instead, give the filer the finding's dedup/site key",
		reason:   "the opening of the file-findings paragraph; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "Also give the filer the finding's class, and, only when the `reviewer` agreed on it (see PROMOTION CANDIDATES above), the reviewer's one-line concurrence, and, when the finding is a patch candidate the reviewer approved, its diff as the finding's patch.",
		reason:   "the class, concurrence and patch instruction; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "The butler never writes to the Issue Tracker itself — the filer is relay-only here.",
		reason:   "the butler never writes to the tracker itself; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "and the launcher files each one host-side once you exit, applying the `agent-butler-finding` label itself — no issue URL is known yet.",
		reason:   "the launcher files host-side and applies the provenance label; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "Best-effort: filing must never block the sweep. - On success (the filer reports `QUEUED`), just count it toward this run's OUTCOME note — never fabricate an issue URL.",
		reason:   "filing is best-effort and never blocks the sweep; the butler log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issues #3927, #3812).",
	},
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "If the filer reports its class was rejected as off the class list, re-delegate the finding with a class from the valid ones the rejection names (see FINDING CLASSES above) — never drop it for that.",
		reason:   "a class-rejected finding is re-delegated with a valid class, never dropped (issue #4766); the twin test also keeps the log twin in step.",
	},
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "On any other failure (the filer errors, times out, or returns nothing usable), drop the finding",
		reason:   "every other failure still drops the finding, so the class exemption stays narrow (issue #4766).",
	},
	{
		fragment: "butler-file-issues-relay.md",
		clause:   "It emits `SPINDRIFT_ISSUE_INTENT` lines instead of filing directly",
		reason:   "the log twin's mechanism clause, the one place besides the socket twin's send paragraph the twins differ (issue #3927).",
	},

	// research-file-issues-relay.md
	{
		fragment: "research-file-issues-relay.md",
		clause:   "**File findings.** Hand any finding worth tracking as follow-up work — beyond the issue's own verdict — to the filer subagent: a bug, a missing feature, a perf chore your exploration surfaced. It is pre-provisioned; pass it each finding verbatim, plus this issue's number for provenance.",
		reason:   "the file-findings paragraph; the research log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issue #3947).",
	},
	{
		fragment: "research-file-issues-relay.md",
		clause:   "Research never writes to the Issue Tracker itself — the filer is relay-only here.",
		reason:   "research never writes to the tracker itself; the research log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issue #3947).",
	},
	{
		fragment: "research-file-issues-relay.md",
		clause:   "instead of filing directly, and the launcher files each one host-side once you exit, applying the `agent-research-finding` label and a \"Filed from research on #<N>\" backlink itself — no issue URL is known yet.",
		reason:   "the launcher files host-side and applies the provenance label and backlink; the research log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issue #3947).",
	},
	{
		fragment: "research-file-issues-relay.md",
		clause:   "Best-effort: filing must never block the verdict. - On success (the filer reports `QUEUED`), just note in the verdict that the finding was queued for filing — never fabricate an issue URL; the launcher appends a \"Filed issues\" section with the real links right after your comment posts. - On failure (the filer errors, times out, or returns nothing usable), describe the finding directly in the verdict body instead.",
		reason:   "filing is best-effort, with the failure fallback into the verdict body; the research log and socket relay twins mirror this prose by hand, since the carrier is chosen per dispatch and fragments share no include, so each must say it alike (issue #3947).",
	},
	{
		fragment: "research-file-issues-relay.md",
		clause:   "It emits `SPINDRIFT_ISSUE_INTENT` lines instead of filing directly",
		reason:   "the log twin's mechanism clause, the one place besides the socket twin's send paragraph the twins differ (issue #3947).",
	},
}

// TestClauseContracts assembles every golden cell once and checks each
// contract against the fragment text the cells actually rendered.
func TestClauseContracts(t *testing.T) {
	renders := renderedFragments{}
	for _, cell := range goldenCells() {
		c := defaultCell()
		cell.setup(c)
		result, _ := assembleCell(t, c, t.TempDir())
		renders[cell.name] = promptassembly.FragmentRenders(result)
	}

	for _, c := range clauseContracts {
		t.Run(c.fragment+"/"+c.clause, func(t *testing.T) {
			if err := checkClauseContract(c, renders); err != nil {
				t.Error(err)
			}
		})
	}
}
