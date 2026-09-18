package local

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

const frontmatterDelim = "---"

// localFrontmatter is the YAML frontmatter block of a local issue file (ADR 0013).
type localFrontmatter struct {
	Title   string
	State   string
	Labels  []string
	Created string
	Parent  string
	// Closed is the local-only open/closed axis (ADR 0029), independent of the
	// dispatch State marker. Absent or false means open.
	Closed bool
	// Landing is an immutable PR URL or push-only branch ref (ADR 0029), never
	// a cached merge-state.
	Landing string
	// LandingPass and LandingPassKind are advisory provenance for Landing
	// (issue #2983). Zero or empty when never recorded, or when Landing
	// predates these fields.
	LandingPass     int
	LandingPassKind string
	// Abandoned means the landing PR closed without merging (ADR 0029).
	Abandoned bool
}

type localIssue struct {
	frontmatter localFrontmatter
	body        string
}

// parseLocalIssue splits data into its YAML frontmatter block and Markdown
// body. It reads only scalar "key: value" lines and a "labels: [a, b]" flow
// list, so the launcher module stays stdlib-only (lib/mkHarness.nix).
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
		case "landingpass":
			fm.LandingPass, _ = strconv.Atoi(unquote(val))
		case "landingpasskind":
			fm.LandingPassKind = unquote(val)
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

// LocalTracker is the file-based forge.IssueTracker adapter (ADR 0013), one
// Markdown file per issue in a git-ignored directory. labels maps
// forge.DispatchState values to the frontmatter "state" marker, the way the
// GitHub adapter maps them to label names.
type LocalTracker struct {
	dir           string
	labels        forge.DispatchLabels
	verdictLabels forge.VerdictLabels
}

var _ forge.HostPostedIssueFiler = (*LocalTracker)(nil)
var _ forge.HostPostedCommenter = (*LocalTracker)(nil)

// NewLocalTracker returns a forge.IssueTracker backed by issue files in dir.
// verdictLabels configures CompleteVerdict for the research dispatch kind, and
// work-kind construction sites omit it.
func NewLocalTracker(dir string, labels forge.DispatchLabels, verdictLabels ...forge.VerdictLabels) *LocalTracker {
	var vl forge.VerdictLabels
	if len(verdictLabels) > 0 {
		vl = verdictLabels[0]
	}
	return &LocalTracker{dir: dir, labels: labels, verdictLabels: vl}
}

// slugPath returns the file path for issue num, accepting only a bare filename
// directly inside lt.dir. Comparing the cleaned path's base against num+".md"
// rejects any id carrying a separator, escaping ("../x") or not ("a/../b").
// The "", "." and ".." ids pass that comparison (they name ".md", "..md",
// "...md"), so the guard also rejects them by name.
func (lt *LocalTracker) slugPath(num string) (string, error) {
	path := filepath.Clean(filepath.Join(lt.dir, num+".md"))
	if num == "" || num == "." || num == ".." || filepath.Base(path) != num+".md" {
		return "", fmt.Errorf("local: issue %q: id must be a bare filename directly inside %s", num, lt.dir)
	}
	return path, nil
}

func (lt *LocalTracker) readIssueFile(num string) (localIssue, error) {
	path, err := lt.slugPath(num)
	if err != nil {
		return localIssue{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return localIssue{}, fmt.Errorf("read local issue %s: %w", num, err)
	}
	li, err := parseLocalIssue(data)
	if err != nil {
		return localIssue{}, fmt.Errorf("parse local issue %s: %w", num, err)
	}
	return li, nil
}

func (lt *LocalTracker) writeIssueFile(num string, li localIssue) error {
	path, err := lt.slugPath(num)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(li.render()), 0o644); err != nil {
		return fmt.Errorf("write local issue %s: %w", num, err)
	}
	return nil
}

// toIssue converts a parsed local issue file into the launcher's Issue type.
// It appends the frontmatter dispatch-state marker to Labels so cross-backend
// checks for a dispatch label behave as they do against the GitHub adapter,
// whose Labels already carry the state label. An empty State is skipped, so
// Labels never gains a stray "" element.
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

// StateLabels implements forge.LabeledTracker.
func (lt *LocalTracker) StateLabels() forge.DispatchLabels {
	return lt.labels
}

// ListIssues returns open issues whose frontmatter state marker matches state,
// oldest created first.
func (lt *LocalTracker) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	want := lt.labels.Label(state)
	return lt.listIssues(func(li localIssue) bool { return !li.frontmatter.Closed && li.frontmatter.State == want })
}

