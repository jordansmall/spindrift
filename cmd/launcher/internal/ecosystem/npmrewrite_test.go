package ecosystem

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"spindrift.dev/launcher/internal/registryvocab"
)

// Every streamNpmPackument case runs against this route context. A case
// that needs a different host copies it and overrides MatchHost.
var npmRewriteContext = registryvocab.RewriteContext{
	MatchHost: "registry.example.com",
	Forwarder: &url.URL{Scheme: "http", Host: "127.0.0.1:9999"},
	Prefix:    "r0",
}

func TestNpmRow_RewriteRows_PackumentMatches(t *testing.T) {
	rows := npmRow.RewriteRows
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	row := rows[0]

	if row.Ecosystem != nameNpm {
		t.Errorf("Ecosystem = %q, want %q", row.Ecosystem, nameNpm)
	}
	if row.Rewrite != nil || row.RewriteStream == nil {
		t.Errorf("want a streaming row: Rewrite set = %v, RewriteStream set = %v", row.Rewrite != nil, row.RewriteStream != nil)
	}
	if row.Method != http.MethodGet {
		t.Errorf("Method = %q, want %q", row.Method, http.MethodGet)
	}

	cases := []struct {
		name    string
		path    string
		base    string
		matches bool
	}{
		{name: "root base unscoped name", path: "/pkg", base: "/", matches: true},
		{name: "non-root base unscoped name", path: "/r0/pkg", base: "/r0", matches: true},
		{name: "root base decoded scoped name", path: "/@scope/pkg", base: "/", matches: true},
		{name: "non-root base decoded scoped name", path: "/r0/@scope/pkg", base: "/r0", matches: true},
		{name: "unscoped tarball path does not match", path: "/pkg/-/pkg-1.0.0.tgz", base: "/", matches: false},
		{name: "scoped tarball path does not match", path: "/@scope/pkg/-/pkg-1.0.0.tgz", base: "/", matches: false},
		{name: "near-miss base prefix", path: "/r0foo/pkg", base: "/r0", matches: false},
		{name: "trailing slash does not match", path: "/pkg/", base: "/", matches: false},
		{name: "two-segment non-scoped path does not match", path: "/pkg/download", base: "/", matches: false},
		{name: "bare @ segment does not match", path: "/@", base: "/", matches: false},
		{name: "no package name at all", path: "/", base: "/", matches: false},
		{name: "tarball directory marker does not match", path: "/-", base: "/", matches: false},
		{name: "dot segment does not match", path: "/.", base: "/", matches: false},
		{name: "dot-dot segment does not match", path: "/..", base: "/", matches: false},
		{name: "scoped tarball directory marker does not match", path: "/@scope/-", base: "/", matches: false},
		{name: "scoped dot segment does not match", path: "/@scope/.", base: "/", matches: false},
		{name: "scoped dot-dot segment does not match", path: "/@scope/..", base: "/", matches: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := row.Matches(tc.path, tc.base); got != tc.matches {
				t.Errorf("Matches(%q, %q) = %v, want %v", tc.path, tc.base, got, tc.matches)
			}
		})
	}
}

// streamPackument runs streamNpmPackument over in and returns what reached dst,
// the edits onEdit saw, and the returned error.
func streamPackument(in io.Reader, rc registryvocab.RewriteContext) (string, []registryvocab.RewriteEdit, error) {
	var dst bytes.Buffer
	var edits []registryvocab.RewriteEdit
	err := streamNpmPackument(&dst, in, rc, func(e registryvocab.RewriteEdit) { edits = append(edits, e) })
	return dst.String(), edits, err
}

func TestStreamNpmPackument_AllSameHost(t *testing.T) {
	in := `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/pkg/-/pkg-1.0.0.tgz"}},"2.0.0":{"dist":{"tarball":"https://registry.example.com/pkg/-/pkg-2.0.0.tgz"}}}}`
	want := `{"versions":{"1.0.0":{"dist":{"tarball":"http://127.0.0.1:9999/r0/pkg/-/pkg-1.0.0.tgz"}},"2.0.0":{"dist":{"tarball":"http://127.0.0.1:9999/r0/pkg/-/pkg-2.0.0.tgz"}}}}`

	out, edits, err := streamPackument(strings.NewReader(in), npmRewriteContext)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if out != want {
		t.Errorf("out = %s\nwant  %s", out, want)
	}
	wantEdits := []registryvocab.RewriteEdit{
		{From: "https://registry.example.com/pkg/-/pkg-1.0.0.tgz", To: "http://127.0.0.1:9999/r0/pkg/-/pkg-1.0.0.tgz", LearnedPath: "/pkg/-/pkg-1.0.0.tgz"},
		{From: "https://registry.example.com/pkg/-/pkg-2.0.0.tgz", To: "http://127.0.0.1:9999/r0/pkg/-/pkg-2.0.0.tgz", LearnedPath: "/pkg/-/pkg-2.0.0.tgz"},
	}
	if len(edits) != len(wantEdits) || edits[0] != wantEdits[0] || edits[1] != wantEdits[1] {
		t.Errorf("edits = %+v, want %+v", edits, wantEdits)
	}
}

