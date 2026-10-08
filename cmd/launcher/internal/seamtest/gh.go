package seamtest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// GhConfig is the gh fake's JSON config. The typed Issues and PRs answer the
// reads a launcher run makes between discovery and settle; Replies are the
// escape hatch for anything they do not cover and win over them.
type GhConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// Issues are the tracker's issues; `issue list` and `issue view` answer
	// from them.
	Issues []GhIssue `json:"issues"`
	// PRs are the forge's pull requests; `pr list`, `pr view` and the GraphQL
	// check, failure-detail and mergeable probes answer from them.
	PRs []GhPR `json:"prs"`
	// Replies are tried in order before the typed state; the first whose Args
	// match answers. An invocation nothing answers succeeds silently, like
	// the shell fake's no-op subcommands (`issue comment`, `pr create`, ...).
	Replies []GhReply `json:"replies"`
}

// GhIssue is one tracker issue. State defaults to OPEN. Labels are the
// initial set: the fake replays the recorded `issue edit` and `issue close`
// calls on top of it, because settle re-reads labels after a claim edited
// them.
type GhIssue struct {
	Number   int         `json:"number"`
	Title    string      `json:"title"`
	Body     string      `json:"body"`
	State    string      `json:"state"`
	Labels   []string    `json:"labels"`
	Comments []GhComment `json:"comments"`
}

// GhComment is one issue comment, oldest first.
type GhComment struct {
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	Body      string `json:"body"`
}

// GhPR is one pull request. State defaults to OPEN and flips to MERGED once
// a `pr merge` of its URL (not --auto) is recorded. Checks is the head
// commit's statusCheckRollup state (SUCCESS, FAILURE, PENDING, or empty for
// none); Mergeable is the GraphQL mergeable value. Contexts is the raw JSON
// array of check nodes the failure-detail query returns (empty means none).
type GhPR struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	HeadRefName string `json:"head_ref_name"`
	BaseRefName string `json:"base_ref_name"`
	HeadRefOid  string `json:"head_ref_oid"`
	State       string `json:"state"`
	Checks      string `json:"checks"`
	Mergeable   string `json:"mergeable"`
	Contexts    string `json:"contexts"`
}

// GhReply scripts the answer to the invocations it matches.
type GhReply struct {
	// Args match when each element appears in the invocation's argv, in
	// order but not necessarily adjacent, so a reply can name
	// {"issue", "view", "7", "number,title,body,state,labels"} and ignore
	// the --repo flag between them.
	Args   []string `json:"args"`
	Stdout string   `json:"stdout"`
	Exit   int      `json:"exit"`
}

func ghMain(args []string) int {
	return ghFake(args, os.Stdout, os.Stderr)
}

func ghFake(args []string, stdout, stderr io.Writer) int {
	var cfg GhConfig
	if err := loadConfig("gh", &cfg); err != nil {
		fmt.Fprintf(stderr, "gh fake: %v\n", err)
		return fakeConfigExit
	}
	prior, err := appendRecord(cfg.Record, args)
	if err != nil {
		fmt.Fprintf(stderr, "gh fake: record: %v\n", err)
		return fakeConfigExit
	}
	for _, r := range cfg.Replies {
		if containsInOrder(args, r.Args) {
			io.WriteString(stdout, r.Stdout)
			return r.Exit
		}
	}
	out, err := cfg.answer(args, prior)
	if err != nil {
		fmt.Fprintf(stderr, "gh fake: %v\n", err)
		return 1
	}
	io.WriteString(stdout, out)
	return 0
}

func containsInOrder(args, want []string) bool {
	i := 0
	for _, a := range args {
		if i < len(want) && a == want[i] {
			i++
		}
	}
	return i == len(want)
}

// answer serves the typed state; prior is the argv history, which carries the
// mutations. An unrecognised call answers empty and succeeds.
func (c GhConfig) answer(args []string, prior [][]string) (string, error) {
	switch {
	case hasPrefix(args, "issue", "list"):
		return c.issueList(args, prior)
	case hasPrefix(args, "issue", "view") && len(args) > 2:
		return c.issueView(args, prior)
	case hasPrefix(args, "pr", "list"):
		return c.prList(args, prior)
	case hasPrefix(args, "pr", "view") && len(args) > 2:
		return c.prView(args, prior)
	case hasPrefix(args, "api", "graphql"):
		return c.graphql(args, prior)
	}
	return "", nil
}

