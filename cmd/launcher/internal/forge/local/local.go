package local

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
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
	// Closed is the local-only open/closed axis (ADR 0029), independent of
	// the dispatch State marker — absent/false means open.
	Closed bool
	// Landing is the immutable landing reference RecordLanding writes after
	// a work outcome line is parsed (ADR 0029) — a PR URL or push-only
	// branch ref, never a cached merge-state.
	Landing string
	// Abandoned is set by FlagAbandoned when the issue's landing PR was
	// closed without merging (ADR 0029) — absent/false means not abandoned.
	Abandoned bool
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
		case "closed":
			fm.Closed = unquote(val) == "true"
		case "landing":
			fm.Landing = unquote(val)
		case "abandoned":
			fm.Abandoned = unquote(val) == "true"
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

// LocalTracker is the file-based forge.IssueTracker adapter (ADR 0013): one
// Markdown file per issue, with YAML frontmatter, in a directory the operator
// keeps git-ignored (default .spindrift/issues/). labels maps canonical
// forge.DispatchState values to the frontmatter "state" marker, the same way the
// GitHub adapter maps them to label names.
type LocalTracker struct {
	dir           string
	labels        forge.DispatchLabels
	verdictLabels forge.VerdictLabels
}

var _ forge.HostPostedIssueFiler = (*LocalTracker)(nil)

// NewLocalTracker returns a forge.IssueTracker backed by Markdown + YAML
// frontmatter files in dir. verdictLabels configures CompleteVerdict (the
// research dispatch kind's Complete transition); omitted for work-kind
// construction sites, matching NewFake's variadic convention for an
// optional, kind-specific config value.
func NewLocalTracker(dir string, labels forge.DispatchLabels, verdictLabels ...forge.VerdictLabels) *LocalTracker {
	var vl forge.VerdictLabels
	if len(verdictLabels) > 0 {
		vl = verdictLabels[0]
	}
	return &LocalTracker{dir: dir, labels: labels, verdictLabels: vl}
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
// State reflects the frontmatter's closed: axis (ADR 0029) — IssueClosed
// when true, IssueOpen otherwise (absent/false). The frontmatter's
// dispatch-state marker is appended to Labels so cross-backend logic that
// checks for a specific dispatch label (e.g. failedLabel) works the same as
// it does against the GitHub adapter, whose Labels already include whatever
// label represents current state — skipped when State is empty (a
// PostIssue'd, untriaged issue carries no marker), so Labels never gains a
// stray "" element.
func toIssue(num string, li localIssue) forge.Issue {
	labels := append([]string(nil), li.frontmatter.Labels...)
	if li.frontmatter.State != "" {
		labels = append(labels, li.frontmatter.State)
	}
	state := forge.IssueOpen
	if li.frontmatter.Closed {
		state = forge.IssueClosed
	}
	return forge.Issue{
		Number:    num,
		Title:     li.frontmatter.Title,
		Body:      li.body,
		State:     state,
		Labels:    labels,
		Landing:   li.frontmatter.Landing,
		Abandoned: li.frontmatter.Abandoned,
		Parent:    li.frontmatter.Parent,
	}
}

// StateLabels implements forge.LabeledTracker, returning the DispatchLabels
// lt resolves DispatchState values through.
func (lt *LocalTracker) StateLabels() forge.DispatchLabels {
	return lt.labels
}

// ListIssues returns issues whose frontmatter state marker matches state,
// excluding closed issues, in canonical order (ascending by the created
// timestamp).
func (lt *LocalTracker) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	want := lt.labels.Label(state)
	return lt.listIssues(func(li localIssue) bool { return !li.frontmatter.Closed && li.frontmatter.State == want })
}

// ListOpenIssues returns every non-closed issue file in dir, in canonical
// order (ascending by the created timestamp), regardless of its frontmatter
// state marker — unlike ListIssues, which filters to a single state.
func (lt *LocalTracker) ListOpenIssues() ([]forge.Issue, error) {
	return lt.listIssues(func(li localIssue) bool { return !li.frontmatter.Closed })
}

// AllIssues returns every issue file in dir, in canonical order (ascending
// by the created timestamp), regardless of parent, state, or dispatch
// marker — see forge.SeamLister.
func (lt *LocalTracker) AllIssues() ([]forge.Issue, error) {
	return lt.listIssues(func(localIssue) bool { return true })
}

