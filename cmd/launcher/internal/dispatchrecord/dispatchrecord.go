// Package dispatchrecord infers a Dispatch Record from a pass log file left
// under .spindrift/logs (ADR 0061). It is pure parsing: no store, no CLI.
package dispatchrecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/passmachine"
)

const (
	// KindUnknown is not a dispatch kind: it marks a log the parser could not
	// attribute to one. The real kinds come from package dispatchkind.
	KindUnknown = "unknown"

	// AttributionInferred marks a Record reconstructed from its log rather
	// than one a Dispatch wrote itself.
	AttributionInferred = "inferred"
	// OutcomeUnknown is the value of a Record's outcome field when the log
	// does not say how the Dispatch ended; unrelated to KindUnknown.
	OutcomeUnknown = "unknown"
)

// Pass is the spend of one pass within a Dispatch.
type Pass struct {
	Ordinal                  int      `json:"ordinal"`
	Role                     string   `json:"role"`
	Models                   []string `json:"models"`
	USD                      float64  `json:"usd"`
	InputTokens              int      `json:"input_tokens"`
	OutputTokens             int      `json:"output_tokens"`
	CacheReadInputTokens     int      `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int      `json:"cache_creation_input_tokens"`
	APICalls                 int      `json:"api_calls"`
	Turns                    int      `json:"turns"`
	DurationMs               int64    `json:"duration_ms"`
	APIDurationMs            int64    `json:"api_duration_ms"`
	Verdict                  string   `json:"verdict,omitempty"`
	// VerdictText and Dispositions are Box-written, untrusted prose stored
	// verbatim.
	VerdictText  string `json:"verdict_text,omitempty"`
	Dispositions string `json:"dispositions,omitempty"`
}

// Record is one Dispatch reconstructed from its log.
type Record struct {
	ID          string    `json:"record_id"`
	Kind        string    `json:"kind"`
	DispatchKey string    `json:"dispatch_key"`
	ClaimTime   time.Time `json:"claim_time"`
	Attribution string    `json:"attribution"`
	Outcome     string    `json:"outcome"`
	Passes      []Pass    `json:"passes"`
}

// ErrNoEvents is returned by ParseLog for a log with no parsed event at all
// (empty, or only unparseable lines): there is no Dispatch to record yet.
var ErrNoEvents = errors.New("dispatchrecord: log has no events")

// chainName matches a Dispatch's first-attempt log and its quarantined
// earlier runs; fix, conflict-resolve, and "<path>.N" rotated attempt logs
// are other files of the chain and do not match.
var chainName = regexp.MustCompile(`^issue-(.+)\.log(?:\.prior-run\.\d+)?$`)

var chainSuffix = regexp.MustCompile(`-fix-\d+$|-conflict-resolve$`)

// ChainKey extracts the Dispatch key from the base name of a bare
// issue-<key>.log file or its issue-<key>.log.prior-run.N quarantine.
func ChainKey(name string) (key string, ok bool) {
	m := chainName.FindStringSubmatch(name)
	if m == nil || chainSuffix.MatchString(m[1]) {
		return "", false
	}
	return m[1], true
}

// logLine is the union of the stream-json fields this package reads. The
// embedded Event supplies type, message, and the synthetic spindrift_op.
type logLine struct {
	claude.Event
	Timestamp     string  `json:"timestamp"`
	Result        string  `json:"result"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	DurationMs    int64   `json:"duration_ms"`
	DurationApiMs int64   `json:"duration_api_ms"`
	Usage         struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]json.RawMessage `json:"modelUsage"`
}

type passAcc struct {
	Pass
	models   map[string]bool
	seenIDs  map[string]bool
	opCalls  int
	results  int
	explicit bool
}

func newPass(ordinal int, role string, explicit bool) *passAcc {
	return &passAcc{Pass: Pass{Ordinal: ordinal, Role: role}, models: map[string]bool{}, seenIDs: map[string]bool{}, explicit: explicit}
}

func (p *passAcc) finish() Pass {
	out := p.Pass
	out.Models = make([]string, 0, len(p.models))
	for m := range p.models {
		out.Models = append(out.Models, m)
	}
	sort.Strings(out.Models)
	if p.opCalls > 0 {
		out.APICalls = p.opCalls
	}
	return out
}

// RecordID is the identity of a Dispatch Record: its kind, key, and claim time
// in UTC to the millisecond.
func RecordID(kind, key string, claim time.Time) string {
	return fmt.Sprintf("%s:%s@%s", kind, key, claim.UTC().Format("2006-01-02T15:04:05.000Z"))
}