func hasPrefix(args []string, want ...string) bool {
	return len(args) >= len(want) && containsInOrder(args[:len(want)], want)
}

// flagValues returns every value following name in args.
func flagValues(args []string, name string) []string {
	var vs []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			vs = append(vs, args[i+1])
		}
	}
	return vs
}

func flagValue(args []string, name string) string {
	if vs := flagValues(args, name); len(vs) > 0 {
		return vs[len(vs)-1]
	}
	return ""
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// issueState replays the recorded label edits and close onto the issue.
func (c GhConfig) issueState(i GhIssue, prior [][]string) (labels []string, state string) {
	labels = append(labels, i.Labels...)
	state = strings.ToUpper(orDefault(i.State, string(forge.IssueOpen)))
	num := strconv.Itoa(i.Number)
	for _, a := range prior {
		switch {
		case hasPrefix(a, "issue", "edit") && len(a) > 2 && a[2] == num:
			for _, rm := range flagValues(a, "--remove-label") {
				kept := labels[:0]
				for _, l := range labels {
					if l != rm {
						kept = append(kept, l)
					}
				}
				labels = kept
			}
			for _, add := range flagValues(a, "--add-label") {
				if !slices.Contains(labels, add) {
					labels = append(labels, add)
				}
			}
		case hasPrefix(a, "issue", "close") && len(a) > 2 && a[2] == num:
			state = string(forge.IssueClosed)
		}
	}
	return labels, state
}

func ghLabels(names []string) []map[string]string {
	out := []map[string]string{}
	for _, n := range names {
		out = append(out, map[string]string{"name": n})
	}
	return out
}

// issueFields builds the object `gh issue ... --json fields` emits.
func (c GhConfig) issueFields(i GhIssue, fields string, prior [][]string) (map[string]any, error) {
	labels, state := c.issueState(i, prior)
	all := map[string]func() any{
		"number": func() any { return i.Number },
		"title":  func() any { return i.Title },
		"body":   func() any { return i.Body },
		"state":  func() any { return state },
		"labels": func() any { return ghLabels(labels) },
		"comments": func() any {
			cs := []map[string]any{}
			for _, cm := range i.Comments {
				cs = append(cs, map[string]any{"author": map[string]string{"login": cm.Author}, "createdAt": cm.CreatedAt, "body": cm.Body})
			}
			return cs
		},
	}
	obj := map[string]any{}
	for _, f := range strings.Split(fields, ",") {
		fn, ok := all[f]
		if !ok {
			return nil, fmt.Errorf("issue --json: unsupported field %q", f)
		}
		obj[f] = fn()
	}
	return obj, nil
}

func (c GhConfig) issueList(args []string, prior [][]string) (string, error) {
	wantState := strings.ToUpper(orDefault(flagValue(args, "--state"), "open"))
	wantLabels := flagValues(args, "--label")
	rows := []map[string]any{}
	for _, i := range c.Issues {
		labels, state := c.issueState(i, prior)
		if wantState != "ALL" && state != wantState {
			continue
		}
		ok := true
		for _, l := range wantLabels {
			ok = ok && slices.Contains(labels, l)
		}
		if !ok {
			continue
		}
		row, err := c.issueFields(i, orDefault(flagValue(args, "--json"), "number"), prior)
		if err != nil {
			return "", err
		}
		rows = append(rows, row)
	}
	return marshalLine(rows)
}

func (c GhConfig) issueView(args []string, prior [][]string) (string, error) {
	for _, i := range c.Issues {
		if strconv.Itoa(i.Number) == args[2] {
			obj, err := c.issueFields(i, orDefault(flagValue(args, "--json"), "number"), prior)
			if err != nil {
				return "", err
			}
			return marshalLine(obj)
		}
	}
	return "", fmt.Errorf("issue %s not found", args[2])
}

func marshalLine(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b) + "\n", err
}

