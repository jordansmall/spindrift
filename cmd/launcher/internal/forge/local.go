package forge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const frontmatterDelim = "---"

// localFrontmatter is the YAML frontmatter block of a local issue file (ADR
// 0013): title, dispatch state, arbitrary labels, a created timestamp, and an
// optional parent.
type localFrontmatter struct {
	Title   string
	State   string
	Labels  []string
	Created string
	Parent  string
}

// localIssue is a parsed local issue file: its frontmatter plus Markdown body.
type localIssue struct {
	frontmatter localFrontmatter
	body        string
}

// parseLocalIssue splits data into its YAML frontmatter block and Markdown
// body. Frontmatter is a restricted subset: scalar "key: value" lines and a
// flow-sequence "labels: [a, b]" list — enough for the fields this adapter
// writes, so no external YAML dependency is needed (the launcher module is
// stdlib-only; see lib/mkHarness.nix's vendorHash policy).
func parseLocalIssue(data []byte) (localIssue, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != frontmatterDelim {
		return localIssue{}, fmt.Errorf("missing opening %q frontmatter delimiter", frontmatterDelim)
	}
	var fm localFrontmatter
	i := 1
	closed := false
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == frontmatterDelim {
			closed = true
			i++
			break
		}
		key, val, ok := strings.Cut(lines[i], ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "title":
			fm.Title = unquote(val)
		case "state":
			fm.State = unquote(val)
		case "created":
			fm.Created = unquote(val)
		case "parent":
			fm.Parent = unquote(val)
		case "labels":
			fm.Labels = parseFlowList(val)
		}
	}
	if !closed {
		return localIssue{}, fmt.Errorf("missing closing %q frontmatter delimiter", frontmatterDelim)
	}
	body := strings.TrimPrefix(strings.Join(lines[i:], "\n"), "\n")
	return localIssue{frontmatter: fm, body: body}, nil
}

// LocalTracker is the file-based IssueTracker adapter (ADR 0013): one
// Markdown file per issue, with YAML frontmatter, in a directory the operator
// keeps git-ignored (default .spindrift/issues/). labels maps canonical
// DispatchState values to the frontmatter "state" marker, the same way the
// GitHub adapter maps them to label names.
type LocalTracker struct {
	dir    string
	labels DispatchLabels
}

// NewLocalTracker returns an IssueTracker backed by Markdown + YAML
// frontmatter files in dir.
func NewLocalTracker(dir string, labels DispatchLabels) *LocalTracker {
	return &LocalTracker{dir: dir, labels: labels}
}

// slugPath returns the file path for issue num.
func (lt *LocalTracker) slugPath(num string) string {
	return filepath.Join(lt.dir, num+".md")
}

// readIssueFile reads and parses the issue file for num.
func (lt *LocalTracker) readIssueFile(num string) (localIssue, error) {
	data, err := os.ReadFile(lt.slugPath(num))
	if err != nil {
		return localIssue{}, fmt.Errorf("read local issue %s: %w", num, err)
	}
	li, err := parseLocalIssue(data)
	if err != nil {
		return localIssue{}, fmt.Errorf("parse local issue %s: %w", num, err)
	}
	return li, nil
}

// toIssue converts a parsed local issue file into the launcher's Issue type.
// Local issues have no closed/open concept of their own; State is always
// reported OPEN. The frontmatter's dispatch-state marker is appended to
// Labels so cross-backend logic that checks for a specific dispatch label
// (e.g. failedLabel) works the same as it does against the GitHub adapter,
// whose Labels already include whatever label represents current state.
func toIssue(num string, li localIssue) Issue {
	labels := append(append([]string(nil), li.frontmatter.Labels...), li.frontmatter.State)
	return Issue{
		Number: num,
		Title:  li.frontmatter.Title,
		Body:   li.body,
		State:  IssueOpen,
		Labels: labels,
	}
}

// ListIssues returns issues whose frontmatter state marker matches state, in
// canonical order (ascending by the created timestamp).
func (lt *LocalTracker) ListIssues(state DispatchState) ([]Issue, error) {
	entries, err := os.ReadDir(lt.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read local issues dir %s: %w", lt.dir, err)
	}
	want := lt.labels.Label(state)

	type entry struct {
		iss     Issue
		created time.Time
	}
	var matches []entry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		num := strings.TrimSuffix(e.Name(), ".md")
		li, err := lt.readIssueFile(num)
		if err != nil {
			return nil, err
		}
		if li.frontmatter.State != want {
			continue
		}
		created, _ := time.Parse(time.RFC3339, li.frontmatter.Created)
		matches = append(matches, entry{iss: toIssue(num, li), created: created})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].created.Before(matches[j].created) })

	issues := make([]Issue, len(matches))
	for i, m := range matches {
		issues[i] = m.iss
	}
	return issues, nil
}

