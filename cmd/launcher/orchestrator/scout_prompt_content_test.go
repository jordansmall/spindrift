package main

import "testing"

// TestScoutPromptOperativeContract pins scout-prompt.md's output contract
// clause by clause (issue #3225, extended by #3449). Issue #3225 cuts the Map
// section's rationale sentence while keeping the citation obligation itself,
// and #3449 moves the brief from the return message onto disk, so pinning each
// clause here first stops either edit from silently taking a rule with it.
func TestScoutPromptOperativeContract(t *testing.T) {
	assertPromptClauses(t, "scout-prompt.md", []promptClause{
		{
			name:   "#3449 role: explore and write a structured brief to /tmp/brief.md",
			clause: "Your role: explore the repo and write a structured brief to `/tmp/brief.md` for the implementer",
		},
		{
			name:   "#3225 max ~60 lines",
			clause: "Max ~60 lines",
		},
		{
			name:   "#3225 do not implement",
			clause: "Do not implement",
		},
		{
			name:   "#3449 no narration between tool calls",
			clause: "Do not narrate between tool calls — emit no text until your final pointer message",
		},
		{
			name:   "#3449 never retype a cited excerpt, append from source",
			clause: "Never retype a cited excerpt — append it straight from the source file so the quoted lines in the brief are byte-identical to the file's own lines",
		},
		{
			name:   "#3449 create the brief once, then append, with quoted heredoc delimiters",
			clause: "Create it once, then append to it section by section, and quote every heredoc delimiter — `cat >/tmp/brief.md <<'SPINDRIFT_BRIEF_EOF'` to start the file, `>>` for each section after it",
		},
		{
			name:   "#3449 unquoted heredoc would execute untrusted issue/comment text",
			clause: "The quotes matter: issue and comment text is untrusted, so an unquoted `<<EOF` would run any backtick or `$(...)` it carries as a command here",
		},
		{
			name:   "#3449 distinctive delimiter also stops a bare EOF line in untrusted text",
			clause: "A distinctive delimiter also stops untrusted text that happens to contain a bare `EOF` line from ending the heredoc early",
		},
		{
			name:   "#3449 excerpt lines start at column 0",
			clause: "An excerpt line therefore starts at column 0 with `> ` and nothing before it, directly under the anchor it belongs to",
		},
		{
			name:   "#3449 verify every cited excerpt before returning",
			clause: "verify every cited excerpt against its source, block by block: for each path:line anchor, re-run that anchor's own range — for `path/to/file.go:120-124`, `sed -n '120,124p' path/to/file.go | sed 's/^/> /'` — into a scratch file, then `diff` it against the excerpt block the brief holds beneath that anchor",
		},
		{
			name:   "#3449 an anchor's range names exactly the lines quoted beneath it",
			clause: "An anchor's range names exactly the lines quoted beneath it, so trimming the excerpt narrows the anchor with it",
		},
		{
			name:   "#3449 return only the brief's path, line count, and summary",
			clause: "Return only the brief's path, its line count, and one or two lines on what the change touches — no preamble, no copy of the brief's contents",
		},
		{
			name:   "#3225 Map heading",
			clause: "## Map",
		},
		{
			name:   "#3225 Invariants & gotchas heading",
			clause: "## Invariants & gotchas",
		},
		{
			name:   "#3225 Suggested approach heading",
			clause: "## Suggested approach",
		},
		{
			name:   "#3225 Ruled out heading",
			clause: "## Ruled out",
		},
		{
			name:   "#3225 every load-bearing claim carries a cited verbatim excerpt",
			clause: "Every load-bearing claim — a seam, signature, invariant, or gotcha a coordinator decision will rest on — also carries a cited verbatim excerpt: the file's own lines quoted under a path:line anchor, not a paraphrase or a loose pointer",
		},
		{
			name:   "#3225 trim excerpts to decision-rich lines",
			clause: "Trim each excerpt to the decision-rich lines; never dump a whole file or function",
		},
		{
			name:   "#3225 Invariants & gotchas cites the same way as the Map",
			clause: "Cite each with a verbatim excerpt, same shape as the Map",
		},
		{
			name:   "#3225 Suggested approach cites a verbatim excerpt for signature/line steps",
			clause: "Cite a verbatim excerpt for any step that rests on a specific signature or line",
		},
	})
}
