package corpus

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// plantedShapes is the one table behind both plantedTokens and bodyMarkers: a
// token is prefix + count bytes of filler (cycled) + suffix, and its marker is
// the first 8 bytes of that body. Adding a pattern means adding one row. The
// openai filler carries a digit and an uppercase letter, as real keys do.
var plantedShapes = []struct {
	name, prefix, filler, suffix string
	count                        int
}{
	{"github-token", "gh" + "p_", "a", "", 36},
	{"github-fine-grained", "github" + "_pat_", "B", "", 30},
	{"gitlab-token", "gl" + "pat-", "c", "", 20},
	{"anthropic-api-key", "sk" + "-ant-api03-", "d", "", 30},
	{"openai-api-key", "sk" + "-proj-", "eE1", "", 42},
	{"bearer-token", "Bear" + "er ", "f", "", 30},
	{"private-key-block", "-----BEGIN " + "RSA PRIVATE KEY-----\n", "MIIE", "\n-----END " + "RSA PRIVATE KEY-----", 40},
	{"aws-access-key-id", "AK" + "IA", "Q", "", 16},
	{"google-api-key", "AI" + "za", "g", "", 35},
	{"slack-token", "xo" + "xb-", "1", "", 12},
	{"npm-token", "npm" + "_", "h", "", 36},
	{"email", "", "u", "@example.com", 12},
	{"home-path", "/home/", "j", "/src/main.go", 8},
}

func shapeBody(filler string, count int) string {
	return strings.Repeat(filler, count/len(filler)+1)[:count]
}

// plantedTokens builds every credential shape at runtime so this file never
// contains a literal the guard itself would reject.
func plantedTokens() map[string]string {
	out := map[string]string{}
	for _, s := range plantedShapes {
		out[s.name] = s.prefix + shapeBody(s.filler, s.count) + s.suffix
	}
	return out
}

func TestFindReportsEachPatternWithLine(t *testing.T) {
	for name, tok := range plantedTokens() {
		t.Run(name, func(t *testing.T) {
			data := []byte("first line\nsecond line\nvalue: " + tok + "\nlast\n")
			got := Find(data)
			if len(got) != 1 || got[0].Pattern != name || got[0].Line != 3 {
				t.Errorf("Find = %+v, want one %q finding on line 3", got, name)
			}
		})
	}
}

func TestFindReportsBlockStartLine(t *testing.T) {
	tok := plantedTokens()["private-key-block"]
	got := Find([]byte("a\nb\n" + tok + "\nafter\n"))
	if len(got) != 1 || got[0].Line != 3 {
		t.Errorf("Find = %+v, want one finding on line 3", got)
	}
}

func TestFindIgnoresBenignText(t *testing.T) {
	benign := strings.Join([]string{
		"the task-runner picked sk-1 and sk- prefixes",
		"a bearer of bad news; Bearer short",
		"ghp_short and github_pat_ alone",
		"AKIA is a prefix, AKIAshort too",
		"-----BEGIN CERTIFICATE-----",
		"npm_ and xoxb- and AIza mentioned in prose",
		"{\"type\":\"result\",\"session_id\":\"0123456789abcdef0123456789abcdef\"}",
		"/nix/store/10dxp0qxqxxsyiljrh2kp0xqhz6arhcx-bash-5.3p15/bin/bash",
		"please ask-the-reviewer-to-run-the-full-gate-now",
		"/tmp/disk-usage-report-for-the-nightly-build-x",
		"/home/agent/workspace/cmd/main.go is the Box's own home",
		"pinned lodash@4.17.21 and golang.org/x/mod@v0.20.0",
		"ssh to admin@localhost, no domain",
	}, "\n")
	if got := Find([]byte(benign)); len(got) != 0 {
		t.Errorf("Find(benign) = %+v, want none", got)
	}
}

func TestScrubLeavesKebabCaseTextUntouched(t *testing.T) {
	in := []byte("/tmp/disk-usage-report-for-the-nightly-build-x and ask-the-reviewer-to-run-the-full-gate-now\n")
	if got := Scrub(in); !bytes.Equal(got, in) {
		t.Errorf("Scrub(%q) = %q, want unchanged", in, got)
	}
}

func TestAnthropicKeyIsNotAlsoReportedAsOpenAI(t *testing.T) {
	got := Find([]byte(plantedTokens()["anthropic-api-key"]))
	if len(got) != 1 || got[0].Pattern != "anthropic-api-key" {
		t.Errorf("Find = %+v, want exactly one anthropic-api-key finding", got)
	}
}

