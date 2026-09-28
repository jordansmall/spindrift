package dispatchkey

import (
	"os"
	"strings"
	"testing"
)

func TestFieldsParseRoundTrip(t *testing.T) {
	issue := Issue("42")
	i, c := issue.Fields()
	got, err := Parse(i, c)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", i, c, err)
	}
	if got != issue {
		t.Errorf("round trip: got %+v, want %+v", got, issue)
	}

	chore := Chore("bugs")
	i, c = chore.Fields()
	got, err = Parse(i, c)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", i, c, err)
	}
	if got != chore {
		t.Errorf("round trip: got %+v, want %+v", got, chore)
	}
}

func TestParseRejectsNeither(t *testing.T) {
	if _, err := Parse("", ""); err == nil {
		t.Error("Parse(\"\", \"\") = nil error, want error")
	}
}

func TestParseRejectsBoth(t *testing.T) {
	_, err := Parse("42", "bugs")
	if err == nil {
		t.Fatal("Parse(\"42\", \"bugs\") = nil error, want error")
	}
	if !strings.Contains(err.Error(), "42") || !strings.Contains(err.Error(), "bugs") {
		t.Errorf("error %q does not name both values", err.Error())
	}
}

func TestString(t *testing.T) {
	if got, want := Issue("42").String(), "42"; got != want {
		t.Errorf("Issue(\"42\").String() = %q, want %q", got, want)
	}
	if got, want := Chore("bugs").String(), "butler-bugs"; got != want {
		t.Errorf("Chore(\"bugs\").String() = %q, want %q", got, want)
	}
}

func TestIsChore(t *testing.T) {
	if Issue("42").IsChore() {
		t.Error("Issue(...).IsChore() = true, want false")
	}
	if !Chore("bugs").IsChore() {
		t.Error("Chore(...).IsChore() = false, want true")
	}
}

func TestIsZero(t *testing.T) {
	var k Key
	if !k.IsZero() {
		t.Error("zero Key.IsZero() = false, want true")
	}
	if Issue("42").IsZero() {
		t.Error("Issue(...).IsZero() = true, want false")
	}
	if Chore("bugs").IsZero() {
		t.Error("Chore(...).IsZero() = true, want false")
	}
}

// TestChoreSpellingMatchesBoxSeam pins Chore(name).String() to the literal
// spelling the Box side already emits: agent/entrypoint.sh's _issue_ref and
// butler-prompt.md's OUTCOME line format the same "butler-" prefix
// independently in bash/markdown, so this package's Go rendering must not
// drift from either without both sides noticing.
func TestChoreSpellingMatchesBoxSeam(t *testing.T) {
	prefix := strings.TrimSuffix(Chore("X").String(), "X")
	if prefix != "butler-" {
		t.Fatalf("Chore(\"X\").String() prefix = %q, want \"butler-\"", prefix)
	}

	entrypoint, err := os.ReadFile("../../../../agent/entrypoint.sh")
	if err != nil {
		t.Fatalf("reading entrypoint.sh: %v", err)
	}
	if !strings.Contains(string(entrypoint), "printf 'butler-%s' \"$CHORE_NAME\"") {
		t.Error("agent/entrypoint.sh does not contain the expected butler-<percent>s spelling")
	}

	butlerPrompt, err := os.ReadFile("../../../../templates/default/prompts/butler-prompt.md")
	if err != nil {
		t.Fatalf("reading butler-prompt.md: %v", err)
	}
	if !strings.Contains(string(butlerPrompt), "issue=butler-${CHORE_NAME}") {
		t.Error("templates/default/prompts/butler-prompt.md does not contain the expected butler-${CHORE_NAME} spelling")
	}
}
