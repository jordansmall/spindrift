package ecosystemkit

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

// A selector that picks every string value reaches same-host URLs at any
// depth, arrays included, and never touches keys, foreign hosts or non-URL
// strings.
func TestStreamRepointJSON_SelectEveryStringValue(t *testing.T) {
	rc := registryvocab.RewriteContext{
		MatchHost: "registry.example.com",
		Forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
		Prefix:    "r0",
	}
	in := `{"https://registry.example.com/key":"https://registry.example.com/a",` +
		`"items":[{"@id":"https://registry.example.com/b/c","n":1,` +
		`"deep":[[["https://registry.example.com/d"]]]},` +
		`"https://other.example.org/x","plain text"]}`
	want := `{"https://registry.example.com/key":"http://127.0.0.1:9999/r0/a",` +
		`"items":[{"@id":"http://127.0.0.1:9999/r0/b/c","n":1,` +
		`"deep":[[["http://127.0.0.1:9999/r0/d"]]]},` +
		`"https://other.example.org/x","plain text"]}`

	var dst bytes.Buffer
	var edits []registryvocab.RewriteEdit
	err := StreamRepointJSON(&dst, strings.NewReader(in), rc,
		func(e registryvocab.RewriteEdit) { edits = append(edits, e) },
		func([]JSONPathStep) bool { return true })
	if err != nil {
		t.Fatalf("StreamRepointJSON: %v", err)
	}
	if got := dst.String(); got != want {
		t.Errorf("output =\n%s\nwant\n%s", got, want)
	}
	// Three repointed, one declined; "plain text" is not a URL so no edit.
	if len(edits) != 4 {
		t.Fatalf("edits = %+v, want 4", edits)
	}
	if edits[3].From != "https://other.example.org/x" || edits[3].To != "" {
		t.Errorf("edits[3] = %+v, want the declined foreign host", edits[3])
	}
}

// The selector sees the container path from the root, with each step's kind
// and, inside an object, the key the value sits under.
func TestStreamRepointJSON_PathSteps(t *testing.T) {
	rc := registryvocab.RewriteContext{
		MatchHost: "h",
		Forwarder: &url.URL{Scheme: "http", Host: "f"},
	}
	var paths [][]JSONPathStep
	err := StreamRepointJSON(&bytes.Buffer{}, strings.NewReader(`{"a":[{"b":"x"},"y"],"c":"z"}`), rc, nil,
		func(p []JSONPathStep) bool {
			paths = append(paths, append([]JSONPathStep(nil), p...))
			return false
		})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]JSONPathStep{
		{{IsObject: true, Key: "a"}, {IsObject: false}, {IsObject: true, Key: "b"}},
		{{IsObject: true, Key: "a"}, {IsObject: false}},
		{{IsObject: true, Key: "c"}},
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %+v, want %+v", paths, want)
	}
	for i := range want {
		if len(paths[i]) != len(want[i]) {
			t.Fatalf("paths[%d] = %+v, want %+v", i, paths[i], want[i])
		}
		for j := range want[i] {
			if paths[i][j] != want[i][j] {
				t.Errorf("paths[%d][%d] = %+v, want %+v", i, j, paths[i][j], want[i][j])
			}
		}
	}
}
