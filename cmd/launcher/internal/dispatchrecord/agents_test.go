package dispatchrecord

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/seamtest"
)

func subagentCall(msgID, role string, in, out, cacheRead, cacheWrite int) string {
	return seamtest.Line(driverkit.Event{Kind: driverkit.KindUsage, MessageID: msgID, InSubagent: true, Role: role,
		Tokens: driverkit.Tokens{Input: in, Output: out, CacheRead: cacheRead, CacheWrite5m: cacheWrite}})
}

func agentsLog() []string {
	return []string{
		driverStamp(altDriver),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		usageCall("m1", "", 1, 100, 1000, 200, 0),
		subagentCall("s1", "worker", 10, 50, 500, 20),
		subagentCall("s2", "worker", 20, 60, 600, 30),
		subagentCall("s3", "scout", 5, 5, 5, 5),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"}),
		usageCall("m2", "", 2, 200, 2000, 300, 0),
	}
}

func TestParseLogPassAgentsBreakDownEachPass(t *testing.T) {
	rec, _, err := ParseLog(writeLog(t, "issue-42.log", agentsLog()...))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Passes) != 2 {
		t.Fatalf("passes = %+v", rec.Passes)
	}
	want1 := []PassAgent{
		{Agent: "main", APICalls: 1, InputTokens: 1, OutputTokens: 100, CacheReadInputTokens: 1000, CacheCreationInputTokens: 200},
		{Agent: "worker", APICalls: 2, InputTokens: 30, OutputTokens: 110, CacheReadInputTokens: 1100, CacheCreationInputTokens: 50},
		{Agent: "scout", APICalls: 1, InputTokens: 5, OutputTokens: 5, CacheReadInputTokens: 5, CacheCreationInputTokens: 5},
	}
	if got := rec.Passes[0].Agents; !reflect.DeepEqual(got, want1) {
		t.Fatalf("pass 1 agents = %+v, want %+v", got, want1)
	}
	want2 := []PassAgent{{Agent: "main", APICalls: 1, InputTokens: 2, OutputTokens: 200, CacheReadInputTokens: 2000, CacheCreationInputTokens: 300}}
	if got := rec.Passes[1].Agents; !reflect.DeepEqual(got, want2) {
		t.Fatalf("pass 2 agents = %+v, want %+v", got, want2)
	}
	for _, p := range rec.Passes {
		var sum Pass
		for _, a := range p.Agents {
			sum.InputTokens += a.InputTokens
			sum.OutputTokens += a.OutputTokens
			sum.CacheReadInputTokens += a.CacheReadInputTokens
			sum.CacheCreationInputTokens += a.CacheCreationInputTokens
			sum.APICalls += a.APICalls
		}
		if sum.InputTokens != p.InputTokens || sum.OutputTokens != p.OutputTokens ||
			sum.CacheReadInputTokens != p.CacheReadInputTokens || sum.CacheCreationInputTokens != p.CacheCreationInputTokens ||
			sum.APICalls != p.APICalls {
			t.Fatalf("pass %d agents sum %+v != pass %+v", p.Ordinal, sum, p)
		}
	}
}

func TestParseLogPassWithoutUsageHasNoAgents(t *testing.T) {
	rec, _, err := ParseLog(writeLog(t, "issue-42.log", driverStamp(altDriver),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}), outcomeResult("ready")))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Passes) == 0 || len(rec.Passes[0].Agents) != 0 {
		t.Fatalf("passes = %+v", rec.Passes)
	}
}

func TestStoreRoundTripsPassAgentsAndReupsertReplacesThem(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-42.log", agentsLog()...)
	rec, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	s := openStore(t, root)
	ingest(t, s)
	recs := records(t, s)
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	for i := range rec.Passes {
		if !reflect.DeepEqual(recs[0].Passes[i].Agents, rec.Passes[i].Agents) {
			t.Fatalf("pass %d agents = %+v, want %+v", i, recs[0].Passes[i].Agents, rec.Passes[i].Agents)
		}
	}
	if len(recs[0].Passes[0].Agents) != 3 {
		t.Fatalf("agents = %+v", recs[0].Passes[0].Agents)
	}

	// The log regrows to a different breakdown; the old rows must not linger.
	putLog(t, root, "issue-42.log", append(agentsLog()[:2], usageCall("m1", "", 1, 100, 1000, 200, 0))...)
	if _, err := s.Reingest(); err != nil {
		t.Fatal(err)
	}
	recs = records(t, s)
	if len(recs) != 1 || len(recs[0].Passes) != 1 {
		t.Fatalf("records = %+v", recs)
	}
	if got := recs[0].Passes[0].Agents; len(got) != 1 || got[0].Agent != "main" {
		t.Fatalf("agents after re-upsert = %+v", got)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM pass_agents").Scan(&n); err != nil || n != 1 {
		t.Fatalf("pass_agents rows = %d, err %v", n, err)
	}
}

func TestStoreMigratesV11DatabaseGainsPassAgents(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-42.log", agentsLog()...)
	rec, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	// Ingest with the current code, then roll the database back to the v11
	// shape: the record and passes stay, the agent rows and the version go.
	old, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := ingest(t, old); n == 0 {
		t.Fatal("first ingest read nothing")
	}
	for _, q := range []string{"DROP TABLE pass_agents", "PRAGMA user_version = 11"} {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s := openStore(t, root)
	if n := ingest(t, s); n == 0 {
		t.Fatal("the migrated file was not re-read")
	}
	recs := records(t, s)
	if len(recs) != 1 || len(recs[0].Passes) != len(rec.Passes) {
		t.Fatalf("records = %+v", recs)
	}
	for i := range rec.Passes {
		if !reflect.DeepEqual(recs[0].Passes[i].Agents, rec.Passes[i].Agents) {
			t.Fatalf("pass %d agents = %+v, want %+v", i, recs[0].Passes[i].Agents, rec.Passes[i].Agents)
		}
	}
	for table, want := range map[string]int{"records": 1, "passes": len(rec.Passes), "pass_agents": 4} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s rows = %d, want %d, err %v", table, n, want, err)
		}
	}
}
