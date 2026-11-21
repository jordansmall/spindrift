package shadow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/shadowrecord"
)

// goDelta is the fixture Connector's deltaFilter in Go: "delta " lines are held
// until a "done" line drops them, and Flush writes back what is still open.
type goDelta struct {
	w       io.Writer
	partial []byte
	pending [][]byte
}

func (f *goDelta) Write(p []byte) (int, error) {
	f.partial = append(f.partial, p...)
	for {
		i := bytes.IndexByte(f.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := bytes.Clone(f.partial[:i+1])
		f.partial = f.partial[i+1:]
		switch {
		case bytes.HasPrefix(line, []byte("delta ")):
			f.pending = append(f.pending, line)
		case string(line) == "done\n":
			f.pending = nil
			_, _ = f.w.Write(line)
		default:
			_, _ = f.w.Write(line)
		}
	}
}

func (f *goDelta) Flush() error {
	for _, l := range f.pending {
		_, _ = f.w.Write(l)
	}
	f.pending = nil
	_, err := f.w.Write(f.partial)
	f.partial = nil
	return err
}

type identity struct{ io.Writer }

func (identity) Flush() error { return nil }

func withFilter(f func(io.Writer) driverkit.StreamFilter) driverkit.Strategy {
	s := nativeStrategy()
	s.NewStreamFilter = f
	return s
}

func deltaStrategy() driverkit.Strategy {
	return withFilter(func(w io.Writer) driverkit.StreamFilter { return &goDelta{w: w} })
}

func identityStrategy() driverkit.Strategy {
	return withFilter(func(w io.Writer) driverkit.StreamFilter { return identity{w} })
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// runFilter runs only the filter check of a replay against shadow, a stub
// Connector Strategy, and returns the streamFilter divergences and the summary.
func runFilter(t *testing.T, ctx context.Context, native, shadow driverkit.Strategy, failed func() error, logPath, rawPath string) ([]hostop.ShadowDivergence, hostop.ShadowSummary) {
	t.Helper()
	var ops []hostop.SpindriftOp
	r := &run{native: native, Recorder: shadowrecord.New(native.Name, func(op hostop.SpindriftOp) { ops = append(ops, op) })}
	r.Sum.Loaded = true
	r.filterCompare(ctx, shadow, failed, logPath, rawPath)
	r.finish()
	divs, sum := split(t, ops)
	for _, d := range divs {
		if d.Check != "streamFilter" {
			t.Errorf("check = %q, want only streamFilter", d.Check)
		}
	}
	return divs, sum
}

func noFailure() error { return nil }

func TestFilterShadowReportsRegionAndBothOutputs(t *testing.T) {
	raw := "a\ndelta x\ndone\nb\n"
	// The native filter passes everything; the Connector's drops the delta line.
	divs, sum := runFilter(t, context.Background(), identityStrategy(), deltaStrategy(), noFailure,
		writeTemp(t, "log", raw), writeTemp(t, "raw", raw))

	if len(divs) != 1 {
		t.Fatalf("divergences = %v, want one", divs)
	}
	d := divs[0]
	if d.LineNo != 2 {
		t.Errorf("LineNo = %d, want 2, the first line the outputs differ on", d.LineNo)
	}
	if string(d.Native) != `"delta x\ndone\nb\n"` || string(d.Shadow) != `"done\nb\n"` {
		t.Errorf("native = %s, shadow = %s, want both outputs from the differing line on", d.Native, d.Shadow)
	}
	if !sum.FilterRan || !sum.FilterDiverged || sum.FilterError != "" || sum.FilterBytes != int64(len(raw)) {
		t.Errorf("summary = %+v, want the filter shadow ran, diverged, over %d bytes", sum, len(raw))
	}
	if sum.Diverged != 1 || sum.Recorded != 1 || sum.Error != "" {
		t.Errorf("Diverged = %d, Recorded = %d, Error = %q, want the divergence counted and no Error", sum.Diverged, sum.Recorded, sum.Error)
	}
	if sum.FilterNativeNsPerKiB <= 0 || sum.FilterShadowNsPerKiB <= 0 {
		t.Errorf("ns per KiB = %d native, %d shadow, want both populated", sum.FilterNativeNsPerKiB, sum.FilterShadowNsPerKiB)
	}
}

func TestFilterShadowCleanWhenOutputsAgree(t *testing.T) {
	raw := "a\ndelta x\ndone\nb\ndelta y\npartial"
	// What the native filter wrote to the log: the open delta line comes back at Flush.
	log := "a\ndone\nb\ndelta y\npartial"
	divs, sum := runFilter(t, context.Background(), deltaStrategy(), deltaStrategy(), noFailure,
		writeTemp(t, "log", log), writeTemp(t, "raw", raw))
	if len(divs) != 0 || sum.Diverged != 0 || sum.FilterDiverged {
		t.Errorf("divergences = %v, summary = %+v, want none", divs, sum)
	}
	if !sum.FilterRan || sum.FilterError != "" || sum.FilterBytes != int64(len(raw)) {
		t.Errorf("summary = %+v, want a clean filter shadow over %d bytes", sum, len(raw))
	}
}

// The raw copy crosses several chunks, and the difference sits past the first.
func TestFilterShadowComparesAcrossChunks(t *testing.T) {
	var raw strings.Builder
	for raw.Len() < 3*filterChunk+100 {
		raw.WriteString("line of plain output\n")
	}
	head := raw.String()
	lines := strings.Count(head, "\n")
	in := head + "delta x\ndone\n" + head
	log := head + "delta x\ndone\n" + head
	divs, sum := runFilter(t, context.Background(), identityStrategy(), deltaStrategy(), noFailure,
		writeTemp(t, "log", log), writeTemp(t, "raw", in))
	if len(divs) != 1 || divs[0].LineNo != lines+1 {
		t.Fatalf("divergences = %+v, want one at line %d", divs, lines+1)
	}
	// Past the payload clip each side is a quoted string holding its head.
	if !strings.HasPrefix(string(divs[0].Native), `"\"delta x`) || !strings.HasPrefix(string(divs[0].Shadow), `"\"done`) {
		t.Errorf("native = %.40s, shadow = %.40s, want regions starting at the differing line", divs[0].Native, divs[0].Shadow)
	}
	if !sum.FilterRan || sum.FilterBytes != int64(len(in)) {
		t.Errorf("summary = %+v, want the whole %d-byte copy fed", sum, len(in))
	}
}

func TestFilterShadowRegionKeepsTheMatchedHeadOfALongLine(t *testing.T) {
	// Two writes' worth of identical bytes, then a final differing one.
	same := strings.Repeat("x", 2*filterChunk+5)
	divs, _ := runFilter(t, context.Background(), identityStrategy(), deltaStrategy(), noFailure,
		writeTemp(t, "log", same+"A\n"), writeTemp(t, "raw", same+"B\n"))
	// The raw line is echoed, so the outputs differ only at "A" vs "B".
	if len(divs) != 1 || divs[0].LineNo != 1 {
		t.Fatalf("divergences = %+v, want one on line 1", divs)
	}
	if n, s := string(divs[0].Native), string(divs[0].Shadow); !strings.Contains(n, "A") || !strings.Contains(s, "B") || len(n) > 9<<10 {
		t.Errorf("native = %.20s, shadow = %.20s, want bounded regions around the difference", n, s)
	}
}

func TestFilterShadowOutputLengthMismatchIsADivergence(t *testing.T) {
	for _, tc := range []struct {
		name, log string
		line      int
	}{
		{"native ends early", "a\nb\n", 3},
		{"native has more", "a\nb\nc\nd\n", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			divs, sum := runFilter(t, context.Background(), identityStrategy(), identityStrategy(), noFailure,
				writeTemp(t, "log", tc.log), writeTemp(t, "raw", "a\nb\nc\n"))
			if len(divs) != 1 || divs[0].LineNo != tc.line || !sum.FilterDiverged || !sum.FilterRan {
				t.Errorf("divergences = %+v, summary = %+v, want one at line %d", divs, sum, tc.line)
			}
		})
	}
}

// A Connector advertising no filter is what cut-over would run as a passthrough.
func TestFilterShadowTreatsMissingConnectorFilterAsPassthrough(t *testing.T) {
	raw := "a\ndelta x\ndone\n"
	divs, sum := runFilter(t, context.Background(), identityStrategy(), nativeStrategy(), noFailure,
		writeTemp(t, "log", raw), writeTemp(t, "raw", raw))
	if len(divs) != 0 || !sum.FilterRan {
		t.Errorf("divergences = %v, summary = %+v, want a clean run", divs, sum)
	}
	divs, _ = runFilter(t, context.Background(), deltaStrategy(), nativeStrategy(), noFailure,
		writeTemp(t, "log", "a\ndone\n"), writeTemp(t, "raw", raw))
	if len(divs) != 1 {
		t.Errorf("divergences = %v, want the filtering native to diverge from a passthrough", divs)
	}
}

func TestFilterShadowWithoutARawCopyIsNeverClean(t *testing.T) {
	divs, sum := runFilter(t, context.Background(), deltaStrategy(), deltaStrategy(), noFailure, writeTemp(t, "log", "a\n"), "")
	if len(divs) != 0 || sum.FilterRan || sum.FilterError == "" || sum.Error != "" || sum.FilterBytes != 0 {
		t.Errorf("summary = %+v, divergences = %v, want a FilterError and no run", sum, divs)
	}
	_, sum = runFilter(t, context.Background(), deltaStrategy(), deltaStrategy(), noFailure, writeTemp(t, "log", "a\n"), filepath.Join(t.TempDir(), "missing"))
	if sum.FilterRan || sum.FilterError == "" || sum.Error != "" {
		t.Errorf("summary = %+v, want a FilterError for an unreadable copy", sum)
	}
}

func TestFilterShadowIsSilentForANativeWithoutAFilter(t *testing.T) {
	divs, sum := runFilter(t, context.Background(), nativeStrategy(), deltaStrategy(), noFailure, writeTemp(t, "log", "a\n"), writeTemp(t, "raw", "a\n"))
	want := hostop.ShadowSummary{Driver: "native", Loaded: true}
	if len(divs) != 0 || sum != want {
		t.Errorf("summary = %+v, divergences = %v, want %+v", sum, divs, want)
	}
}

func TestFilterShadowFailuresAreFilterErrorsNeverRunErrors(t *testing.T) {
	raw := writeTemp(t, "raw", strings.Repeat("a\n", 3*filterChunk))
	log := writeTemp(t, "log", "a\n")
	failing := func(err error) driverkit.Strategy {
		return withFilter(func(w io.Writer) driverkit.StreamFilter { return &brokenFilter{err: err} })
	}
	boom := errors.New("filter boom")
	for _, tc := range []struct {
		name   string
		shadow driverkit.Strategy
		failed func() error
		ctx    func() (context.Context, context.CancelFunc)
		want   string
	}{
		{"write error", failing(boom), noFailure, nil, "filter boom"},
		{"connector died", deltaStrategy(), func() error { return errors.New("connector trapped") }, nil, "connector trapped"},
		{"deadline", withFilter(func(w io.Writer) driverkit.StreamFilter { return slow{identity{w}} }), noFailure,
			func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 100*time.Millisecond)
			}, "filter shadow deadline exceeded after"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.Background(), func() {}
			if tc.ctx != nil {
				c, cc := tc.ctx()
				ctx, cancel = c, cc
			}
			defer cancel()
			start := time.Now()
			_, sum := runFilter(t, ctx, deltaStrategy(), tc.shadow, tc.failed, log, raw)
			if time.Since(start) > 5*time.Second {
				t.Errorf("filter shadow took %v, want it cut off", time.Since(start))
			}
			if sum.FilterRan || sum.Error != "" || !strings.Contains(sum.FilterError, tc.want) {
				t.Errorf("summary = %+v, want FilterError containing %q, no Error and not Ran", sum, tc.want)
			}
		})
	}
}

