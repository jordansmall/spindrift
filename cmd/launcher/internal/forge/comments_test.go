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
