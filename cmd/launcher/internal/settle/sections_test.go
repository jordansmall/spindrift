package settle

import (
	"strings"
	"testing"
)

// A failed intent's bullet renders only the body's first line. An unescaped
// multi-line body would break out of the Markdown list item and inject
// arbitrary Markdown into the posted verdict comment.
func TestBuildFiledIssuesSection_FailedBodyTruncatedToFirstLine(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug", Failed: true, Body: "first line of repro\n\n## Heading\n```code fence```"},
	}

	got := buildFiledIssuesSection(filed)

	if !strings.Contains(got, "first line of repro") {
		t.Errorf("section = %q, want it to contain the body's first line", got)
	}
	if strings.Contains(got, "## Heading") || strings.Contains(got, "```code fence```") {
		t.Errorf("section = %q, want later body lines truncated away", got)
	}
}

// A failed bullet's CR-separated body truncates at its first CR.
func TestBuildFiledIssuesSection_FailedBodyLoneCRTruncated(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug", Failed: true, Body: "first line of repro\r\r## Heading\r```code fence```"},
	}

	got := buildFiledIssuesSection(filed)

	if !strings.Contains(got, "first line of repro") {
		t.Errorf("section = %q, want it to contain the body's first line", got)
	}
	if strings.ContainsRune(got, '\r') || strings.Contains(got, "## Heading") || strings.Contains(got, "```code fence```") {
		t.Errorf("section = %q, want later CR-separated lines truncated away", got)
	}
}

// A failed bullet's body is agent-chosen, untrusted text too, so a Markdown
// link in it must not render as a live link in the posted verdict comment.
func TestBuildFiledIssuesSection_FailedBodyEscapesLinkText(t *testing.T) {
	filed := []filedIntent{
		{Title: "t", Failed: true, Body: "see [text](url) and stray ] here\nmore"},
	}

	got := buildFiledIssuesSection(filed)

	if !strings.Contains(got, `\[text\](url)`) || !strings.Contains(got, `stray \] here`) {
		t.Errorf("section = %q, want body brackets escaped", got)
	}
	if strings.Contains(got, "[text](url)") {
		t.Errorf("section = %q, want no raw link in the body", got)
	}
}

// A title is agent-chosen, untrusted text, so a bracket in it renders escaped
// instead of breaking the surrounding Markdown link. The fixture holds both a
// linked and a failed entry because they render through different paths.
func TestBuildFiledIssuesSection_TitleWithBracketEscaped(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): [bad] title", URL: "https://github.com/owner/repo/issues/501"},
		{Title: "fix(y): [bad] title", Failed: true, Body: "repro"},
	}

	got := buildFiledIssuesSection(filed)

	if !strings.Contains(got, `\[bad\]`) {
		t.Errorf("section = %q, want the bracketed title escaped", got)
	}
	if strings.Contains(got, "[bad]") {
		t.Errorf("section = %q, want no unescaped bracketed title", got)
	}
}

// A multi-line title is agent-chosen text, so only its first line renders; a
// later line would otherwise start a forged heading in the verdict comment. Each
// entry takes a different render path (linked, failed, plain) (issue #3834).
func TestBuildFiledIssuesSection_MultiLineTitleTruncated(t *testing.T) {
	const title = "fix(x): first line\n## forged heading"
	cases := []struct {
		name   string
		intent filedIntent
		bullet string
	}{
		{
			name:   "linked",
			intent: filedIntent{Title: title, URL: "https://github.com/owner/repo/issues/501"},
			bullet: "- [fix(x): first line](https://github.com/owner/repo/issues/501)",
		},
		{
			name:   "failed",
			intent: filedIntent{Title: title, Failed: true, Body: "repro"},
			bullet: "- **fix(x): first line** (filing failed) — repro",
		},
		{
			name:   "plain",
			intent: filedIntent{Title: title, URL: "local:slug"},
			bullet: "- **fix(x): first line** — local:slug",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildFiledIssuesSection([]filedIntent{tc.intent})

			if want := "## Filed issues\n\n" + tc.bullet; got != want {
				t.Errorf("section = %q, want %q", got, want)
			}
		})
	}
}