// ParseLog infers a Record from the pass log at path, which must be named like
// a ChainKey file. A log with no events yields ErrNoEvents. The claim time is
// the first timestamped event in file order, which later appends cannot move.
// provisional reports that no event carried a timestamp, so the claim time fell
// back to the file mtime and appended output can still move the ID.
func ParseLog(path string) (rec Record, provisional bool, err error) {
	key, ok := ChainKey(filepath.Base(path))
	if !ok {
		return Record{}, false, fmt.Errorf("dispatchrecord: %q is not an issue-<key>.log file", filepath.Base(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		return Record{}, false, err
	}

	var (
		passes    = []Pass{}
		cur       = newPass(1, "", false)
		firstRole string
		sawStart  bool
		firstTS   time.Time
		haveTS    bool
		sawEvent  bool
		// pendingDispositions holds a fix pass's Write to the dispositions file,
		// by tool_use ID, until its tool_result shows the Write took effect.
		pendingDispositions = map[string]string{}
	)
	closeCur := func() {
		// A stray implicit pass is real only if it produced a result; an
		// explicit one stays even when the run died before its first turn.
		if cur.explicit || cur.results > 0 {
			passes = append(passes, cur.finish())
		}
	}

	err = driverkit.ScanLog(path, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if !strings.Contains(s, `"timestamp"`) && !strings.Contains(s, `"type":"result"`) &&
			!strings.Contains(s, `"type":"assistant"`) && !strings.Contains(s, `"spindrift_op"`) &&
			!strings.Contains(s, `"tool_result"`) {
			return
		}
		var ev logLine
		if json.Unmarshal([]byte(s), &ev) != nil || ev.Type == "" {
			return
		}
		sawEvent = true
		if ev.Timestamp != "" && !haveTS {
			if t, perr := time.Parse(time.RFC3339Nano, ev.Timestamp); perr == nil {
				firstTS, haveTS = t, true
			}
		}
		switch ev.Type {
		case "spindrift_op":
			op := ev.SpindriftOp
			if op == nil {
				return
			}
			switch op.Op {
			case "pass_start":
				if !sawStart {
					firstRole = op.Role
				}
				sawStart = true
				closeCur()
				clear(pendingDispositions)
				// Strictly increasing: an implicit pass 1 before a pass_start
				// that also says pass 1 must not collide in the store.
				last := 0
				if len(passes) > 0 {
					last = passes[len(passes)-1].Ordinal
				}
				ordinal := op.Pass
				if ordinal <= last {
					ordinal = last + 1
				}
				cur = newPass(ordinal, op.Role, true)
			case "verdict":
				cur.Verdict = op.Verdict
			case "pass_usage":
				if op.Usage != nil {
					cur.opCalls = op.Usage.APICalls
				}
			}
		case "assistant":
			if ev.Message == nil {
				return
			}
			// Stream-json repeats one message ID across events that each
			// carry different blocks, so scan blocks before deduping.
			if cur.Role == string(passmachine.RoleFix) {
				for _, b := range ev.Message.Content {
					if b.Type != "tool_use" || b.Name != "Write" {
						continue
					}
					var in struct {
						FilePath string `json:"file_path"`
						Content  string `json:"content"`
					}
					if json.Unmarshal(b.Input, &in) == nil && in.FilePath == passmachine.DispositionsPath {
						pendingDispositions[b.ID] = in.Content
					}
				}
			}
			if id := ev.Message.ID; id != "" {
				if cur.seenIDs[id] {
					return
				}
				cur.seenIDs[id] = true
			}
			cur.APICalls++
		case "user":
			if ev.Message == nil {
				return
			}
			for _, b := range ev.Message.Content {
				if content, ok := pendingDispositions[b.ToolUseID]; ok && b.Type == "tool_result" {
					delete(pendingDispositions, b.ToolUseID)
					if !b.IsError {
						cur.Dispositions = content
					}
				}
			}
		case "result":
			cur.results++
			if passmachine.Role(cur.Role).IsReview() {
				cur.VerdictText = ev.Result
			}
			cur.USD += ev.TotalCostUSD
			cur.InputTokens += ev.Usage.InputTokens
			cur.OutputTokens += ev.Usage.OutputTokens
			cur.CacheReadInputTokens += ev.Usage.CacheReadInputTokens
			cur.CacheCreationInputTokens += ev.Usage.CacheCreationInputTokens
			cur.Turns += ev.NumTurns
			cur.DurationMs += ev.DurationMs
			cur.APIDurationMs += ev.DurationApiMs
			for m := range ev.ModelUsage {
				cur.models[m] = true
			}
		}
	})
	if err != nil {
		return Record{}, false, err
	}
	if !sawEvent {
		return Record{}, false, ErrNoEvents
	}
	closeCur()

	claim := info.ModTime()
	if haveTS {
		claim = firstTS
	}
	claim = claim.UTC()

	kind := KindUnknown
	switch {
	case dispatchkey.IsChoreKey(key):
		kind = choreKeyedKind()
	case firstRole != "":
		kind = dispatchkind.Work.Name
	}

	return Record{
		ID:          RecordID(kind, key, claim),
		Kind:        kind,
		DispatchKey: key,
		ClaimTime:   claim,
		Attribution: AttributionInferred,
		Outcome:     OutcomeUnknown,
		Passes:      passes,
	}, !haveTS, nil
}

// choreKeyedKind is the name of the one kind keyed by Ledger Chore, or
// KindUnknown if the descriptors do not single one out.
func choreKeyedKind() string {
	name := KindUnknown
	for _, d := range dispatchkind.All {
		if d.Keying != dispatchkind.ByChore {
			continue
		}
		if name != KindUnknown {
			return KindUnknown
		}
		name = d.Name
	}
	return name
}
