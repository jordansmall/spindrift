package bundle_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/credresolver"
	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/golden"
)

// The Strategies live only in Go, so docs/reference.md's spans over them are
// rendered here rather than by a Nix renderer. It holds no ecosystem-name
// literal, since containment_test.go scans this package's test files too.

const regenHint = "regenerate with `nix run .#regen-goldens`, or `UPDATE_GOLDENS=1 go test -run '^TestReferenceDocs_GeneratedSpans$' ./internal/driver/bundle` from cmd/launcher"

type docSpan struct {
	name   string
	render func() string
}

func (s docSpan) begin() string {
	return "<!-- BEGIN GENERATED " + s.name + " -- nix run .#regen-goldens -- DO NOT EDIT -->\n"
}

func (s docSpan) end() string { return "<!-- END GENERATED " + s.name + " -->\n" }

var referenceSpans = []docSpan{
	{"LOCKFILE CLASSIFICATION", renderLockfileClassification},
	{"REGISTRY DISCOVERY SOURCES", renderRegistryDiscoverySources},
}

// renderLockfileClassification merges consecutive registered Strategies sharing a
// Classification (npm, yarn and pnpm report one ecosystem). Only adjacent Strategies
// merge, so the rendered order is always first-match precedence, i.e. registration
// order.
func renderLockfileClassification() string {
	type group struct {
		class string
		files []string
	}
	var groups []group
	for _, row := range ecosystem.Registered() {
		if n := len(groups); n == 0 || groups[n-1].class != row.Classification {
			groups = append(groups, group{class: row.Classification})
		}
		g := &groups[len(groups)-1]
		for _, name := range row.LockfileNames {
			g.files = append(g.files, "`"+name+"`")
		}
	}
	const head1, head2 = "file(s)", "reported ecosystem"
	w1, w2 := len(head1), len(head2)
	cells := make([][2]string, len(groups))
	for i, g := range groups {
		cells[i] = [2]string{strings.Join(g.files, ", "), g.class}
		w1, w2 = max(w1, len(cells[i][0])), max(w2, len(cells[i][1]))
	}
	var b strings.Builder
	line := func(col1, col2 string) { fmt.Fprintf(&b, "| %-*s | %-*s |\n", w1, col1, w2, col2) }
	line(head1, head2)
	line(strings.Repeat("-", w1), strings.Repeat("-", w2))
	for _, c := range cells {
		line(c[0], c[1])
	}
	return b.String()
}

func renderRegistryDiscoverySources() string {
	var configs []string
	for _, row := range ecosystem.Registered() {
		if row.InTreeConfigPath != "" {
			configs = append(configs, "`"+row.InTreeConfigPath+"`")
		}
	}
	var stores []string
	for _, k := range credresolver.StoreKinds() {
		stores = append(stores, fmt.Sprintf("`%s` (`~/%s`)", k.SourceKey, strings.Join(k.StorePath, "/")))
	}
	return "- Config files read from the Target repo: " + strings.Join(configs, ", ") + "\n" +
		"- Credential stores, searched **in this order**: " + strings.Join(stores, ", ") + "\n"
}

// spliceSpan replaces only the text between s's markers. Anything but exactly
// one begin marker followed by exactly one end marker is an error, so a
// damaged doc is never silently rewritten.
func spliceSpan(doc string, s docSpan, body string) (string, error) {
	if n := strings.Count(doc, s.begin()); n != 1 {
		return "", fmt.Errorf("want exactly one %q marker, found %d", strings.TrimSpace(s.begin()), n)
	}
	if n := strings.Count(doc, s.end()); n != 1 {
		return "", fmt.Errorf("want exactly one %q marker, found %d", strings.TrimSpace(s.end()), n)
	}
	start := strings.Index(doc, s.begin()) + len(s.begin())
	stop := strings.Index(doc, s.end())
	if stop < start {
		return "", fmt.Errorf("%q precedes %q", strings.TrimSpace(s.end()), strings.TrimSpace(s.begin()))
	}
	return doc[:start] + body + doc[stop:], nil
}

func TestReferenceDocs_GeneratedSpans(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "..", "docs", "reference.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reference guide: %v", err)
	}
	doc := string(raw)
	updated := doc
	var stale []string
	for _, s := range referenceSpans {
		next, err := spliceSpan(updated, s, s.render())
		if err != nil {
			t.Fatalf("docs/reference.md span %s: %v; %s", s.name, err, regenHint)
		}
		if next != updated {
			stale = append(stale, s.name)
		}
		updated = next
	}
	if updated == doc {
		return
	}
	if golden.Update() {
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			t.Fatalf("write reference guide: %v", err)
		}
		return
	}
	for _, name := range stale {
		t.Errorf("docs/reference.md span %s is out of date with the registered Strategies; %s", name, regenHint)
	}
}

func TestSpliceSpan(t *testing.T) {
	s := docSpan{"X", nil}
	doc := "before\n" + s.begin() + "old\n" + s.end() + "after\n"
	got, err := spliceSpan(doc, s, "new\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := "before\n" + s.begin() + "new\n" + s.end() + "after\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for name, bad := range map[string]string{
		"missing begin": "a\n" + s.end(),
		"missing end":   s.begin() + "a\n",
		"duplicated":    doc + doc,
		"reversed":      s.end() + s.begin(),
	} {
		if _, err := spliceSpan(bad, s, "new\n"); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