// The local tracker's PostIssue returns "local:<slug>" rather than a URL, so a
// non-http identifier renders as a plain bullet instead of a broken Markdown
// link.
func TestBuildFiledIssuesSection_NonHTTPURLDegradesToPlainBullet(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug", URL: "local:some-slug"},
	}

	got := buildFiledIssuesSection(filed)

	if strings.Contains(got, "](local:some-slug)") {
		t.Errorf("section = %q, want no Markdown link around the non-http URL", got)
	}
	if !strings.Contains(got, "local:some-slug") {
		t.Errorf("section = %q, want the local identifier still surfaced", got)
	}
}

// An all-skipped filed list renders only the skipped-deduplicated section,
// naming each title and its dedup reference, never the Filed issues heading
// (nothing was actually filed).
func TestBuildSkippedIssuesSection_AllSkipped(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug", Skipped: true, DupRef: "#123"},
		{Title: "fix(y): other bug", Skipped: true, DupRef: `this run's "fix(y): other bug"`},
	}

	got := buildSkippedIssuesSection(filed)

	if !strings.Contains(got, "## Skipped (deduplicated)") {
		t.Errorf("section = %q, want the skipped heading", got)
	}
	if !strings.Contains(got, "open, closed, or one this run filed itself") {
		t.Errorf("section = %q, want the closed-finding-covered lead text", got)
	}
	if !strings.Contains(got, "fix(x): bug") || !strings.Contains(got, "#123") {
		t.Errorf("section = %q, want the first title and its reference", got)
	}
	if !strings.Contains(got, "fix(y): other bug") {
		t.Errorf("section = %q, want the second title", got)
	}
	if strings.Contains(got, "## Filed issues") {
		t.Errorf("section = %q, want no Filed issues heading", got)
	}
}

// A mix of one filed and one skipped intent renders both sections, and the
// skipped entry never appears in the Filed issues list (it was never
// posted).
func TestBuildSkippedIssuesSection_MixedWithFiled(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(a): filed bug", URL: "https://github.com/owner/repo/issues/1"},
		{Title: "fix(b): dup bug", Skipped: true, DupRef: "#42"},
	}

	filedSection := buildFiledIssuesSection(filed)
	skippedSection := buildSkippedIssuesSection(filed)

	if !strings.Contains(filedSection, "fix(a): filed bug") {
		t.Errorf("filedSection = %q, want the filed title", filedSection)
	}
	if strings.Contains(filedSection, "fix(b): dup bug") {
		t.Errorf("filedSection = %q, want no skipped title", filedSection)
	}
	if !strings.Contains(skippedSection, "fix(b): dup bug") || !strings.Contains(skippedSection, "#42") {
		t.Errorf("skippedSection = %q, want the skipped title and its reference", skippedSection)
	}
}

// Both the title and the dedup reference are agent-chosen, untrusted text
// (the reference can echo an intra-run title verbatim), so a bracket in
// either renders escaped rather than breaking the Markdown bullet.
func TestBuildSkippedIssuesSection_BracketsEscaped(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): [bad] title", Skipped: true, DupRef: `this run's "fix(y): [bad] ref"`},
	}

	got := buildSkippedIssuesSection(filed)

	if !strings.Contains(got, `\[bad\]`) {
		t.Errorf("section = %q, want brackets escaped in both title and reference", got)
	}
	if strings.Contains(got, "[bad]") {
		t.Errorf("section = %q, want no unescaped bracket", got)
	}
}

