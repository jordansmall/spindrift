package dispatchrecord

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/hostpaths"
)

func settledDS(recordID string) hostop.DispatchSettled {
	return hostop.DispatchSettled{
		RecordID: recordID, State: "complete", Reason: "merged", Note: "n", PRURL: "https://x/pr/1",
	}
}

func settledRecords(t *testing.T, root string) []Record {
	t.Helper()
	return records(t, openStore(t, root))
}

func TestRecordSettledIngestsSettledOutcome(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := putLog(t, root, "issue-7.log", stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1)...)
	var warn bytes.Buffer

	RecordSettled(&warn, "7", path, settledDS(RecordID("work", "42", stampClaim)))

	recs := settledRecords(t, root)
	if len(recs) != 1 {
		t.Fatalf("records = %+v", recs)
	}
	r := recs[0]
	if r.Outcome != "complete" || r.OutcomeSource != OutcomeSourceSettled || r.Reason != "merged" || r.PRURL != "https://x/pr/1" {
		t.Fatalf("record = %+v", r)
	}
	if warn.Len() != 0 {
		t.Fatalf("warn = %q", warn.String())
	}
}

func TestRecordSettledTokenedStampSurvivesIngest(t *testing.T) {
	t.Parallel()
	id := RecordID("work", "42", stampClaim)
	for name, stamp := range map[string]string{
		"tokened": tokenStampLine(stampClaim, "tok-1"),
		"legacy":  tokenStampLine(stampClaim, ""),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := putLog(t, root, "issue-7.log", append([]string{stamp}, workLog("2026-05-01T09:00:00Z", 1)...)...)
			var warn bytes.Buffer

			RecordSettled(&warn, "7", path, settledDS(id))

			recs := settledRecords(t, root)
			if len(recs) != 1 || recs[0].Outcome != "complete" || recs[0].OutcomeSource != OutcomeSourceSettled {
				t.Fatalf("records = %+v", recs)
			}
		})
	}
}

func TestAppendSettledWritesEncodedOpWithStampToken(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	id := RecordID("work", "42", stampClaim)
	prior := append([]string{tokenStampLine(stampClaim, "tok-1")}, workLog("2026-05-01T09:00:00Z", 1)...)
	path := putLog(t, root, "issue-7.log", prior...)
	var warn bytes.Buffer

	if !AppendSettled(&warn, "7", path, settledDS(id)) {
		t.Fatalf("AppendSettled = false, warn %q", warn.String())
	}

	want := settledDS(id)
	want.HostToken = "tok-1"
	line := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchSettled, Settled: &want})
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != strings.Join(prior, "")+line {
		t.Fatalf("log = %q", got)
	}
}

func TestAppendSettledForeignRecordIDDoesNotEchoToken(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := putLog(t, root, "issue-7.log", tokenStampLine(stampClaim, "tok-1"))
	ds := settledDS("work:99@elsewhere")

	if !AppendSettled(&bytes.Buffer{}, "7", path, ds) {
		t.Fatal("AppendSettled = false")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchSettled, Settled: &ds})
	if !strings.HasSuffix(string(got), line) {
		t.Fatalf("log = %q", got)
	}
}

func TestRecordSettledMissingLogIsNotCreated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(hostpaths.LogDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(hostpaths.LogDir(root), "issue-7.log")
	var warn bytes.Buffer

	RecordSettled(&warn, "7", path, settledDS(RecordID("work", "42", stampClaim)))

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("log was created: err = %v", err)
	}
	if !strings.HasPrefix(warn.String(), "    ?? 7: could not append "+hostop.OpDispatchSettled+" to "+path+": ") {
		t.Fatalf("warn = %q", warn.String())
	}
	if recs := settledRecords(t, root); len(recs) != 0 {
		t.Fatalf("records = %+v", recs)
	}
}

func TestRecordSettledEmptyRecordIDLeavesLogAlone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := putLog(t, root, "issue-7.log", stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1)...)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer

	RecordSettled(&warn, "7", path, settledDS(""))

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("log changed: %q", after)
	}
	want := "    ?? 7: no record id, not appending " + hostop.OpDispatchSettled + " to " + path + "\n"
	if warn.String() != want {
		t.Fatalf("warn = %q, want %q", warn.String(), want)
	}
	if recs := settledRecords(t, root); len(recs) != 0 {
		t.Fatalf("records = %+v", recs)
	}
}

func TestRecordSettledEmptyLogPathIsSilent(t *testing.T) {
	t.Parallel()
	var warn bytes.Buffer

	RecordSettled(&warn, "7", "", settledDS(RecordID("work", "42", stampClaim)))

	if warn.Len() != 0 {
		t.Fatalf("warn = %q", warn.String())
	}
}

func TestAppendSettledDoesNotIngest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := putLog(t, root, "issue-7.log", stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1)...)

	if !AppendSettled(&bytes.Buffer{}, "7", path, settledDS(RecordID("work", "42", stampClaim))) {
		t.Fatal("AppendSettled = false")
	}

	if recs := settledRecords(t, root); len(recs) != 0 {
		t.Fatalf("records before ingest = %+v", recs)
	}
	s := openStore(t, root)
	if _, err := s.IngestChain(filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	if recs := records(t, s); len(recs) != 1 || recs[0].OutcomeSource != OutcomeSourceSettled {
		t.Fatalf("records after ingest = %+v", recs)
	}
}