// prState is the PR's state with a recorded `pr merge` of its URL applied.
func (c GhConfig) prState(p GhPR, prior [][]string) string {
	for _, a := range prior {
		if hasPrefix(a, "pr", "merge") && len(a) > 2 && a[2] == p.URL && !slices.Contains(a, "--auto") {
			return string(forge.PRMerged)
		}
	}
	return strings.ToUpper(orDefault(p.State, string(forge.PROpen)))
}

func (c GhConfig) prList(args []string, prior [][]string) (string, error) {
	head := flagValue(args, "--head")
	wantState := strings.ToUpper(orDefault(flagValue(args, "--state"), "open"))
	rows := []map[string]string{}
	for _, p := range c.PRs {
		if p.HeadRefName == head && (wantState == "ALL" || c.prState(p, prior) == wantState) {
			rows = append(rows, map[string]string{"url": p.URL})
		}
	}
	// The launcher's only `pr list` shape pipes --json url through
	// `.[0].url // ""`; gh applies the jq, so the fake emits its result.
	if jq := flagValue(args, "--jq"); jq != "" {
		if jq != `.[0].url // ""` {
			return "", fmt.Errorf("pr list: unsupported --jq %q", jq)
		}
		if len(rows) == 0 {
			return "\n", nil
		}
		return rows[0]["url"] + "\n", nil
	}
	return marshalLine(rows)
}

func (c GhConfig) pr(ref string) (GhPR, error) {
	for _, p := range c.PRs {
		if p.URL == ref || strconv.Itoa(p.Number) == ref {
			return p, nil
		}
	}
	return GhPR{}, fmt.Errorf("pull request %s not found", ref)
}

func (c GhConfig) prView(args []string, prior [][]string) (string, error) {
	p, err := c.pr(args[2])
	if err != nil {
		return "", err
	}
	fields := map[string]string{
		"state":       c.prState(p, prior),
		"headRefOid":  p.HeadRefOid,
		"headRefName": p.HeadRefName,
		"baseRefName": p.BaseRefName,
		"url":         p.URL,
	}
	// gh applies --jq; the launcher uses `.field` and a head/base tsv.
	switch jq := flagValue(args, "--jq"); {
	case jq == "[.headRefName,.baseRefName]|@tsv":
		return fields["headRefName"] + "\t" + fields["baseRefName"] + "\n", nil
	case strings.HasPrefix(jq, ".") && !strings.ContainsAny(jq[1:], ".[|/ "):
		v, ok := fields[jq[1:]]
		if !ok {
			return "", fmt.Errorf("pr view: unsupported --jq %q", jq)
		}
		return v + "\n", nil
	case jq != "":
		return "", fmt.Errorf("pr view: unsupported --jq %q", jq)
	}
	obj := map[string]any{}
	for _, f := range strings.Split(flagValue(args, "--json"), ",") {
		v, ok := fields[f]
		if !ok {
			return "", fmt.Errorf("pr view --json: unsupported field %q", f)
		}
		obj[f] = v
	}
	return marshalLine(obj)
}

// graphql answers the four GraphQL probes the launcher makes, telling them
// apart by the query text; gh applies each call's --jq, so the fake emits the
// extracted value.
func (c GhConfig) graphql(args []string, prior [][]string) (string, error) {
	var query string
	for _, v := range flagValues(args, "-f") {
		if q, ok := strings.CutPrefix(v, "query="); ok {
			query = q
		}
	}
	if strings.Contains(query, "autoMergeAllowed") {
		return "false\n", nil
	}
	p, err := c.pr(strings.TrimPrefix(flagValue(args, "-F"), "number="))
	if err != nil {
		return "", err
	}
	switch {
	// The failure-detail query also names statusCheckRollup, so it must match first.
	case strings.Contains(query, "contexts("):
		if p.Contexts == "" {
			return "[]\n", nil
		}
		return p.Contexts + "\n", nil
	case strings.Contains(query, "statusCheckRollup"):
		return p.Checks + "\n", nil
	case strings.Contains(query, "mergeable"):
		return p.Mergeable + "\n", nil
	}
	return "", nil
}