// A multi-line title renders as a single bullet: the title is agent-chosen,
// so a heading on a later line would otherwise break out of the list and
// forge structure in the posted comment (issue #3811 review).
func TestBuildSkippedIssuesSection_MultiLineTitleTruncated(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug\n## forged heading", Skipped: true, DupRef: "#123"},
	}

	got := buildSkippedIssuesSection(filed)

	if strings.Contains(got, "## forged heading") {
		t.Errorf("section = %q, want the title truncated at its first line", got)
	}
	if !strings.Contains(got, "fix(x): bug") {
		t.Errorf("section = %q, want the title's first line kept", got)
	}
}

// A skipped title forged with a lone CR renders as a single bullet.
func TestBuildSkippedIssuesSection_LoneCRTitleTruncated(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug\r## forged heading", Skipped: true, DupRef: "#123"},
	}

	got := buildSkippedIssuesSection(filed)

	if strings.ContainsRune(got, '\r') || strings.Contains(got, "## forged heading") {
		t.Errorf("section = %q, want the title truncated at its first CR", got)
	}
	if !strings.HasSuffix(got, "\n\n- **fix(x): bug** — already tracked: #123") {
		t.Errorf("section = %q, want a single bullet holding the title's first line", got)
	}
}

// A DupRef carrying a line break renders as a single bullet: the render
// site guards itself rather than relying on the producer's %q (issue #3835).
func TestBuildSkippedIssuesSection_MultiLineDupRefTruncated(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug", Skipped: true, DupRef: "#1, this run's \"x\"\n## forged"},
	}

	got := buildSkippedIssuesSection(filed)

	if strings.Contains(got, "## forged") {
		t.Errorf("section = %q, want the DupRef truncated at its first newline", got)
	}
	if !strings.HasSuffix(got, "\n\n- **fix(x): bug** — already tracked: #1, this run's \"x\"") {
		t.Errorf("section = %q, want a single bullet holding the DupRef's first line", got)
	}
}

// A DupRef forged with a lone CR renders as a single bullet (issue #3835).
func TestBuildSkippedIssuesSection_LoneCRDupRefTruncated(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(x): bug", Skipped: true, DupRef: "#1, this run's \"x\"\r## forged"},
	}

	got := buildSkippedIssuesSection(filed)

	if strings.ContainsRune(got, '\r') || strings.Contains(got, "## forged") {
		t.Errorf("section = %q, want the DupRef truncated at its first CR", got)
	}
	if !strings.HasSuffix(got, "\n\n- **fix(x): bug** — already tracked: #1, this run's \"x\"") {
		t.Errorf("section = %q, want a single bullet holding the DupRef's first line", got)
	}
}

// A filed list with no skips renders no skipped section at all.
func TestBuildSkippedIssuesSection_NoSkips(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(a): filed bug", URL: "https://github.com/owner/repo/issues/1"},
	}

	got := buildSkippedIssuesSection(filed)

	if got != "" {
		t.Errorf("section = %q, want empty string when nothing was skipped", got)
	}
}

// The lead sentence must not claim an intra-run match was "an existing open
// issue": it wasn't, it was filed moments earlier by this same run (issue
// #3811 review).
func TestBuildSkippedIssuesSection_LeadCoversIntraRunMatch(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(y): other bug", Skipped: true, DupRef: `this run's "fix(z): peer bug"`},
	}

	got := buildSkippedIssuesSection(filed)

	if strings.Contains(got, "existing open issue") {
		t.Errorf("section = %q, want no claim of an existing open issue for an intra-run match", got)
	}
	if !strings.Contains(got, "already-filed issue") {
		t.Errorf("section = %q, want the reworded lead covering both a backlog and an intra-run match", got)
	}
}

// firstLine cuts at whichever line break comes first — LF, CR or CRLF — for
// every renderer call site at once.
func TestFirstLine(t *testing.T) {
	for _, in := range []string{"a\rb", "a\r\nb", "a\nb", "a"} {
		if got := firstLine(in); got != "a" {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, "a")
		}
	}
}

