package signalwire

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Resolved the same way internal/promptassembly's promptsDir is.
const relayPromptsDir = "../../../../templates/default/prompts"

// relayFragments is every fragment teaching the SPINDRIFT_ISSUE_INTENT
// log carrier's JSON keys; a new one that teaches a key belongs here too.
// Hand-listed, not globbed: other fragments name SPINDRIFT_ISSUE_INTENT
// without teaching any key.
var relayFragments = []string{
	relayPromptsDir + "/fragments/filer-file-relay.md",
	relayPromptsDir + "/fragments/filer-label-relay-butler.md",
}

// extraTaughtAllowed is prose-taught keys that legitimately aren't on
// IssueIntent's json tags: settle's own issueIntent (internal/settle) wraps
// the wire shape with "labels", the Box's own label request, never part of
// the wire struct itself.
var extraTaughtAllowed = map[string]bool{"labels": true}

// relayKeyPattern matches a key taught in prose: a quoted bare word followed
// by a colon ("title":) or a closing backtick (`"class"`). Values such as
// "bug" or "path/to/file.go:Symbol" never match.
var relayKeyPattern = regexp.MustCompile("\"([A-Za-z]+)\"(?:\\s*:|`)")

// TestRelayKeysMatchIssueIntentTags pins the log carrier's prose-taught keys
// to IssueIntent's json tags, both ways (issue #3992).
func TestRelayKeysMatchIssueIntentTags(t *testing.T) {
	taught := map[string]bool{}
	for _, path := range relayFragments {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range relayKeyPattern.FindAllStringSubmatch(string(b), -1) {
			taught[m[1]] = true
		}
	}

	structKeys := map[string]bool{}
	rt := reflect.TypeOf(IssueIntent{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		structKeys[name] = true
	}

	var extraTaught, missingTaught []string
	for k := range taught {
		if !structKeys[k] && !extraTaughtAllowed[k] {
			extraTaught = append(extraTaught, k)
		}
	}
	for k := range structKeys {
		if !taught[k] {
			missingTaught = append(missingTaught, k)
		}
	}
	sort.Strings(extraTaught)
	sort.Strings(missingTaught)

	if len(extraTaught) > 0 || len(missingTaught) > 0 {
		t.Fatalf("relay fragments and signalwire.IssueIntent's json tags disagree:\n"+
			"taught but not on the struct: %v\n"+
			"on the struct but never taught: %v",
			extraTaught, missingTaught)
	}
}