func TestScrubOutputPassesGuardAndIsIdempotent(t *testing.T) {
	var b strings.Builder
	for _, tok := range plantedTokens() {
		b.WriteString("line with " + tok + " inside\n")
	}
	b.WriteString("Authorization: " + plantedTokens()["bearer-token"] + "\nplain line\n")
	scrubbed := Scrub([]byte(b.String()))

	if got := Find(scrubbed); len(got) != 0 {
		t.Errorf("Find(Scrub(x)) = %+v, want none", got)
	}
	if again := Scrub(scrubbed); !bytes.Equal(again, scrubbed) {
		t.Errorf("Scrub is not idempotent:\nfirst:  %q\nsecond: %q", scrubbed, again)
	}
	for _, tok := range plantedTokens() {
		if bytes.Contains(scrubbed, []byte(tok)) {
			t.Errorf("Scrub left a planted token in place")
		}
	}
	if !bytes.Contains(scrubbed, []byte("plain line\n")) {
		t.Errorf("Scrub altered text with no credential: %q", scrubbed)
	}
}

func TestScrubAdjacentAndOverlappingCredentials(t *testing.T) {
	rep := strings.Repeat
	google := "AI" + "za" + rep("a", 35)
	github := "gh" + "p_" + rep("b", 36)
	cases := map[string]string{
		"back-to-back":         google + github,
		"overlapping":          "AI" + "za" + rep("a", 29) + "-gh" + "p_" + rep("b", 36),
		"back-to-back in text": "key=" + google + github + " end\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			scrubbed := Scrub([]byte(in))
			if got := Find(scrubbed); len(got) != 0 {
				t.Errorf("Find(Scrub(x)) = %+v, want none", got)
			}
			if again := Scrub(scrubbed); !bytes.Equal(again, scrubbed) {
				t.Errorf("Scrub is not idempotent:\nfirst:  %q\nsecond: %q", scrubbed, again)
			}
			if bytes.Contains(scrubbed, []byte(rep("b", 10))) {
				t.Errorf("Scrub leaked part of the github token body: %q", scrubbed)
			}
		})
	}
}

func TestScrubKeepsBearerPrefix(t *testing.T) {
	got := string(Scrub([]byte("Authorization: " + plantedTokens()["bearer-token"])))
	if want := "Authorization: Bearer [REDACTED:bearer-token]"; got != want {
		t.Errorf("Scrub = %q, want %q", got, want)
	}
}

func TestScrubUsesNamedPlaceholder(t *testing.T) {
	got := string(Scrub([]byte("k=" + plantedTokens()["github-token"] + "\n")))
	if want := "k=[REDACTED:github-token]\n"; got != want {
		t.Errorf("Scrub = %q, want %q", got, want)
	}
}

func TestScrubLeavesCleanInputUntouched(t *testing.T) {
	in := []byte("nothing secret here\n")
	if got := Scrub(in); !bytes.Equal(got, in) {
		t.Errorf("Scrub(%q) = %q, want unchanged", in, got)
	}
}

