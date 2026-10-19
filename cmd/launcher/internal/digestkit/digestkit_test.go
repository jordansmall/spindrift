package digestkit

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// failures embeds a testing.TB so a guard can run against it while the test
// asserts on what the guard reported, instead of failing itself.
type failures struct {
	testing.TB
	errs []string
}

func (f *failures) Errorf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}

func TestRunReplaysCorpusIntoGoldens(t *testing.T) {
	var got Case
	calls := 0
	render := func(_ testing.TB, c Case) (string, string) {
		got = c
		calls++
		var b strings.Builder
		fmt.Fprintf(&b, "cursor=%s now=%s min=%d max=%d\n", c.Cursor, c.Now.Format(time.RFC3339), c.MinSample, c.MaxBytes)
		fmt.Fprintf(&b, "prior=%s\n", c.Prior)
		urls := make([]string, 0, len(c.Merged))
		for u := range c.Merged {
			urls = append(urls, u)
		}
		sort.Strings(urls)
		for _, u := range urls {
			fmt.Fprintf(&b, "merged %s -> %s\n", u, c.Merged[u])
		}
		for _, r := range c.Records {
			fmt.Fprintf(&b, "record %s %s %s\n", r.ID, r.DispatchKey, r.Outcome)
		}
		return b.String(), c.Records[len(c.Records)-1].ID
	}

	corpus := filepath.Join("testdata", "corpus")
	Run(t, render, corpus)

	if want := len(caseDirs(t, corpus)); calls != want {
		t.Fatalf("renderer called %d times, want once per case (%d)", calls, want)
	}
	if got.Cursor != "rec-0" || got.MinSample != 2 || got.MaxBytes != 4096 {
		t.Errorf("limits not decoded: %+v", got)
	}
	if !got.Now.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("now = %v", got.Now)
	}
	if got.Merged["https://forge.example/pr/7"] != "abc123" {
		t.Errorf("merged = %v", got.Merged)
	}
	if !strings.Contains(string(got.Prior), "slow-ci") {
		t.Errorf("prior = %s", got.Prior)
	}
	if len(got.Records) != 2 || got.Records[0].ID != "rec-1" || got.Records[0].PRURL == "" {
		t.Errorf("records = %+v", got.Records)
	}
}

func TestLoadCaseRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"case.json": `{"cursr": "x"}`, "records.json": `[]`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadCase(dir); err == nil {
		t.Fatal("a typo'd case.json key must fail to load")
	}
}

func TestDecodeStrictRejectsTrailingContent(t *testing.T) {
	var c Case
	if err := DecodeStrict([]byte(`{"cursor": "a"}`), &c); err != nil {
		t.Fatalf("single object: %v", err)
	}
	for name, body := range map[string]string{
		"second object": `{"cursor": "a"} {"cursor": "b"}`,
		"stray text":    `{"cursor": "a"} x`,
	} {
		if err := DecodeStrict([]byte(body), &c); err == nil {
			t.Errorf("%s: trailing content must fail", name)
		}
	}
}

func TestCaseDirsFailsWhenCorpusHasNoCase(t *testing.T) {
	f := &failures{TB: t}
	if got := caseDirs(f, t.TempDir()); len(got) != 0 {
		t.Fatalf("caseDirs = %v, want none", got)
	}
	if len(f.errs) == 0 {
		t.Fatal("an empty corpus directory must fail")
	}

	f = &failures{TB: t}
	if got := caseDirs(f, filepath.Join("testdata", "corpus")); len(got) != 1 || got[0] != "basic" || len(f.errs) != 0 {
		t.Fatalf("caseDirs = %v, errs %v", got, f.errs)
	}
}