// ListOpenIssues returns every non-closed issue file in dir whatever its state
// marker, oldest created first.
func (lt *LocalTracker) ListOpenIssues() ([]forge.Issue, error) {
	return lt.listIssues(func(li localIssue) bool { return !li.frontmatter.Closed })
}

// AllIssues returns every issue file in dir, oldest created first, whatever
// its parent or state (forge.SeamLister).
func (lt *LocalTracker) AllIssues() ([]forge.Issue, error) {
	return lt.listIssues(func(localIssue) bool { return true })
}

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
		if _, err := lt.slugPath(num); err != nil {
			continue
		}
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

// TransitionState rewrites issue num's frontmatter "state" marker to the label
// for to. The local file has a single scalar state field, so the transition is
// a plain overwrite rather than the GitHub adapter's label add/remove pair.
func (lt *LocalTracker) TransitionState(num string, from, to forge.DispatchState) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.frontmatter.State = lt.labels.Label(to)
	return lt.writeIssueFile(num, li)
}

// CompleteVerdict rewrites issue num's "state" marker to verdict's terminal
// label. It errors without touching the file when verdict has no configured
// state (the work-kind construction path) or when num is not InProgress, the
// github adapter's #701 double-dispatch guard. Check-then-write narrows the
// double-dispatch window without closing it, as exec.go's version also notes.
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
	return lt.writeIssueFile(num, li)
}

// DepsOf returns the dependency slugs under issue num's "## Blocked by"
// section, always forge.DepSourceBody because the local tracker has no native
// relationship concept. Local issues reference each other by filename slug,
// not by the GitHub "#N" refs ParseBlockerRefs reads.
func (lt *LocalTracker) DepsOf(num string) ([]forge.Dependency, error) {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return nil, err
	}
	return forge.WithSource(parseLocalBlockers(li.body), forge.DepSourceBody), nil
}

// TouchesOf returns the touch-set parsed from issue num's body with the shared
// body grammar, because the local tracker has no native touch-set concept.
func (lt *LocalTracker) TouchesOf(num string) ([]string, error) {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return nil, err
	}
	return forge.ParseTouchPaths(li.body), nil
}

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
			// The sentinel check runs after backtick-stripping, unlike
			// ParseBlockerRefs, so a quoted "`None`" also reads as the
			// sentinel. Real slugs are never a backtick-quoted "None", so this
			// only widens sentinel recognition, never narrows it.
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

// Comment appends body as a bullet under issue num's "## Comments" section,
// creating that section if absent.
func (lt *LocalTracker) Comment(num, body string) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.body = forge.AppendComment(li.body, body)
	return lt.writeIssueFile(num, li)
}

// RecordLanding persists landing as issue num's landing: frontmatter field
// (forge.LandingRecorder, ADR 0029), an optional method only this adapter has.
func (lt *LocalTracker) RecordLanding(num, landing string) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.frontmatter.Landing = landing
	return lt.writeIssueFile(num, li)
}

// RecordLandingPass persists pass and kind as issue num's landingpass: and
// landingpasskind: frontmatter fields (forge.LandingPassRecorder, issue #2983).
func (lt *LocalTracker) RecordLandingPass(num string, pass int, kind string) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.frontmatter.LandingPass = pass
	li.frontmatter.LandingPassKind = kind
	return lt.writeIssueFile(num, li)
}

// CloseIssue sets issue num's closed: frontmatter field (forge.IssueCloser,
// ADR 0029). reconcile is its sole caller.
func (lt *LocalTracker) CloseIssue(num string) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.frontmatter.Closed = true
	return lt.writeIssueFile(num, li)
}

// FlagAbandoned sets issue num's abandoned: frontmatter field
// (forge.AbandonedFlagger, ADR 0029). reconcile calls it when the landing PR
// closed without merging.
func (lt *LocalTracker) FlagAbandoned(num string) error {
	li, err := lt.readIssueFile(num)
	if err != nil {
		return err
	}
	li.frontmatter.Abandoned = true
	return lt.writeIssueFile(num, li)
}

// PostIssue implements forge.HostPostedIssueFiler, filing a new issue file
// slugified from title. State stays empty because an untriaged issue carries
// no dispatch-state marker until someone applies one. A taken slug gets a
// "-2", "-3", ... suffix, never an overwrite.
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
	if err := lt.writeIssueFile(slug, li); err != nil {
		return "", err
	}
	return "local:" + slug, nil
}