func TestStreamNpmPackument_HostMatchNormalization(t *testing.T) {
	cases := []struct {
		name        string
		tarballHost string
		matchHost   string
	}{
		{name: "matchHost carries explicit default port", tarballHost: "registry.example.com", matchHost: "registry.example.com:443"},
		{name: "tarball carries explicit default port", tarballHost: "registry.example.com:443", matchHost: "registry.example.com"},
		{name: "case differs", tarballHost: "Registry.Example.COM", matchHost: "registry.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := `{"versions":{"1.0.0":{"dist":{"tarball":"https://` + tc.tarballHost + `/pkg/-/pkg-1.0.0.tgz"}}}}`
			rc := npmRewriteContext
			rc.MatchHost = tc.matchHost

			out, edits, err := streamPackument(strings.NewReader(in), rc)

			want := `{"versions":{"1.0.0":{"dist":{"tarball":"http://127.0.0.1:9999/r0/pkg/-/pkg-1.0.0.tgz"}}}}`
			if err != nil || out != want || len(edits) != 1 {
				t.Errorf("out = %s, edits = %+v, err = %v; want out %s with one edit", out, edits, err, want)
			}
		})
	}
}

func TestStreamNpmPackument_AllForeignHost(t *testing.T) {
	in := `{"versions":{"1.0.0":{"dist":{"tarball":"https://cdn.example.com/pkg/-/pkg-1.0.0.tgz"}},"2.0.0":{"dist":{"tarball":"https://cdn.example.com/pkg/-/pkg-2.0.0.tgz"}}}}`

	out, edits, err := streamPackument(strings.NewReader(in), npmRewriteContext)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if out != in {
		t.Errorf("out = %s, want byte-identical to input", out)
	}
	if len(edits) != 2 {
		t.Fatalf("edits = %+v, want exactly two", edits)
	}
	for _, e := range edits {
		if e.To != "" || e.LearnedPath != "" {
			t.Errorf("edit %+v: want a declined edit (empty To and LearnedPath)", e)
		}
	}
	if edits[0].From != "https://cdn.example.com/pkg/-/pkg-1.0.0.tgz" {
		t.Errorf("edit[0].From = %q", edits[0].From)
	}
}

func TestStreamNpmPackument_Mixed(t *testing.T) {
	in := `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/pkg/-/pkg-1.0.0.tgz"}},"2.0.0":{"dist":{"tarball":"https://cdn.example.com/pkg/-/pkg-2.0.0.tgz"}}}}`
	want := `{"versions":{"1.0.0":{"dist":{"tarball":"http://127.0.0.1:9999/r0/pkg/-/pkg-1.0.0.tgz"}},"2.0.0":{"dist":{"tarball":"https://cdn.example.com/pkg/-/pkg-2.0.0.tgz"}}}}`

	out, edits, err := streamPackument(strings.NewReader(in), npmRewriteContext)

	if err != nil || out != want {
		t.Errorf("out = %s, err = %v; want %s", out, err, want)
	}
	if len(edits) != 2 || edits[0].To != "http://127.0.0.1:9999/r0/pkg/-/pkg-1.0.0.tgz" || edits[1].To != "" {
		t.Errorf("edits = %+v, want an applied edit then a declined one", edits)
	}
}