// Issue returns full details for the local issue num.
func (lt *LocalTracker) Issue(num string) (Issue, error) {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return Issue{}, err
	}
	return toIssue(num, li), nil
}

// TransitionState rewrites issue num's frontmatter "state" marker from the
// label for from to the label for to. Unlike the GitHub adapter's label
// add/remove pair, the local file has a single scalar state field, so the
// transition is a plain overwrite.
func (lt *LocalTracker) TransitionState(num string, from, to DispatchState) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.frontmatter.State = lt.labels.Label(to)
	if err := os.WriteFile(lt.slugPath(num), []byte(li.render()), 0o644); err != nil {
		return fmt.Errorf("write local issue %s: %w", num, err)
	}
	return nil
}

// DepsOf returns the dependency slugs listed under issue num's "## Blocked
// by" section — always DepSourceBody; the local tracker has no native
// relationship concept. Unlike ParseBlockerRefs (GitHub "#N" refs), local
// issues reference each other by filename slug, one per bullet line.
func (lt *LocalTracker) DepsOf(num string) ([]Dependency, error) {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return nil, err
	}
	return WithSource(parseLocalBlockers(li.body), DepSourceBody), nil
}

// parseLocalBlockers extracts dependency slugs from a "## Blocked by" section
// (reusing blockers.go's header/heading detection), one slug per bullet line.
func parseLocalBlockers(body string) []string {
	seen := map[string]bool{}
	var refs []string
	inSection := false
	for _, rawLine := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if blockedByHeader.MatchString(strings.TrimSpace(line)) {
			inSection = true
			continue
		}
		if anyHeading.MatchString(line) {
			inSection = false
		}
		if inSection && bulletItem.MatchString(line) {
			slug := strings.TrimSpace(bulletItem.ReplaceAllString(line, ""))
			slug = strings.Trim(slug, "`")
			if slug != "" && !seen[slug] {
				seen[slug] = true
				refs = append(refs, slug)
			}
		}
	}
	return refs
}

// Comment appends body as a bullet under a "## Comments" section at the end
// of issue num's file, creating the section if absent.
func (lt *LocalTracker) Comment(num, body string) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.body = appendComment(li.body, body)
	if err := os.WriteFile(lt.slugPath(num), []byte(li.render()), 0o644); err != nil {
		return fmt.Errorf("write local issue %s: %w", num, err)
	}
	return nil
}

// Probe ensures the local issues directory exists and returns its absolute
// path (the local analogue of a resolved repo slug).
func (lt *LocalTracker) Probe() (string, error) {
	if err := os.MkdirAll(lt.dir, 0o755); err != nil {
		return "", fmt.Errorf("create local issues dir %s: %w", lt.dir, err)
	}
	abs, err := filepath.Abs(lt.dir)
	if err != nil {
		return lt.dir, nil
	}
	return abs, nil
}

// ListLabels returns the four dispatch state markers. The local adapter has
// no separate label registry to check against — a file's state field is
// always one of these — so they are reported unconditionally present.
func (lt *LocalTracker) ListLabels() ([]string, error) {
	return lt.labels.AllLabels(), nil
}

// CreateLabel is a no-op: the local adapter has no label registry to create
// entries in (see ListLabels).
func (lt *LocalTracker) CreateLabel(name, description, color string) error {
	return nil
}

// render serializes li back into frontmatter + body form, the inverse of
// parseLocalIssue.
func (li localIssue) render() string {
	var b strings.Builder
	b.WriteString(frontmatterDelim + "\n")
	fmt.Fprintf(&b, "title: %s\n", li.frontmatter.Title)
	fmt.Fprintf(&b, "state: %s\n", li.frontmatter.State)
	fmt.Fprintf(&b, "labels: [%s]\n", strings.Join(li.frontmatter.Labels, ", "))
	fmt.Fprintf(&b, "created: %s\n", li.frontmatter.Created)
	if li.frontmatter.Parent != "" {
		fmt.Fprintf(&b, "parent: %s\n", li.frontmatter.Parent)
	}
	b.WriteString(frontmatterDelim + "\n")
	b.WriteString(li.body)
	return b.String()
}

// unquote strips a single layer of matching single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// parseFlowList parses a YAML flow sequence like "[a, b, c]" into its
// elements. An empty or absent list yields nil.
func parseFlowList(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = unquote(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}
