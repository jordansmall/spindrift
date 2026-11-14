package nuget

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

func indexRow(t *testing.T) registryvocab.RewriteRow {
	t.Helper()
	if len(Strategy.RewriteRows) == 0 {
		t.Fatal("nuget Strategy has no RewriteRows")
	}
	return Strategy.RewriteRows[0]
}

func rewriteContext() registryvocab.RewriteContext {
	fwd, _ := url.Parse("http://127.0.0.1:8123")
	return registryvocab.RewriteContext{MatchHost: "feed.example.test", Forwarder: fwd, Prefix: "feed-example-test"}
}

func TestServiceIndexMatches(t *testing.T) {
	row := indexRow(t)
	const base = "/api/nuget/v3/repo"
	cases := []struct {
		path, base string
		want       bool
	}{
		{base + "/index.json", base, true},
		{base, base, true},
		{"/index.json", "/", true},
		{base + "/registration/foo/index.json", base, false},
		{base + "/v3-flatcontainer/foo/index.json", base, false},
		{base + "/index.json/extra", base, false},
		{"/index.json", base, false},
		{"/other/index.json", "/", false},
	}
	for _, c := range cases {
		if got := row.Matches(c.path, c.base); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.path, c.base, got, c.want)
		}
	}
}

func resourceIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var doc struct {
		Resources []struct {
			ID string `json:"@id"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, body)
	}
	var ids []string
	for _, r := range doc.Resources {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestRewriteServiceIndex(t *testing.T) {
	const in = `{"version":"3.0.0","resources":[
 {"@id":"https://feed.example.test/api/nuget/v3/repo/flat/","@type":"PackageBaseAddress/3.0.0"},
 {"@id":"https://feed.example.test/api/nuget/v3/repo/readme/{lower_id}/{lower_version}/readme","@type":"ReadmeUriTemplate/6.13.0"},
 {"@id":"https://feed.example.test/api/nuget/v3/repo/sig","@type":"RepositorySignatures/5.0.0"},
 {"@id":"https://feed.example.test/api/nuget/v3/repo/sig2","@type":["Other","RepositorySignatures/4.9.0"]},
 {"@id":"https://feed.example.test/api/nuget/v3/repo/sig3","@type":"RepositorySignatures"},
 {"@id":"https://www.nuget.org/packages/{id}/{version}","@type":"PackageDetailsUriTemplate/5.1.0"}
]}`
	res := indexRow(t).Rewrite([]byte(in), rewriteContext())
	if res.Outcome != registryvocab.RewriteApplied {
		t.Fatalf("outcome = %v, want applied", res.Outcome)
	}
	want := []string{
		"http://127.0.0.1:8123/feed-example-test/api/nuget/v3/repo/flat/",
		"http://127.0.0.1:8123/feed-example-test/api/nuget/v3/repo/readme/{lower_id}/{lower_version}/readme",
		"https://www.nuget.org/packages/{id}/{version}",
	}
	got := resourceIDs(t, res.Body)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ids = %q, want %q", got, want)
	}
	if strings.HasSuffix(string(res.Body), "\n") {
		t.Errorf("body carries a trailing newline")
	}
	var declined int
	for _, e := range res.Edits {
		if e.To == "" {
			declined++
		} else if e.LearnedPath == "" {
			t.Errorf("applied edit %q has empty LearnedPath", e.From)
		}
	}
	if declined != 1 {
		t.Errorf("declined edits = %d, want 1 (the foreign host)", declined)
	}
}

func TestRewriteServiceIndex_ForeignOnlyIsSkipped(t *testing.T) {
	const in = `{"resources":[{"@id":"https://www.nuget.org/v3-flatcontainer/","@type":"PackageBaseAddress/3.0.0"}]}`
	res := indexRow(t).Rewrite([]byte(in), rewriteContext())
	if res.Outcome != registryvocab.RewriteSkippedForeignHost {
		t.Fatalf("outcome = %v, want skipped foreign host", res.Outcome)
	}
	if string(res.Body) != in {
		t.Errorf("body changed: %s", res.Body)
	}
}

func TestRewriteServiceIndex_DroppingSignaturesAloneIsApplied(t *testing.T) {
	const in = `{"resources":[{"@id":"https://www.nuget.org/sig","@type":"RepositorySignatures/5.0.0"}]}`
	res := indexRow(t).Rewrite([]byte(in), rewriteContext())
	if res.Outcome != registryvocab.RewriteApplied {
		t.Fatalf("outcome = %v, want applied", res.Outcome)
	}
	if ids := resourceIDs(t, res.Body); len(ids) != 0 {
		t.Errorf("resources = %q, want none", ids)
	}
}

func TestRewriteServiceIndex_Unrecognisable(t *testing.T) {
	for _, in := range []string{
		`not json`,
		`{"resources":[]} trailing`,
		`[]`,
		`{"version":"3.0.0"}`,
		`{"resources":"nope"}`,
		`{"resources":[{"@type":"x"},"str",{"@id":7}]}`,
	} {
		res := indexRow(t).Rewrite([]byte(in), rewriteContext())
		if res.Outcome != registryvocab.RewriteNone || string(res.Body) != in {
			t.Errorf("%q: outcome %v body %q, want none and untouched", in, res.Outcome, res.Body)
		}
	}
}
