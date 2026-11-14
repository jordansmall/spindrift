package nuget

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/registryvocab"
)

func rowNamed(t *testing.T, name string) registryvocab.RewriteRow {
	t.Helper()
	for _, r := range Strategy.RewriteRows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("nuget Strategy has no %q row", name)
	return registryvocab.RewriteRow{}
}

func indexRow(t *testing.T) registryvocab.RewriteRow {
	t.Helper()
	return rowNamed(t, "nuget service index")
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

func registrationRowOf(t *testing.T) registryvocab.RewriteRow {
	t.Helper()
	return rowNamed(t, "nuget registration")
}

const registrationUpstream = "https://feed.example.test/api/v3/registration-semver2/"
const registrationForwarded = "http://127.0.0.1:8123/feed-example-test/api/v3/registration-semver2/"

// streamRegistrationDoc runs in through the registration row and returns the
// rewritten body and every edit the stream offered.
func streamRegistrationDoc(t *testing.T, in string) (string, []registryvocab.RewriteEdit) {
	t.Helper()
	var out strings.Builder
	var edits []registryvocab.RewriteEdit
	err := registrationRowOf(t).RewriteStream(&out, strings.NewReader(in), rewriteContext(), func(e registryvocab.RewriteEdit) { edits = append(edits, e) })
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), edits
}

// repointRegistrationLinks is the expected rewrite when every registration-dir
// URL in in is a registry-written link.
func repointRegistrationLinks(in string) string {
	return strings.ReplaceAll(in, registrationUpstream, registrationForwarded)
}

func TestRegistrationMatches(t *testing.T) {
	row := registrationRowOf(t)
	const base = "/api/nuget/v3/repo"
	cases := []struct {
		path, base string
		want       bool
	}{
		{base + "/registration-semver2/newtonsoft.json/index.json", base, true},
		{base + "/registration-semver2/newtonsoft.json/page/1.0.0/13.0.3.json", base, true},
		{base + "/registration5-gz-semver2/foo/index.json", base, true},
		{base + "/registration5-semver1/foo/page/0.1.0/1.0.0.json", base, true},
		{base + "/registration/foo/index.json", base, true},
		{"/registration-semver2/foo/index.json", "/", true},
		{"/registration-semver2/foo/page/1.0.0/2.0.0.json", "/", true},
		{base + "/registration-semver2/Download/foo/1.0.0", base, false},
		{base + "/registration-semver2/Download/foo/index.json", base, false},
		{base + "/index.json", base, false},
		{base, base, false},
		{base + "/flat-container/foo/index.json", base, false},
		{base + "/v3-flatcontainer/foo/1.0.0/foo.1.0.0.nupkg", base, false},
		{base + "/registration-semver2/foo/index.json/extra", base, false},
		{base + "/registration-semver2/index.json", base, false},
		{base + "/registration-semver2/foo/page/1.0.0.json", base, false},
		{base + "/registration-semver2/foo/page/1.0.0/2.0.0/x.json", base, false},
		{base + "/registration-semver2/foo/pages/1.0.0/2.0.0.json", base, false},
		{base + "/registration-semver2/foo/page/1.0.0/2.0.0", base, false},
		{base + "/other/foo/index.json", base, false},
		{"/other/registration-semver2/foo/index.json", base, false},
		{"/registration-semver2/foo/index.json", base, false},
		{"/other/registration-semver2/foo/index.json", "/", false},
	}
	for _, c := range cases {
		if got := row.Matches(c.path, c.base); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.path, c.base, got, c.want)
		}
	}
}

func TestStreamRegistration(t *testing.T) {
	const in = `{"@id":"https://feed.example.test/api/v3/registration-semver2/foo/index.json","@type":["catalog:CatalogRoot"],"count":1,` +
		`"items":[{"@id":"https://feed.example.test/api/v3/registration-semver2/foo/page/1.0.0/2.0.0.json","count":1,` +
		`"items":[{"@id":"https://feed.example.test/api/v3/registration-semver2/foo/1.0.0.json",` +
		`"catalogEntry":{"id":"Foo","version":"1.0.0","projectUrl":"https://github.com/example/foo","description":"not a url"},` +
		`"packageContent":"https://feed.example.test/api/v3/registration-semver2/Download/foo/1.0.0"}]}],` +
		`"@context":{"@vocab":"http://schema.nuget.org/schema#"}}`
	out, edits := streamRegistrationDoc(t, in)
	if want := repointRegistrationLinks(in); out != want {
		t.Errorf("body =\n%s\nwant\n%s", out, want)
	}
	var applied, declined int
	for _, e := range edits {
		if e.To == "" {
			declined++
		} else {
			applied++
		}
	}
	if applied != 4 || declined != 0 {
		t.Errorf("applied/declined = %d/%d, want 4/0 (projectUrl and @vocab never offered)", applied, declined)
	}
}