// uniqueSlug returns the first of base, base-2, base-3, ... with no existing
// issue file. The stat-then-write is TOCTOU-racy, so it is collision-safe only
// under the single-process, sequential settle relay that drives it.
func (lt *LocalTracker) uniqueSlug(base string) (string, error) {
	slug := base
	for n := 2; ; n++ {
		path, err := lt.slugPath(slug)
		if err != nil {
			// Unreachable for slugify output; honours slugPath's signature.
			return "", err
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return slug, nil
		} else if err != nil {
			return "", fmt.Errorf("stat local issue %s: %w", slug, err)
		}
		slug = fmt.Sprintf("%s-%d", base, n)
	}
}

// slugify derives a filename-safe slug from an issue title.
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
		// An all-punctuation title slugifies to empty, and the fallback keeps
		// us from writing a bare ".md".
		return "issue"
	}
	return slug
}

// Probe creates the local issues directory if needed and returns its absolute
// path, the local stand-in for a resolved repo slug.
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

// ListLabels returns the four dispatch state markers, reported present
// unconditionally because the local adapter has no label registry to check
// against. AllLabels omits Recoverable as a local-only frontmatter marker that
// is never a real GitHub label, so it needs no membership check here (#2254).
func (lt *LocalTracker) ListLabels() ([]string, error) {
	return lt.labels.AllLabels(), nil
}

// CreateLabel is a no-op because the local adapter has no label registry.
func (lt *LocalTracker) CreateLabel(name, description, color string) error {
	return nil
}

// render serializes li back into frontmatter and body, inverting
// parseLocalIssue.
func (li localIssue) render() string {
	var b strings.Builder
	b.WriteString(frontmatterDelim + "\n")
	fmt.Fprintf(&b, "title: %s\n", renderScalar(li.frontmatter.Title))
	fmt.Fprintf(&b, "state: %s\n", renderScalar(li.frontmatter.State))
	// A comma, bracket, or newline in a label would fragment the flow-list or
	// inject extra frontmatter lines, so renderLabel escapes each one first.
	// PostIssue's labels arg is caller-supplied (issue #2018), so this must
	// hold for arbitrary label content.
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
	if li.frontmatter.LandingPass != 0 {
		fmt.Fprintf(&b, "landingpass: %d\n", li.frontmatter.LandingPass)
	}
	if li.frontmatter.LandingPassKind != "" {
		fmt.Fprintf(&b, "landingpasskind: %s\n", renderScalar(li.frontmatter.LandingPassKind))
	}
	if li.frontmatter.Abandoned {
		b.WriteString("abandoned: true\n")
	}
	b.WriteString(frontmatterDelim + "\n")
	b.WriteString(li.body)
	return b.String()
}

// scalarNeedsQuoting reports whether s must be double-quoted to render as one
// YAML "key: value" line. A newline or carriage return would otherwise split
// into lines parseLocalIssue re-reads as frontmatter, which is the injection
// this guards against. A colon is quoted because "key: a: b" is invalid YAML,
// even though parseLocalIssue's own first-colon split happens to survive it.
func scalarNeedsQuoting(s string) bool {
	return s != strings.TrimSpace(s) ||
		strings.ContainsAny(s, "\n\r:") ||
		strings.HasPrefix(s, `"`) || strings.HasPrefix(s, "'")
}

// renderScalar returns s bare, or double-quoted and backslash-escaped, as the
// write side of unquote's decode.
func renderScalar(s string) string {
	if !scalarNeedsQuoting(s) {
		return s
	}
	return quoteScalar(s)
}

// labelNeedsQuoting adds to scalarNeedsQuoting the cases specific to a
// "labels: [...]" element: a comma, which separates elements, and a bracket,
// which would read as nesting or closing the list.
func labelNeedsQuoting(s string) bool {
	return scalarNeedsQuoting(s) || strings.ContainsAny(s, ",[]")
}

// renderLabel returns s as a bare or quoted flow-list element, the write side
// of parseFlowList's decode.
func renderLabel(s string) string {
	if !labelNeedsQuoting(s) {
		return s
	}
	return quoteScalar(s)
}

// quoteScalar double-quotes s, backslash-escaping the characters unquote
// decodes.
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

// unquote strips one layer of matching single or double quotes, decoding
// renderScalar's backslash escapes only for a double-quoted layer, which
// matches YAML's single-quote semantics for the values this adapter writes.
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

// splitFlowListElements splits s on element-separating commas, skipping commas
// inside a quoted element so a label like "a,b" round-trips whole. A blind
// strings.Split(s, ",") cannot tell the two kinds of comma apart.
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
