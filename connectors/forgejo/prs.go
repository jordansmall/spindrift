package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

// wipPrefix is the one marker the write side produces; the read side accepts
// every default Forgejo spelling.
const wipPrefix = "WIP:"

var wipPrefixes = []string{wipPrefix, "[WIP]"}

// matchWIPPrefix reports a leading, case-insensitive WIP marker and the title
// after it with colons and spaces trimmed. isDraftTitle and stripWIPPrefix
// share it so their notions of a marker cannot drift apart.
func matchWIPPrefix(title string) (rest string, ok bool) {
	trimmed := strings.TrimSpace(title)
	upper := strings.ToUpper(trimmed)
	for _, prefix := range wipPrefixes {
		if strings.HasPrefix(upper, strings.ToUpper(prefix)) {
			return strings.TrimLeft(trimmed[len(prefix):], ": "), true
		}
	}
	return title, false
}

func isDraftTitle(title string) bool {
	_, ok := matchWIPPrefix(title)
	return ok
}

// stripWIPPrefix removes every stacked marker, so the result is never a draft
// title; a marker-only title yields "".
func stripWIPPrefix(title string) string {
	for {
		rest, ok := matchWIPPrefix(title)
		if !ok {
			return title
		}
		title = rest
	}
}

func marshal(v any) []byte {
	b, _ := json.Marshal(v) // maps of strings and bools cannot fail
	return b
}

// open creates the PR. Forgejo has no create-time draft field, so a draft is
// a WIP title prefix. A 409 (a PR for this head exists) surfaces as an error:
// core adopts the branch's open PR itself.
func open(t forge.Target, p forge.NewPR) (forge.PR, error) {
	title := p.Title
	if p.Draft {
		title = wipPrefix + " " + title
	}
	var raw rawPR
	body := marshal(map[string]string{"title": title, "head": p.Head, "base": p.Base, "body": p.Body})
	if err := call("POST", t.API, forgePath(t, "/pulls"), body, &raw); err != nil {
		return forge.PR{}, err
	}
	return raw.pr(), nil
}

// behind counts the commits the PR's base has that its head lacks. Forgejo's
// compare counts only what its head side adds, so the PR's head goes on the
// base side of head...base.
func behind(t forge.Target, number string) (int, error) {
	raw, err := readPR(t, number)
	if err != nil {
		return 0, err
	}
	head, err := branchPath(raw.Head.Ref)
	if err != nil {
		return 0, err
	}
	base, err := branchPath(raw.Base.Ref)
	if err != nil {
		return 0, err
	}
	var cmp struct {
		TotalCommits int `json:"total_commits"`
	}
	if err := call("GET", t.API, forgePath(t, "/compare/"+head+"..."+base), nil, &cmp); err != nil {
		return 0, err
	}
	return cmp.TotalCommits, nil
}

func prFiles(t forge.Target, number string) ([]string, error) {
	if _, err := parseNumber(number); err != nil {
		return nil, err
	}
	type file struct {
		Filename string `json:"filename"`
	}
	// The files list has no id to detect a repeated page by; the filename of
	// its first entry stands in, and an empty one cannot repeat falsely.
	files, err := pageUntilEmpty(t.API, forgePath(t, "/pulls/"+number+"/files"), url.Values{},
		func(f file) string { return f.Filename })
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		if f.Filename != "" {
			out = append(out, f.Filename)
		}
	}
	return out, nil
}

// setDraft flips the draft state through the title, as Forgejo has no other
// handle. A PR already in the requested state is left untouched. A title that
// strips to empty is refused: Forgejo silently ignores an empty-title PATCH,
// which would leave the PR a draft.
func setDraft(t forge.Target, number string, draft bool) error {
	raw, err := readPR(t, number)
	if err != nil {
		return err
	}
	if (raw.Draft || isDraftTitle(raw.Title)) == draft {
		return nil
	}
	title := wipPrefix + " " + raw.Title
	if !draft {
		if title = stripWIPPrefix(raw.Title); title == "" {
			return fmt.Errorf("PR %s title %q is empty once WIP markers are stripped; refusing to PATCH an empty title", number, raw.Title)
		}
	}
	return call("PATCH", t.API, forgePath(t, "/pulls/"+number), marshal(map[string]string{"title": title}), nil)
}

func comment(t forge.Target, number, body string) error {
	if _, err := parseNumber(number); err != nil {
		return err
	}
	return call("POST", t.API, forgePath(t, "/issues/"+number+"/comments"), marshal(map[string]string{"body": body}), nil)
}

// postMerge always rebases: the protocol carries no method, and rebase is the
// native adapter's default. A 405 or 409 is the forge refusing an unmergeable
// PR, reported as NotMergeable: core re-reads the PR to tell why only for that
// class, and retries only a Transient failure. enqueueAutoMerge shares it, so a
// refused enqueue reports NotMergeable too; core branches on it only for merge.
func postMerge(t forge.Target, number string, extra map[string]any) error {
	if _, err := parseNumber(number); err != nil {
		return err
	}
	body := map[string]any{"Do": "rebase", "delete_branch_after_merge": true}
	for k, v := range extra {
		body[k] = v
	}
	path := forgePath(t, "/pulls/"+number+"/merge")
	resp, err := request("POST", t.API, path, marshal(body))
	if err != nil {
		return err
	}
	switch {
	case is2xx(resp.Status):
		return nil
	case resp.Status == 405 || resp.Status == 409:
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(resp.Body, &m) == nil && m.Message != "" {
			return connector.Errorf(connector.NotMergeable, "POST %s: status %d: %s", path, resp.Status, m.Message)
		}
		return connector.Errorf(connector.NotMergeable, "POST %s: status %d", path, resp.Status)
	}
	return statusError("POST", path, resp.Status)
}

func merge(t forge.Target, number string) error { return postMerge(t, number, nil) }

// enqueueAutoMerge makes the merge endpoint queue the merge, not run it now.
func enqueueAutoMerge(t forge.Target, number string) error {
	return postMerge(t, number, map[string]any{"merge_when_checks_succeed": true})
}

// autoMergeAllowed reports whether the repository allows rebase merges, the
// style postMerge uses: Forgejo has no autoMergeAllowed flag like GitHub's.
func autoMergeAllowed(t forge.Target) (bool, error) {
	var repo struct {
		AllowRebase bool `json:"allow_rebase"`
	}
	err := call("GET", t.API, forgePath(t, ""), nil, &repo)
	return repo.AllowRebase, err
}

// branchProtected reads the branch's server-computed flag rather than
// re-matching rule names: it needs only repo read, where listing the rules
// needs admin.
func branchProtected(t forge.Target, branch string) (bool, error) {
	p, err := branchPath(branch)
	if err != nil {
		return false, err
	}
	var b struct {
		Protected bool `json:"protected"`
	}
	err = call("GET", t.API, forgePath(t, "/branches/"+p), nil, &b)
	return b.Protected, err
}

func probe(t forge.Target) (string, error) {
	var repo struct {
		FullName string `json:"full_name"`
	}
	err := call("GET", t.API, forgePath(t, ""), nil, &repo)
	return repo.FullName, err
}