// listIssues scans dir for issue files matching keep, in canonical order
// (ascending by the created timestamp) — the shared walk behind ListIssues
// and ListOpenIssues.
func (lt *LocalTracker) listIssues(keep func(localIssue) bool) ([]forge.Issue, error) {
	entries, err := os.ReadDir(lt.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read local issues dir %s: %w", lt.dir, err)
	}

	type entry struct {
		iss     forge.Issue
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
		if !keep(li) {
			continue
		}
		created, _ := time.Parse(time.RFC3339, li.frontmatter.Created)
		matches = append(matches, entry{iss: toIssue(num, li), created: created})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].created.Before(matches[j].created) })

	issues := make([]forge.Issue, len(matches))
	for i, m := range matches {
		issues[i] = m.iss
	}
	return issues, nil
}

// Issue returns full details for the local issue num.
func (lt *LocalTracker) Issue(num string) (forge.Issue, error) {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return forge.Issue{}, err
	}
	return toIssue(num, li), nil
}

// TransitionState rewrites issue num's frontmatter "state" marker from the
// label for from to the label for to. Unlike the GitHub adapter's label
// add/remove pair, the local file has a single scalar state field, so the
// transition is a plain overwrite.
func (lt *LocalTracker) TransitionState(num string, from, to forge.DispatchState) error {
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

// CompleteVerdict rewrites issue num's frontmatter "state" marker from the
// InProgress label to verdict's terminal label — the local adapter's single
// scalar state field, same shape as TransitionState's overwrite. It errors
// without touching the file when no state is configured for verdict (the
// work-kind construction path), rather than overwriting frontmatter.State
// with an empty string.
//
// Before writing, it asserts num's current state is InProgress — mirroring
// the github adapter's #701 double-dispatch guard — and errors without
// touching the file when it's not. This is check-then-write, not atomic
// compare-and-swap: it narrows the double-dispatch window without closing
// it, the same caveat exec.go's CompleteVerdict documents.
func (lt *LocalTracker) CompleteVerdict(num string, verdict forge.Verdict) error {
	state := lt.verdictLabels.Label(verdict)
	if state == "" {
		return fmt.Errorf("local: no state configured for verdict %v", verdict)
	}
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	if want := lt.labels.Label(forge.InProgress); want != "" && li.frontmatter.State != want {
		return fmt.Errorf("local: issue %s: expected state %q, has %q", num, want, li.frontmatter.State)
	}
	li.frontmatter.State = state
	if err := os.WriteFile(lt.slugPath(num), []byte(li.render()), 0o644); err != nil {
		return fmt.Errorf("write local issue %s: %w", num, err)
	}
	return nil
}

// DepsOf returns the dependency slugs listed under issue num's "## Blocked
// by" section — always forge.DepSourceBody; the local tracker has no native
// relationship concept. Unlike ParseBlockerRefs (GitHub "#N" refs), local
// issues reference each other by filename slug, one per bullet line.
func (lt *LocalTracker) DepsOf(num string) ([]forge.Dependency, error) {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return nil, err
	}
	return forge.WithSource(parseLocalBlockers(li.body), forge.DepSourceBody), nil
}

// TouchesOf returns the declared touch-set parsed from issue num's body —
// the shared body-grammar default (forge.ParseTouchPaths); the local tracker
// has no native touch-set concept to prefer over it.
func (lt *LocalTracker) TouchesOf(num string) ([]string, error) {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return nil, err
	}
	return forge.ParseTouchPaths(li.body), nil
}

// parseLocalBlockers extracts dependency slugs from a "## Blocked by" section
// (reusing blockers.go's header/heading detection), one slug per bullet line.
func parseLocalBlockers(body string) []string {
	seen := map[string]bool{}
	var refs []string
	inSection := false
	for _, rawLine := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if forge.IsBlockedByHeader(line) {
			inSection = true
			continue
		}
		if forge.IsAnyHeading(line) {
			inSection = false
		}
		if inSection && forge.IsBulletItem(line) {
			// Sentinel check runs after backtick-stripping (unlike
			// ParseBlockerRefs, which checks the raw bullet content) so a
			// backtick-quoted "`None`" slug is also recognised as the
			// sentinel — real slugs are never backtick-quoted "None", so
			// this only widens sentinel recognition, never narrows it.
			slug := strings.Trim(forge.ExtractBulletContent(line), "`")
			if forge.IsSentinelBullet(slug) {
				continue
			}
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
	li.body = forge.AppendComment(li.body, body)
	if err := os.WriteFile(lt.slugPath(num), []byte(li.render()), 0o644); err != nil {
		return fmt.Errorf("write local issue %s: %w", num, err)
	}
	return nil
}

// RecordLanding persists landing as issue num's immutable landing:
// frontmatter field (forge.LandingRecorder, ADR 0029) — only the local
// adapter implements this optional method.
func (lt *LocalTracker) RecordLanding(num, landing string) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.frontmatter.Landing = landing
	if err := os.WriteFile(lt.slugPath(num), []byte(li.render()), 0o644); err != nil {
		return fmt.Errorf("write local issue %s: %w", num, err)
	}
	return nil
}