type brokenFilter struct{ err error }

func (f *brokenFilter) Write([]byte) (int, error) { return 0, f.err }
func (f *brokenFilter) Flush() error              { return nil }

type slow struct{ identity }

func (s slow) Write(p []byte) (int, error) {
	time.Sleep(30 * time.Millisecond)
	return s.identity.Write(p)
}

// Through Run: a Connector whose decode diverges but whose filter shadow still
// runs, the divergent filter reported and the native results the only ones used.
func TestRunReportsAFilterDivergenceAlongsideDecodeResults(t *testing.T) {
	raw := "a\ndelta x\ndone\nb\n"
	var ops []hostop.SpindriftOp
	Run(context.Background(), identityStrategy(), connectorhost.Load, fixture(t, "driver.wasm"),
		writeTemp(t, "log", raw), writeTemp(t, "raw", raw), 0, func(op hostop.SpindriftOp) { ops = append(ops, op) })
	divs, sum := split(t, ops)

	var filter, events int
	for _, d := range divs {
		switch d.Check {
		case "streamFilter":
			filter++
			if d.LineNo != 2 || string(d.Native) != `"delta x\ndone\nb\n"` || string(d.Shadow) != `"done\nb\n"` {
				t.Errorf("streamFilter divergence = %+v, want line 2 with both outputs", d)
			}
		case "events":
			events++
		}
	}
	if filter != 1 || events == 0 {
		t.Errorf("streamFilter = %d, events = %d divergences, want one filter divergence next to decode ones", filter, events)
	}
	if !sum.Loaded || sum.Error != "" || !sum.FilterRan || !sum.FilterDiverged || sum.FilterBytes != int64(len(raw)) || sum.Compared == 0 {
		t.Errorf("summary = %+v, want decode counts intact and a diverged filter shadow", sum)
	}
}