// A publisher-written catalogEntry URL on the match host is neither repointed
// nor learned: learning "/" would admit every path on the registry host.
func TestStreamRegistrationIgnoresCatalogEntryURLs(t *testing.T) {
	const in = `{"@id":"https://feed.example.test/api/v3/registration-semver2/foo/page/1.0.0/2.0.0.json","items":[` +
		`{"@id":"https://feed.example.test/api/v3/registration-semver2/foo/1.0.0.json",` +
		`"catalogEntry":{"@id":"https://feed.example.test/api/v3/catalog/foo.json","projectUrl":"https://feed.example.test/",` +
		`"iconUrl":"https://feed.example.test/artifactory/api/security/x","licenseUrl":"https://feed.example.test/license",` +
		`"description":"https://feed.example.test/"},` +
		`"packageContent":"https://feed.example.test/api/v3/registration-semver2/Download/foo/1.0.0"}]}`
	out, edits := streamRegistrationDoc(t, in)
	if want := repointRegistrationLinks(in); out != want {
		t.Errorf("body =\n%s\nwant\n%s", out, want)
	}
	if len(edits) != 3 {
		t.Errorf("edits = %+v, want only the page @id, leaf @id and packageContent", edits)
	}
	for _, e := range edits {
		if e.LearnedPath == "/" || strings.Contains(e.From, "catalog") || strings.Contains(e.From, "security") || strings.HasSuffix(e.From, ".test/") {
			t.Errorf("edit %+v offered a publisher-written URL", e)
		}
	}
}

// Registration documents nest items[] only to index -> page -> leaf; a link
// deeper than that is not registry-written.
func TestStreamRegistrationIgnoresLinksBeyondLeafDepth(t *testing.T) {
	const in = `{"@id":"https://feed.example.test/api/v3/registration-semver2/foo/index.json","items":[{"items":[{"items":[` +
		`{"@id":"https://feed.example.test/api/v3/registration-semver2/deep.json",` +
		`"packageContent":"https://feed.example.test/api/v3/registration-semver2/Download/deep/1.0.0"}]}]}]}`
	out, edits := streamRegistrationDoc(t, in)
	want := strings.Replace(in, registrationUpstream, registrationForwarded, 1)
	if out != want {
		t.Errorf("body =\n%s\nwant only the root @id repointed:\n%s", out, want)
	}
	if len(edits) != 1 {
		t.Errorf("edits = %+v, want only the root @id", edits)
	}
}

// overCapBytes stands in for registryproxy's unexported maxRewriteBodyBytes
// (1 MiB); the page below is built past it.
const overCapBytes = 2 << 20

// A page larger than the proxy's buffered-body cap must still be streamed and
// repointed to its last byte, not relayed unrewritten.
func TestStreamRegistrationRepointsOverCapPage(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"@id":"` + registrationUpstream + `foo/index.json","items":[{"@id":"` + registrationUpstream + `foo/page/0.0.1/9.9.9.json","items":[`)
	const leaves = 10000
	pad := strings.Repeat("x", 100)
	for i := 0; i < leaves; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		v := "1.0.0-" + pad + strings.Repeat("y", i%7)
		if i == leaves-1 {
			v = "last"
		}
		b.WriteString(`{"@id":"` + registrationUpstream + `foo/` + v + `.json","packageContent":"` + registrationUpstream + `Download/foo/` + v + `"}`)
	}
	b.WriteString(`]}]}`)
	in := b.String()
	if len(in) <= overCapBytes {
		t.Fatalf("test page is %d bytes, want over %d", len(in), overCapBytes)
	}
	out, _ := streamRegistrationDoc(t, in)
	if !strings.Contains(out, `"packageContent":"`+registrationForwarded+`Download/foo/last"`) {
		t.Errorf("last item's packageContent not repointed")
	}
	if out != repointRegistrationLinks(in) {
		t.Errorf("over-cap page not repointed throughout")
	}
}
