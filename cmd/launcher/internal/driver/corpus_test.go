package driver_test

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

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/corpus"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/golden"
)

const corpusRoot = "testdata/corpus"

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

// heartbeat feeds log through the Driver's heartbeat writer in the given
// chunk sizes (cycled), returning the raw bytes forwarded, the lines written,
// and the OnModel calls. Slices are never nil so they marshal as [].
func heartbeat(t *testing.T, d driver.Driver, log []byte, chunks func() int) (raw []byte, lines []string, models []modelCall) {
	t.Helper()
	var rawBuf, out bytes.Buffer
	models = []modelCall{}
	w := d.NewHeartbeatWriter(&rawBuf, heartbeatIssue, &out, driverkit.RenderOptions{
		OnModel: func(model, role string) { models = append(models, modelCall{model, role}) },
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

func subdirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// TestDriverCorpusGoldens drives every registered Driver over every corpus
// log through the Driver interface alone and compares each observable
// output against the golden beside the log.
func TestDriverCorpusGoldens(t *testing.T) {
	registered := map[string]bool{}
	for _, name := range driver.Names() {
		registered[name] = true
		if len(subdirs(t, filepath.Join(corpusRoot, name))) == 0 {
			t.Errorf("driver %q has no corpus log; add one under %s/%s/<case>/pass.jsonl", name, corpusRoot, name)
		}
	}
	for _, name := range subdirs(t, corpusRoot) {
		if !registered[name] {
			t.Errorf("%s/%s holds a corpus for an unregistered driver", corpusRoot, name)
		}
	}

	update := golden.Update()
	for _, name := range driver.Names() {
		d, err := driver.New(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range subdirs(t, filepath.Join(corpusRoot, name)) {
			dir := filepath.Join(corpusRoot, name, c)
			t.Run(name+"/"+c, func(t *testing.T) {
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

				transcript, err := d.RenderTranscript(logPath, driverkit.RenderOptions{})
				if err != nil {
					t.Fatalf("RenderTranscript: %v", err)
				}
				check("transcript.golden.txt", []byte(transcript))

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

				raw, lines, models := heartbeat(t, d, log, func() int { return len(log) })
				if !bytes.Equal(raw, log) {
					t.Errorf("single-write heartbeat forwarded %d bytes, want the %d-byte log unchanged", len(raw), len(log))
				}
				check("heartbeat.golden.json", marshal(t, map[string]any{"lines": lines, "onModel": models}))

				// Chunk stability is compare-only: the golden above is the
				// single-write result, never rewritten from a split run.
				splits := map[string]func() int{"1-byte": func() int { return 1 }}
				for _, seed := range []int64{1, 2, 3} {
					rng := rand.New(rand.NewSource(seed))
					splits["random-seed-"+strconv.FormatInt(seed, 10)] = func() int { return 1 + rng.Intn(maxChunk) }
				}
				for label, chunks := range splits {
					gotRaw, gotLines, gotModels := heartbeat(t, d, log, chunks)
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
			})
		}
	}
}

// TestDriverCorpusHasNoCredentials rejects any committed corpus file, log or
// golden, holding a credential-shaped string.
func TestDriverCorpusHasNoCredentials(t *testing.T) {
	violations, err := corpus.Scan(corpusRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range violations {
		t.Errorf("%s; scrub it with go run ./internal/driver/corpus/scrub", v)
	}
}