func TestScanReportsPlantedTokenAndPassesOnceScrubbed(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "driver", "case")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, "pass.jsonl")
	tok := plantedTokens()["github-token"]
	if err := os.WriteFile(path, []byte("ok\n"+tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{path + ":2: unscrubbed github-token"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Scan = %q, want %q", got, want)
	}
	for _, v := range got {
		if strings.Contains(v, tok) {
			t.Errorf("violation leaks the secret: %q", v)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, Scrub(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Scan(dir); err != nil || len(got) != 0 {
		t.Errorf("Scan after Scrub = %q, %v; want clean", got, err)
	}
}

func TestScanErrorsOnMissingRoot(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("Scan of a missing root returned no error")
	}
}

func TestPatternNamesAreUniqueAndPlaceholderSafe(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range patterns {
		if seen[p.name] {
			t.Errorf("duplicate pattern name %q", p.name)
		}
		seen[p.name] = true
	}
	for _, p := range patterns {
		for name := range seen {
			if p.re.Find([]byte("[REDACTED:"+name+"]")) != nil {
				t.Errorf("pattern %q matches the placeholder for %q", p.name, name)
			}
		}
	}
}

// bodyMarkers names, per pattern, the first 8 bytes of the planted token's
// body. A marker surviving Scrub means secret bytes leaked.
var bodyMarkers = func() map[string]string {
	out := map[string]string{}
	for _, s := range plantedShapes {
		out[s.name] = shapeBody(s.filler, s.count)[:8]
	}
	return out
}()

// assertNothingSurvives checks Find sees in and that Scrub removes every
// listed marker, leaves no Find hit, and is idempotent.
func assertNothingSurvives(t *testing.T, in string, markers ...string) {
	t.Helper()
	if len(Find([]byte(in))) == 0 {
		t.Errorf("Find(%q) found nothing", in)
	}
	scrubbed := Scrub([]byte(in))
	for _, m := range markers {
		if bytes.Contains(scrubbed, []byte(m)) {
			t.Errorf("Scrub(%q) = %q, left %q in clear", in, scrubbed, m)
		}
	}
	if got := Find(scrubbed); len(got) != 0 {
		t.Errorf("Find(Scrub(%q)) = %+v, want none", in, got)
	}
	if again := Scrub(scrubbed); !bytes.Equal(again, scrubbed) {
		t.Errorf("Scrub(%q) is not idempotent: %q then %q", in, scrubbed, again)
	}
}

func TestScrubHidesNoCredentialBehindAnotherAnywhere(t *testing.T) {
	tokens := plantedTokens()
	if len(bodyMarkers) != len(patterns) {
		t.Fatalf("bodyMarkers has %d entries for %d patterns", len(bodyMarkers), len(patterns))
	}
	for _, p := range patterns {
		if _, ok := tokens[p.name]; !ok {
			t.Fatalf("plantedTokens has no sample for pattern %q", p.name)
		}
		if _, ok := bodyMarkers[p.name]; !ok {
			t.Fatalf("bodyMarkers has no entry for pattern %q", p.name)
		}
	}
	for _, a := range patterns {
		for _, b := range patterns {
			t.Run(a.name+"+"+b.name, func(t *testing.T) {
				assertNothingSurvives(t, tokens[a.name]+tokens[b.name], bodyMarkers[a.name], bodyMarkers[b.name])
			})
		}
	}
}

func TestScrubHidesButtedTogetherCredentialsWithDifferentBodies(t *testing.T) {
	rep := strings.Repeat
	ghp := func(filler string) string { return "gh" + "p_" + rep(filler, 36) }
	tokens := plantedTokens()
	cases := []struct {
		name, in string
		markers  []string
	}{
		{"aws then github", tokens["aws-access-key-id"] + ghp("b"), []string{bodyMarkers["aws-access-key-id"], rep("b", 8)}},
		{"github then github", ghp("a") + ghp("b"), []string{rep("a", 8), rep("b", 8)}},
		{"slack then github", tokens["slack-token"] + ghp("b"), []string{bodyMarkers["slack-token"], rep("b", 8)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertNothingSurvives(t, c.in, c.markers...)
		})
	}
}

func TestScrubRedactsWholeHostHomePath(t *testing.T) {
	cases := map[string]string{
		"linux":          `"cwd":"/home/someone/dev/acme-private/src"`,
		"macos":          `"cwd":"/Users/someone/clients/acme/x.go"`,
		"after box home": `"/home/agent/x /home/someone/acme"`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := string(Scrub([]byte(in)))
			for _, leak := range []string{"someone", "acme"} {
				if strings.Contains(got, leak) {
					t.Errorf("Scrub(%q) = %q, left %q in clear", in, got, leak)
				}
			}
			if !strings.Contains(got, "[REDACTED:home-path]") {
				t.Errorf("Scrub(%q) = %q, want a home-path placeholder", in, got)
			}
		})
	}
}

func TestScrubKeepsJSONStructureAroundEmailAndPath(t *testing.T) {
	in := `{"author":"someone@example.com","cwd":"/home/someone/acme","n":1}`
	got := string(Scrub([]byte(in)))
	want := `{"author":"[REDACTED:email]","cwd":"[REDACTED:home-path]","n":1}`
	if got != want {
		t.Errorf("Scrub = %q, want %q", got, want)
	}
}

func TestScrubHidesOverlongGoogleKey(t *testing.T) {
	in := "AI" + "za" + strings.Repeat("g", 40)
	assertNothingSurvives(t, in, strings.Repeat("g", 8))
}