func TestStreamNpmPackument_NotRewritten(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "trailing content after the JSON object", body: `{"versions":{}}{"junk":1}`},
		{name: "empty body", body: ``},
		{name: "top level array", body: `[{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/a.tgz"}}}}]`},
		{name: "no versions key", body: `{"name":"pkg"}`},
		{name: "versions not an object", body: `{"versions":[1,2,3]}`},
		{name: "versions an array of packument-shaped objects", body: `{"versions":[{"dist":{"tarball":"https://registry.example.com/a.tgz"}}]}`},
		{name: "version not an object", body: `{"versions":{"1.0.0":"not an object"}}`},
		{name: "dist not an object", body: `{"versions":{"1.0.0":{"dist":"not an object"}}}`},
		{name: "tarball not a string", body: `{"versions":{"1.0.0":{"dist":{"tarball":123}}}}`},
		{name: "tarball not an absolute URL", body: `{"versions":{"1.0.0":{"dist":{"tarball":"/pkg/-/pkg-1.0.0.tgz"}}}}`},
		{name: "dist one level too deep", body: `{"versions":{"1.0.0":{"x":{"dist":{"tarball":"https://registry.example.com/a.tgz"}}}}}`},
		{name: "dist one level too shallow", body: `{"versions":{"dist":{"tarball":"https://registry.example.com/a.tgz"}}}`},
		{name: "dist at top level", body: `{"dist":{"tarball":"https://registry.example.com/a.tgz"}}`},
		{name: "tarball as a dist key's value of the wrong member", body: `{"versions":{"1.0.0":{"dist":{"other":"https://registry.example.com/a.tgz"}}}}`},
		{name: "tarball under a differently named versions key", body: `{"vers":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/a.tgz"}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, edits, err := streamPackument(strings.NewReader(tc.body), npmRewriteContext)

			if err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if len(edits) != 0 {
				t.Errorf("edits = %+v, want none", edits)
			}
			if out != tc.body {
				t.Errorf("out = %s, want byte-identical to input %s", out, tc.body)
			}
		})
	}
}

func TestStreamNpmPackument_NotJSONIsAbandonedVerbatim(t *testing.T) {
	in := `not json at all`

	out, edits, err := streamPackument(strings.NewReader(in), npmRewriteContext)

	if !errors.Is(err, registryvocab.ErrRewriteAbandoned) {
		t.Errorf("err = %v, want ErrRewriteAbandoned", err)
	}
	if out != in || len(edits) != 0 {
		t.Errorf("out = %q, edits = %+v; want the input verbatim and no edits", out, edits)
	}
}

func TestStreamNpmPackument_LearnedPath(t *testing.T) {
	tests := []struct {
		name    string
		tarball string
		want    string
	}{
		{name: "absolute path preserved verbatim", tarball: "https://registry.example.com/pkg/-/pkg-1.0.0.tgz", want: "/pkg/-/pkg-1.0.0.tgz"},
		{name: "bare host with no path at all normalizes to root, not empty string", tarball: "https://registry.example.com", want: "/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := `{"versions":{"1.0.0":{"dist":{"tarball":"` + tc.tarball + `"}}}}`

			_, edits, err := streamPackument(strings.NewReader(in), npmRewriteContext)

			if err != nil || len(edits) != 1 {
				t.Fatalf("edits = %+v, err = %v; want exactly one edit", edits, err)
			}
			if edits[0].LearnedPath != tc.want {
				t.Errorf("learnedPath = %q, want %q", edits[0].LearnedPath, tc.want)
			}
		})
	}
}

// Everything but a rewritten tarball string's own bytes passes through as it
// arrived: whitespace (including around the colon the tokenizer swallows), key
// order, number literals a float64 round-trip would mangle, and characters
// json.Marshal would escape.
func TestStreamNpmPackument_BytePreserving(t *testing.T) {
	in := " {\n" +
		`  "z" : 12345678901234567890 , "big":1e400,` + "\n" +
		`  "note":"a<b>&cé",` + "\n" +
		`  "versions" :	{ "2.0.0" : { "dist" : { "signatures":[{"sig":"x"}] , "tarball" :  "https://registry.example.com/pkg/-/pkg-2.0.0.tgz"` + " ,\"n\":null } } ,\n" +
		`  "1.0.0":{"dist":{"tarball":"https://cdn.example.com/pkg/-/pkg-1.0.0.tgz"}} } ,` + "\n" +
		`  "dist":{"tarball":"https://registry.example.com/not/under/versions"}` + "\n} \n"
	want := " {\n" +
		`  "z" : 12345678901234567890 , "big":1e400,` + "\n" +
		`  "note":"a<b>&cé",` + "\n" +
		`  "versions" :	{ "2.0.0" : { "dist" : { "signatures":[{"sig":"x"}] , "tarball" :  "http://127.0.0.1:9999/r0/pkg/-/pkg-2.0.0.tgz"` + " ,\"n\":null } } ,\n" +
		`  "1.0.0":{"dist":{"tarball":"https://cdn.example.com/pkg/-/pkg-1.0.0.tgz"}} } ,` + "\n" +
		`  "dist":{"tarball":"https://registry.example.com/not/under/versions"}` + "\n} \n"

	readers := map[string]func() io.Reader{
		"whole":    func() io.Reader { return strings.NewReader(in) },
		"one byte": func() io.Reader { return iotest.OneByteReader(strings.NewReader(in)) },
	}
	for name, newReader := range readers {
		t.Run(name, func(t *testing.T) {
			out, edits, err := streamPackument(newReader(), npmRewriteContext)

			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if out != want {
				t.Errorf("out = %q\nwant  %q", out, want)
			}
			if len(edits) != 2 {
				t.Errorf("edits = %+v, want exactly two (the 2.0.0 and 1.0.0 tarballs)", edits)
			}
		})
	}
}

// A rewritten value is JSON-encoded, so a character that needs escaping in the
// new URL stays a valid string.
func TestStreamNpmPackument_EscapesRewrittenValue(t *testing.T) {
	in := `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/pkg/a\"b.tgz"}}}}`

	out, _, err := streamPackument(strings.NewReader(in), npmRewriteContext)

	want := `{"versions":{"1.0.0":{"dist":{"tarball":"http://127.0.0.1:9999/r0/pkg/a%22b.tgz"}}}}`
	if err != nil || out != want {
		t.Errorf("out = %s, err = %v; want %s", out, err, want)
	}
}

// Edits arrive in document order, not sorted by version name.
func TestStreamNpmPackument_EditsInDocumentOrder(t *testing.T) {
	in := `{"versions":{"2.0.0":{"dist":{"tarball":"https://registry.example.com/b.tgz"}},"10.0.0":{"dist":{"tarball":"https://cdn.example.com/c.tgz"}},"1.0.0":{"dist":{"tarball":"https://registry.example.com/a.tgz"}}}}`

	_, edits, err := streamPackument(strings.NewReader(in), npmRewriteContext)

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	wantFrom := []string{"https://registry.example.com/b.tgz", "https://cdn.example.com/c.tgz", "https://registry.example.com/a.tgz"}
	if len(edits) != len(wantFrom) {
		t.Fatalf("edits = %+v, want %d", edits, len(wantFrom))
	}
	for i, from := range wantFrom {
		if edits[i].From != from {
			t.Errorf("edits[%d].From = %q, want %q", i, edits[i].From, from)
		}
	}
}

// A packument well past the 1 MiB window streams through whole: the window
// bounds one token, not the document.
func TestStreamNpmPackument_LargePackument(t *testing.T) {
	const versions = 2500
	padding := strings.Repeat("x", 2000)
	var in, want strings.Builder
	in.WriteString(`{"name":"big","versions":{`)
	want.WriteString(`{"name":"big","versions":{`)
	for i := range versions {
		if i > 0 {
			in.WriteString(",")
			want.WriteString(",")
		}
		v := `"1.0.` + strconv.Itoa(i) + `":{"description":"` + padding + `","dist":{"tarball":"`
		tail := `/pkg/-/pkg-1.0.` + strconv.Itoa(i) + `.tgz"}}`
		in.WriteString(v + "https://registry.example.com" + tail)
		want.WriteString(v + "http://127.0.0.1:9999/r0" + tail)
	}
	in.WriteString(`}}`)
	want.WriteString(`}}`)
	if in.Len() < 4<<20 {
		t.Fatalf("fixture is %d bytes, want well above the 1 MiB window", in.Len())
	}

	out, edits, err := streamPackument(strings.NewReader(in.String()), npmRewriteContext)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(edits) != versions {
		t.Errorf("len(edits) = %d, want %d", len(edits), versions)
	}
	if out != want.String() {
		t.Errorf("output differs from the expected rewrite (len %d vs %d)", len(out), want.Len())
	}
}

// A single token larger than the window cannot be interpreted; the whole body
// still reaches the client untouched, with the abandonment reported.
func TestStreamNpmPackument_TokenLargerThanWindowIsAbandoned(t *testing.T) {
	in := `{"readme":"` + strings.Repeat("r", 2<<20) + `","versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/a.tgz"}}}}`

	out, edits, err := streamPackument(strings.NewReader(in), npmRewriteContext)

	if !errors.Is(err, registryvocab.ErrRewriteAbandoned) {
		t.Fatalf("err = %v, want ErrRewriteAbandoned", err)
	}
	if out != in {
		t.Errorf("output (len %d) is not byte-identical to the input (len %d)", len(out), len(in))
	}
	if len(edits) != 0 {
		t.Errorf("edits = %+v, want none", edits)
	}
}

// Malformed JSON after a rewritable tarball: what was parsed is rewritten,
// everything from the break on is relayed verbatim, and the abandonment is
// reported.
func TestStreamNpmPackument_MalformedMidStream(t *testing.T) {
	in := `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/a.tgz"}},"2.0.0":{"dist":{"tarball" @@ "https://registry.example.com/b.tgz"}}}}`
	want := `{"versions":{"1.0.0":{"dist":{"tarball":"http://127.0.0.1:9999/r0/a.tgz"}},"2.0.0":{"dist":{"tarball" @@ "https://registry.example.com/b.tgz"}}}}`

	out, edits, err := streamPackument(strings.NewReader(in), npmRewriteContext)

	if !errors.Is(err, registryvocab.ErrRewriteAbandoned) {
		t.Fatalf("err = %v, want ErrRewriteAbandoned", err)
	}
	if out != want {
		t.Errorf("out = %s\nwant  %s", out, want)
	}
	if len(edits) != 1 || edits[0].From != "https://registry.example.com/a.tgz" {
		t.Errorf("edits = %+v, want only the tarball before the break", edits)
	}
}

// A body cut off inside an open container ends the tokenizer with a bare EOF;
// that is truncation, not a clean end, so it is reported like other malformed
// JSON while the bytes still pass through verbatim.
func TestStreamNpmPackument_TruncatedBodyIsAbandoned(t *testing.T) {
	in := `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/a.tgz"}}`
	want := `{"versions":{"1.0.0":{"dist":{"tarball":"http://127.0.0.1:9999/r0/a.tgz"}}`

	out, _, err := streamPackument(strings.NewReader(in), npmRewriteContext)

	if !errors.Is(err, registryvocab.ErrRewriteAbandoned) {
		t.Fatalf("err = %v, want ErrRewriteAbandoned", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want it to wrap io.ErrUnexpectedEOF", err)
	}
	if out != want {
		t.Errorf("out = %s\nwant  %s", out, want)
	}
}

func TestStreamNpmPackument_SrcReadErrorIsNotAbandonment(t *testing.T) {
	boom := errors.New("upstream reset")
	head := `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/a.tgz"}},"2.0.0":{"dist":{"tar`
	src := io.MultiReader(strings.NewReader(head), iotest.ErrReader(boom))

	_, _, err := streamPackument(src, npmRewriteContext)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the src read error", err)
	}
	if errors.Is(err, registryvocab.ErrRewriteAbandoned) {
		t.Errorf("err = %v, must not report abandonment for an I/O failure", err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStreamNpmPackument_DstWriteErrorIsReturned(t *testing.T) {
	boom := errors.New("client went away")
	in := `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/a.tgz"}}}}`

	err := streamNpmPackument(failingWriter{boom}, strings.NewReader(in), npmRewriteContext, func(registryvocab.RewriteEdit) {})

	if !errors.Is(err, boom) || errors.Is(err, registryvocab.ErrRewriteAbandoned) {
		t.Errorf("err = %v, want the dst write error", err)
	}
}

// onEdit fires before the rewritten bytes reach dst: the caller learns the
// edit's path before a client can read the value that admits it.
type orderingWriter struct {
	edited      *int
	editsAtSeen int
	seen        bool
}

func (w *orderingWriter) Write(p []byte) (int, error) {
	if !w.seen && bytes.Contains(p, []byte(repointedPrefix)) {
		w.seen = true
		w.editsAtSeen = *w.edited
	}
	return len(p), nil
}

func TestStreamNpmPackument_OnEditPrecedesRewrittenBytes(t *testing.T) {
	// The URL outgrows the stream's output buffer, so the rewritten bytes reach
	// dst during the write rather than at a later flush.
	in := `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.com/` + strings.Repeat("a", 64<<10) + `.tgz"}}}}`
	var edited int
	dst := &orderingWriter{edited: &edited}

	err := streamNpmPackument(dst, strings.NewReader(in), npmRewriteContext, func(registryvocab.RewriteEdit) { edited++ })

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !dst.seen {
		t.Fatal("rewritten URL never reached dst")
	}
	if dst.editsAtSeen != 1 {
		t.Errorf("edits fired when the rewritten bytes were first written = %d, want 1", dst.editsAtSeen)
	}
}

const repointedPrefix = `http://127.0.0.1:9999/r0/`