// CloseIssue marks issue num closed by setting the closed: frontmatter field
// (forge.IssueCloser, ADR 0029) — only the local adapter implements this
// optional method; reconcile is its sole caller.
func (lt *LocalTracker) CloseIssue(num string) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.frontmatter.Closed = true
	if err := os.WriteFile(lt.slugPath(num), []byte(li.render()), 0o644); err != nil {
		return fmt.Errorf("write local issue %s: %w", num, err)
	}
	return nil
}

// FlagAbandoned marks issue num abandoned by setting the abandoned:
// frontmatter field (forge.AbandonedFlagger, ADR 0029) — only the local
// adapter implements this optional method; reconcile is its sole caller,
// invoked when the issue's landing PR closed without merging.
func (lt *LocalTracker) FlagAbandoned(num string) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.frontmatter.Abandoned = true
	if err := os.WriteFile(lt.slugPath(num), []byte(li.render()), 0o644); err != nil {
		return fmt.Errorf("write local issue %s: %w", num, err)
	}
	return nil
}

// PostIssue implements forge.HostPostedIssueFiler: it files a new issue as a
// Markdown+frontmatter file slugified from title, with State left empty (an
// untriaged issue carries no dispatch-state marker until a human — or the
// filing convention itself — applies one via labels), Labels set from the
// labels arg, and Created stamped with the current time. If the derived slug
// already exists, it appends a "-2", "-3", ... suffix (never overwriting an
// existing issue file). It returns a "local:<slug>" reference, mirroring the
// filename-based identifier every other LocalTracker method takes as num.
func (lt *LocalTracker) PostIssue(title, body string, labels []string) (string, error) {
	if err := os.MkdirAll(lt.dir, 0o755); err != nil {
		return "", fmt.Errorf("create local issues dir %s: %w", lt.dir, err)
	}
	slug, err := lt.uniqueSlug(slugify(title))
	if err != nil {
		return "", err
	}
	li := localIssue{
		frontmatter: localFrontmatter{
			Title:   title,
			Labels:  labels,
			Created: time.Now().Format(time.RFC3339),
		},
		body: body,
	}
	if err := os.WriteFile(lt.slugPath(slug), []byte(li.render()), 0o644); err != nil {
		return "", fmt.Errorf("write local issue %s: %w", slug, err)
	}
	return "local:" + slug, nil
}

// uniqueSlug returns base, or base with a "-2", "-3", ... suffix appended,
// whichever is the first that has no existing issue file — so PostIssue
// never clobbers an issue that already occupies base's slug path. The
// stat-then-write is TOCTOU-racy and so only collision-safe under the
// single-process, sequential settle relay that drives it, not concurrent
// callers.
func (lt *LocalTracker) uniqueSlug(base string) (string, error) {
	slug := base
	for n := 2; ; n++ {
		_, err := os.Stat(lt.slugPath(slug))
		if os.IsNotExist(err) {
			return slug, nil
		}
		if err != nil {
			return "", fmt.Errorf("stat local issue %s: %w", slug, err)
		}
		slug = fmt.Sprintf("%s-%d", base, n)
	}
}

