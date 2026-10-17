// Package conformance is the kit a Driver's package calls from its own tests:
// Run replays a corpus of recorded Pass logs against a driverkit.Strategy and
// checks every observable output against the goldens beside each log. It takes
// the Strategy and a directory, never the Driver registry or another Driver's
// package, so a Driver proves itself over its own corpus alone (issue #5506).
//
// A corpus directory holds one subdirectory per case, each with a pass.jsonl,
// an optional render.json ({"topLevelRole": "..."}), and the *.golden.* files
// the kit compares (or rewrites under UPDATE_GOLDENS). Importing this package
// from non-test code pulls in testing; only _test.go files should do so.
package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/corpus"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/golden"
	"spindrift.dev/launcher/internal/passmachine"
)

const (
	// heartbeatIssue is the issue number baked into every heartbeat golden.
	heartbeatIssue = "42"
	// maxChunk caps random chunk sizes above the shortest corpus line, so
	// splits land both mid-line and across line ends.
	maxChunk = 97
)

type modelCall struct {
	Model string `json:"model"`
	Role  string `json:"role"`
}

// Run replays every case under corpusDir against s: one subtest per case for
// the goldens and the chunked heartbeat replay, plus a "credentials" subtest
// scanning every corpus file. A corpusDir with no case fails.
func Run(t *testing.T, s driverkit.Strategy, corpusDir string) {
	t.Helper()
	d := driverkit.New(s)
	update := golden.Update()
	for _, c := range caseDirs(t, corpusDir) {
		t.Run(c, func(t *testing.T) {
			runCase(t, d, filepath.Join(corpusDir, c), update)
		})
	}
	t.Run("credentials", func(t *testing.T) {
		checkNoCredentials(t, corpusDir)
	})
}

// caseDirs lists the case subdirectories of corpusDir, failing t when there
// are none so an unpopulated or misspelled directory cannot pass vacuously.
func caseDirs(t testing.TB, corpusDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(corpusDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read %s: %v", corpusDir, err)
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Errorf("corpus %s has no case; add one at %s/<case>/pass.jsonl", corpusDir, corpusDir)
	}
	return names
}

// checkNoCredentials rejects any corpus file, log or golden, holding a
// credential-shaped string, an email or a host home path.
func checkNoCredentials(t testing.TB, corpusDir string) {
	t.Helper()
	violations, err := corpus.Scan(corpusDir)
	if err != nil {
		t.Errorf("scan %s: %v", corpusDir, err)
		return
	}
	for _, v := range violations {
		t.Errorf("%s; scrub it with go run ./internal/driver/corpus/scrub", v)
	}
}

// heartbeat feeds log through the Driver's heartbeat writer in the given
// chunk sizes (cycled), returning the raw bytes forwarded, the lines written,
// and the OnModel calls. Slices are never nil so they marshal as [].
func heartbeat(t *testing.T, d driverkit.Driver, log []byte, role string, chunks func() int) (raw []byte, lines []string, models []modelCall) {
	t.Helper()
	var rawBuf, out bytes.Buffer
	models = []modelCall{}
	w := d.NewHeartbeatWriter(&rawBuf, heartbeatIssue, &out, driverkit.RenderOptions{
		TopLevelRole: role,
		OnModel:      func(model, role string) { models = append(models, modelCall{model, role}) },
	})
	for rest := log; len(rest) > 0; {
		n := min(chunks(), len(rest))
		if _, err := w.Write(rest[:n]); err != nil {
			t.Fatalf("heartbeat Write: %v", err)
		}
		rest = rest[n:]
	}
	lines = []string{}
	if out.Len() > 0 {
		lines = strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	}
	return rawBuf.Bytes(), lines, models
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// runCase drives d over the case in dir through the Driver interface alone and
// compares each observable output against the golden beside the log.
func runCase(t *testing.T, d driverkit.Driver, dir string, update bool) {
	t.Helper()
	logPath := filepath.Join(dir, "pass.jsonl")
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	check := func(file string, produced []byte) {
		t.Helper()
		compare := golden.CompareOrUpdateText
		if strings.HasSuffix(file, ".json") {
			compare = func(p string, b []byte, u bool) error { return golden.CompareOrUpdateJSON(p, b, u) }
		}
		if err := compare(filepath.Join(dir, file), produced, update); err != nil {
			t.Error(err)
		}
	}

	class, err := d.ClassifyTransient(logPath)
	if err != nil {
		t.Fatalf("ClassifyTransient: %v", err)
	}
	check("classification.golden.json", marshal(t, class))

	report, err := d.ExtractUsage(logPath)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	check("usage.golden.json", marshal(t, report))

	// An absent render.json renders under the Driver's default role.
	var opts driverkit.RenderOptions
	if b, err := os.ReadFile(filepath.Join(dir, "render.json")); err == nil {
		var cfg struct {
			TopLevelRole string `json:"topLevelRole"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			t.Fatalf("render.json: %v", err)
		}
		if cfg.TopLevelRole == "" {
			t.Fatal("render.json: topLevelRole is empty")
		}
		opts.TopLevelRole = cfg.TopLevelRole
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}

	transcript, err := d.RenderTranscript(logPath, opts)
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	check("transcript.golden.txt", []byte(transcript))

	if opts.TopLevelRole == driverkit.ReviewerRole {
		check("verdict.golden.json", marshal(t, passmachine.Scan(transcript, passmachine.KindReview)))
	} else if err := golden.RemoveIfUpdate(filepath.Join(dir, "verdict.golden.json"), update); err != nil {
		t.Error(err)
	}

	result, err := d.ResultText(logPath)
	if err != nil {
		t.Fatalf("ResultText: %v", err)
	}
	check("result.golden.txt", []byte(result))

	exits := map[string]int{}
	for _, code := range []int{0, 1} {
		got, err := d.ResolveExit(logPath, code)
		if err != nil {
			t.Fatalf("ResolveExit(%d): %v", code, err)
		}
		exits[strconv.Itoa(code)] = got
	}
	check("resolve-exit.golden.json", marshal(t, exits))

	raw, lines, models := heartbeat(t, d, log, opts.TopLevelRole, func() int { return len(log) })
	if !bytes.Equal(raw, log) {
		t.Errorf("single-write heartbeat forwarded %d bytes, want the %d-byte log unchanged", len(raw), len(log))
	}
	check("heartbeat.golden.json", marshal(t, map[string]any{"lines": lines, "onModel": models}))

	// Chunk stability is compare-only: the golden above is the single-write
	// result, never rewritten from a split run.
	splits := map[string]func() int{"1-byte": func() int { return 1 }}
	for _, seed := range []int64{1, 2, 3} {
		rng := rand.New(rand.NewSource(seed))
		splits["random-seed-"+strconv.FormatInt(seed, 10)] = func() int { return 1 + rng.Intn(maxChunk) }
	}
	for label, chunks := range splits {
		gotRaw, gotLines, gotModels := heartbeat(t, d, log, opts.TopLevelRole, chunks)
		if !bytes.Equal(gotRaw, log) {
			t.Errorf("%s: forwarded bytes differ from the input", label)
		}
		if !reflect.DeepEqual(gotLines, lines) {
			t.Errorf("%s: heartbeat lines differ from the single-write run", label)
		}
		if !reflect.DeepEqual(gotModels, models) {
			t.Errorf("%s: OnModel calls differ from the single-write run", label)
		}
	}
}