// buildVerdictCommentSections is the call site's joiner: filed and skipped
// sections, in that order, blank-line separated, so both survive as one
// appended block.
func TestBuildVerdictCommentSections_JoinsFiledAndSkipped(t *testing.T) {
	filed := []filedIntent{
		{Title: "fix(a): filed bug", URL: "https://github.com/owner/repo/issues/1"},
		{Title: "fix(b): dup bug", Skipped: true, DupRef: "#42"},
	}

	got := buildVerdictCommentSections(filed)

	filedIdx := strings.Index(got, "## Filed issues")
	skippedIdx := strings.Index(got, "## Skipped (deduplicated)")
	if filedIdx == -1 || skippedIdx == -1 {
		t.Fatalf("sections = %q, want both headings present", got)
	}
	if filedIdx > skippedIdx {
		t.Errorf("sections = %q, want Filed issues before Skipped", got)
	}
}

// Nothing filed and nothing skipped yields an empty joined string, so the
// call site's non-empty check still skips appending altogether.
func TestBuildVerdictCommentSections_EmptyWhenNothing(t *testing.T) {
	if got := buildVerdictCommentSections(nil); got != "" {
		t.Errorf("sections = %q, want empty string", got)
	}
}

// A backslash in agent text would otherwise escape the escaping backslash
// added before a bracket and re-open the bracket as live Markdown (issue #4219).
func TestEscapeMarkdownLinkText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"backslash-bracket link", `\[evil\](https://x.example)`, `\\\[evil\\\](https://x.example)`},
		{"lone backslash", `a\b`, `a\\b`},
		{"brackets", `[bad]`, `\[bad\]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeMarkdownLinkText(tc.in); got != tc.want {
				t.Errorf("escapeMarkdownLinkText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarkdownInlineText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"line break and brackets", "[a]\\b\nrest", `\[a\]\\b`},
		{"bracket past the line break", "a\n[b]", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := markdownInlineText(tc.in); got != tc.want {
				t.Errorf("markdownInlineText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Every render site that escapes agent text must neutralise a backslash-led
// bracket link, not just a bare bracket (issue #4219).
func TestMarkdownBullets_BackslashBracketLinkEscaped(t *testing.T) {
	const evil = `\[evil\](https://x.example)`
	const esc = `\\\[evil\\\](https://x.example)`
	cases := []struct {
		name   string
		render func([]filedIntent) string
		intent filedIntent
		want   string
	}{
		{
			name:   "filed title linked",
			render: buildFiledIssuesSection,
			intent: filedIntent{Title: evil, URL: "https://github.com/owner/repo/issues/501"},
			want:   "- [" + esc + "](https://github.com/owner/repo/issues/501)",
		},
		{
			name:   "filed title plain",
			render: buildFiledIssuesSection,
			intent: filedIntent{Title: evil, URL: "local:slug"},
			want:   "- **" + esc + "** — local:slug",
		},
		{
			name:   "failed title",
			render: buildFiledIssuesSection,
			intent: filedIntent{Title: evil, Failed: true, Body: "repro"},
			want:   "- **" + esc + "** (filing failed) — repro",
		},
		{
			name:   "failed body",
			render: buildFiledIssuesSection,
			intent: filedIntent{Title: "t", Failed: true, Body: evil + "\nmore"},
			want:   "- **t** (filing failed) — " + esc,
		},
		{
			name:   "skipped title",
			render: buildSkippedIssuesSection,
			intent: filedIntent{Title: evil, Skipped: true, DupRef: "#12"},
			want:   "- **" + esc + "** — already tracked: #12",
		},
		{
			name:   "skipped dupref",
			render: buildSkippedIssuesSection,
			intent: filedIntent{Title: "t", Skipped: true, DupRef: evil},
			want:   "- **t** — already tracked: " + esc,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.render([]filedIntent{tc.intent})

			// Each fixture renders one bullet, always the last line.
			bullet := got[strings.LastIndex(got, "\n")+1:]
			if bullet != tc.want {
				t.Errorf("bullet = %q, want %q", bullet, tc.want)
			}
		})
	}
}