// A guest filter that traps ends only the filter shadow: the run completes.
func TestRunSurvivesAFilterTrap(t *testing.T) {
	raw := "a\nfilter-trap\nb\n"
	var ops []hostop.SpindriftOp
	Run(context.Background(), identityStrategy(), connectorhost.Load, fixture(t, "driver.wasm"),
		writeTemp(t, "log", raw), writeTemp(t, "raw", raw), 0, func(op hostop.SpindriftOp) { ops = append(ops, op) })
	_, sum := split(t, ops)
	if !sum.Loaded || sum.Error != "" || sum.Compared == 0 || sum.FilterRan || sum.FilterError == "" {
		t.Errorf("summary = %+v, want decode intact and a FilterError", sum)
	}
}

func TestRunWithoutARawCopyReportsAFilterError(t *testing.T) {
	var ops []hostop.SpindriftOp
	Run(context.Background(), identityStrategy(), connectorhost.Load, fixture(t, "driver.wasm"),
		writeTemp(t, "log", "a\n"), "", 0, func(op hostop.SpindriftOp) { ops = append(ops, op) })
	_, sum := split(t, ops)
	if sum.FilterRan || sum.FilterError == "" || sum.Error != "" {
		t.Errorf("summary = %+v, want a FilterError and no Error", sum)
	}
}

func TestFilterShadowCountsADivergenceInInvalidUTF8Bytes(t *testing.T) {
	// A byte-cut tool result: the outputs differ only in a byte that is not
	// valid UTF-8, which a lossy string encoding would turn into one and the
	// same U+FFFD.
	divs, sum := runFilter(t, context.Background(), identityStrategy(), identityStrategy(), noFailure,
		writeTemp(t, "log", "ok \xc3"), writeTemp(t, "raw", "ok \xff"))

	if len(divs) != 1 || sum.Diverged != 1 || !sum.FilterDiverged {
		t.Fatalf("divergences = %v, Diverged = %d, FilterDiverged = %v, want one counted divergence", divs, sum.Diverged, sum.FilterDiverged)
	}
	if bytes.Equal(divs[0].Native, divs[0].Shadow) {
		t.Errorf("native = %s, shadow = %s, want payloads that differ", divs[0].Native, divs[0].Shadow)
	}
}
