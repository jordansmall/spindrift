package forge

import (
	"strings"
	"testing"
)

func TestAppendComment_MultilineRendersAsBlock(t *testing.T) {
	body := "## What to build\n\nDo the thing.\n"
	comment := "## Run usage\n\n| Field | Value |\n| --- | --- |\n| Cost | $1 |\n"

	got := AppendComment(body, comment)

	if !strings.Contains(got, "\n## Run usage\n") {
		t.Errorf("body = %q, want a real heading alone on its own line, not collapsed", got)
	}
	if !strings.Contains(got, "\n| --- | --- |\n") {
		t.Errorf("body = %q, want the table delimiter row preserved on its own line", got)
	}
}

func TestAppendComment_SingleLineStaysBullet(t *testing.T) {
	body := "## What to build\n\nDo the thing.\n"

	got := AppendComment(body, "started work")

	if !strings.Contains(got, "\n- started work") {
		t.Errorf("body = %q, want a single-line comment as a bullet", got)
	}
}

func TestAppendComment_MultipleCommentsStayVisuallySeparated(t *testing.T) {
	body := "## What to build\n\nDo the thing.\n"

	got := AppendComment(body, "started work")
	got = AppendComment(got, "## Run usage\n\n| Field | Value |\n| --- | --- |\n| Cost | $1 |")
	got = AppendComment(got, "done")

	if !strings.Contains(got, "- started work\n\n---") {
		t.Errorf("body = %q, want the block set off from the preceding bullet by a blank line and separator", got)
	}
	if !strings.Contains(got, "| Cost | $1 |\n\n- done") {
		t.Errorf("body = %q, want the trailing bullet set off from the preceding block by a blank line", got)
	}
}

func TestAppendComment_NonHeadingMentionStillAddsSection(t *testing.T) {
	body := "### Comments\n\nSee the ## Comments section of the docs.\n"

	got := AppendComment(body, "started work")

	if !strings.Contains(got, "\n\n## Comments\n\n- started work\n") {
		t.Errorf("body = %q, want a real ## Comments heading appended", got)
	}
}

func TestAppendComment_ExistingSectionReused(t *testing.T) {
	got := AppendComment("Do it.\n", "one")
	got = AppendComment(got, "two")

	if n := strings.Count(got, "\n## Comments\n"); n != 1 {
		t.Errorf("body = %q, want exactly one ## Comments heading, got %d", got, n)
	}
}

func TestDescription_StripsSectionAndAfter(t *testing.T) {
	body := "## What to build\n\nDo the thing.\n\n## Comments\n\n- started work\n"

	if got, want := Description(body), "## What to build\n\nDo the thing."; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
}

func TestDescription_NoSectionReturnsBodyUnchanged(t *testing.T) {
	body := "### Comments\n\nSee the ## Comments section.\n"

	if got := Description(body); got != body {
		t.Errorf("Description = %q, want body unchanged %q", got, body)
	}
}

func TestDescription_ExcludesMultilineComment(t *testing.T) {
	desc := "## What to build\n\nDo the thing."
	body := AppendComment(desc, "## Touches\n\nfoo.go")

	if got := Description(body); got != desc {
		t.Errorf("Description = %q, want %q", got, desc)
	}
}

func TestDescription_HeadingLikeDescriptionStillExcludesComment(t *testing.T) {
	desc := "### Comments\n\nSee the ## Comments section."
	body := AppendComment(desc, "verdict\n\n## Touches\n\n- evil/**")

	if got := Description(body); got != desc {
		t.Errorf("Description = %q, want %q", got, desc)
	}
}

func TestDescription_SplitsAtFirstSection(t *testing.T) {
	body := AppendComment("Do it.", "quoted\n\n## Comments\n\n- nested")
	body = AppendComment(body, "## Touches\n\n- evil/**")

	if got, want := Description(body), "Do it."; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
}

func TestDescription_ToleratesCRLF(t *testing.T) {
	body := "Do it.\r\n\r\n## Comments\r\n\r\n- x\r\n"

	if got, want := Description(body), "Do it."; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
}