// slugify derives a filename-safe slug from an issue title: lowercase,
// runs of whitespace/underscore/hyphen collapse to a single hyphen, any
// remaining character outside [a-z0-9-] is stripped, and leading/trailing
// hyphens are trimmed.
func slugify(title string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r == ' ' || r == '_' || r == '-' || r == '\t' || r == '\n':
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		default:
			// stripped
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		// A title with no [a-z0-9] characters (e.g. all punctuation)
		// slugifies to empty; fall back so we never write a bare ".md".
		return "issue"
	}
	return slug
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
	fmt.Fprintf(&b, "title: %s\n", renderScalar(li.frontmatter.Title))
	fmt.Fprintf(&b, "state: %s\n", renderScalar(li.frontmatter.State))
	// Each label is escaped through renderScalar before joining: a comma,
	// bracket, or newline in a label would otherwise fragment the flow-list
	// or inject extra frontmatter lines. PostIssue's labels arg is
	// caller-supplied (issue #2018), not hard-coded, so this must hold for
	// arbitrary label content, not just the settle relay's own labels.
	renderedLabels := make([]string, len(li.frontmatter.Labels))
	for i, l := range li.frontmatter.Labels {
		renderedLabels[i] = renderLabel(l)
	}
	fmt.Fprintf(&b, "labels: [%s]\n", strings.Join(renderedLabels, ", "))
	fmt.Fprintf(&b, "created: %s\n", renderScalar(li.frontmatter.Created))
	if li.frontmatter.Parent != "" {
		fmt.Fprintf(&b, "parent: %s\n", renderScalar(li.frontmatter.Parent))
	}
	if li.frontmatter.Closed {
		b.WriteString("closed: true\n")
	}
	if li.frontmatter.Landing != "" {
		fmt.Fprintf(&b, "landing: %s\n", renderScalar(li.frontmatter.Landing))
	}
	if li.frontmatter.Abandoned {
		b.WriteString("abandoned: true\n")
	}
	b.WriteString(frontmatterDelim + "\n")
	b.WriteString(li.body)
	return b.String()
}

// scalarNeedsQuoting reports whether s must be double-quoted to render as a
// single YAML "key: value" line: leading/trailing whitespace, an embedded
// newline or carriage return (both of which would otherwise fragment into
// extra physical lines parseLocalIssue re-reads as frontmatter — the
// injection this guards against), or a leading quote character that would
// otherwise be misread as opening a quoted scalar. Plain values like "Fix
// the Thing" and RFC3339 timestamps stay bare, unchanged from before.
//
// Scope: this guards only parseLocalIssue's own re-read vectors, not a
// general YAML reader. A value with a leading "#", "[", or ":" stays bare
// and would mis-parse under a real YAML parser — acceptable because the
// custom parser here reads the whole "key: value" line verbatim.
func scalarNeedsQuoting(s string) bool {
	return s != strings.TrimSpace(s) ||
		strings.ContainsAny(s, "\n\r") ||
		strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "'")
}

// renderScalar returns s as a bare YAML scalar when it needs no quoting, or
// a double-quoted, backslash-escaped scalar otherwise (see
// scalarNeedsQuoting) — the write side of unquote's decode.
func renderScalar(s string) string {
	if !scalarNeedsQuoting(s) {
		return s
	}
	return quoteScalar(s)
}

// labelNeedsQuoting reports whether s must be double-quoted to render as one
// element of a "labels: [...]" flow-list: everything scalarNeedsQuoting
// already guards against, plus a comma (the flow-list's own element
// separator) or a bracket (which would otherwise read as nesting or closing
// the list) — the vectors specific to a flow-list element rather than a bare
// "key: value" scalar.
func labelNeedsQuoting(s string) bool {
	return scalarNeedsQuoting(s) || strings.ContainsAny(s, ",[]")
}

// renderLabel returns s as a bare flow-list element when it needs no
// quoting, or a double-quoted, backslash-escaped element otherwise (see
// labelNeedsQuoting) — the write side of parseFlowList's decode.
func renderLabel(s string) string {
	if !labelNeedsQuoting(s) {
		return s
	}
	return quoteScalar(s)
}

// quoteScalar double-quotes s, backslash-escaping the characters unquote
// decodes: the shared quoting body for both renderScalar and renderLabel.
func quoteScalar(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// unquote strips a single layer of matching single or double quotes,
// decoding renderScalar's backslash escapes when the layer is double-quoted
// (single-quoted values are a literal strip, matching YAML's single-quote
// semantics for the values this adapter ever writes).
func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	if s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	if s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	inner := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] != '\\' || i == len(inner)-1 {
			b.WriteByte(inner[i])
			continue
		}
		i++
		switch inner[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		default:
			b.WriteByte(inner[i])
		}
	}
	return b.String()
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
	parts := splitFlowListElements(s)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = unquote(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitFlowListElements splits s on commas that separate flow-list elements,
// skipping commas inside a quoted element (renderLabel's escaping) so a
// label like "a,b" round-trips as one element rather than fragmenting on its
// embedded comma — unlike a blind strings.Split(s, ","), which cannot tell
// an element-separating comma from one quoted-escaped inside an element.
func splitFlowListElements(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == '\\' && quote == '"' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}
