// Package dispatchrecord infers a Dispatch Record from a pass log file left
// under .spindrift/logs (ADR 0061). It is pure parsing: no store, no CLI.
package dispatchrecord

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmachine"
)

const (
	// KindUnknown is not a dispatch kind: it marks a log the parser could not
	// attribute to one. The real kinds come from package dispatchkind.
	KindUnknown = "unknown"

	// AttributionInferred marks a Record reconstructed from its log rather
	// than one a Dispatch wrote itself.
	AttributionInferred = "inferred"
	// AttributionStamped marks a Record whose logs carry the host's own
	// dispatch_start stamp (issue #4783), so nothing about it is inferred.
	AttributionStamped = "stamped"
	// OutcomeUnknown is the value of a Record's outcome field when the log
	// does not say how the Dispatch ended; unrelated to KindUnknown.
	OutcomeUnknown = "unknown"
	// OutcomeSourceSettled marks an Outcome taken from the host's dispatch_settled
	// op (issue #4785), the only source an Outcome has.
	OutcomeSourceSettled = "dispatch_settled"
	// OutcomeSourceNone marks a Record no log gave an Outcome.
	OutcomeSourceNone = "none"
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
	// Log is the base name of the log file the pass was read from; empty for a
	// pass migrated from a store that did not record it.
	Log string `json:"log"`
}

// Record is one Dispatch reconstructed from its log.
type Record struct {
	ID string `json:"record_id"`
	// Root is the checkout whose store holds the Record, filled when it is read
	// back: a stored path would go stale if the checkout moved.
	Root        string    `json:"root"`
	Kind        string    `json:"kind"`
	DispatchKey string    `json:"dispatch_key"`
	ClaimTime   time.Time `json:"claim_time"`
	Attribution string    `json:"attribution"`
	Outcome     string    `json:"outcome"`
	// OutcomeSource names where Outcome came from: OutcomeSourceSettled for the
	// host's dispatch_settled op, OutcomeSourceNone when no log said.
	OutcomeSource string `json:"outcome_source"`
	Reason        string `json:"reason,omitempty"`
	Note          string `json:"note,omitempty"`
	PRURL         string `json:"pr_url,omitempty"`
	// BoxStatus is the Box's own SPINDRIFT_OUTCOME status= self-report; it never
	// stands in for Outcome.
	BoxStatus string `json:"box_status,omitempty"`
	Passes    []Pass `json:"passes"`

	// Stamped Records only: what the host recorded about the Dispatch.
	Revision      string            `json:"revision,omitempty"`
	Driver        string            `json:"driver,omitempty"`
	DriverVersion string            `json:"driver_version,omitempty"`
	RoleModels    map[string]string `json:"role_models,omitempty"`
	Knobs         map[string]string `json:"knobs,omitempty"`

	// PromptHashes maps a pass role to the hash of its prompt template. Unlike
	// the fields above it is Box-reported, so untrusted.
	PromptHashes map[string]string `json:"prompt_hashes,omitempty"`
}

// ErrEmptyLog is returned by ParseLog for a log with no non-blank line at all
// (empty or whitespace-only): nothing was written, so there is no Dispatch to
// record yet. Any output, even a bare startup error, yields a zero-cost Record.
var ErrEmptyLog = errors.New("dispatchrecord: log has no non-blank line")

// ErrUnstamped is returned by ParseLog for a log that is not named like a
// ChainKey file and does not open with a dispatch_start stamp: only a stamp can
// attribute such a log (a primary's rotated attempt) to a Dispatch.
var ErrUnstamped = errors.New("dispatchrecord: log is not stamped and not a chain log")

// PassLogName reports whether name is a pass log of any kind: an attempt,
// fix, or conflict-resolve log, optionally rotated ("<path>.N") and/or
// quarantined ("<path>.prior-run.N"). It excludes .warnings and .run-lineage.
func PassLogName(name string) bool { return passLogName.MatchString(name) }

var passLogName = regexp.MustCompile(`^issue-.+\.log(?:\.\d+)?(?:\.prior-run\.\d+)?$`)

// chainName matches a Dispatch's first-attempt log and its quarantined
// earlier runs. A primary's "<path>.N" rotations are not chain files; only
// satellites' rotations are accepted (see satelliteName).
var chainName = regexp.MustCompile(`^issue-(.+)\.log(?:\.prior-run\.\d+)?$`)

// satelliteName matches the host's fix-pass and conflict-resolve logs, their
// "<path>.N" rotated attempts, and their quarantined earlier runs. Such a log
// belongs to whichever Dispatch of its key was running when it began.
var satelliteName = regexp.MustCompile(`^issue-(.+?)(?:-fix-\d+|-conflict-resolve)\.log(?:\.\d+)?(?:\.prior-run\.\d+)?$`)

// ChainKey classifies the base name of a log file. ok is false for names that
// are no Dispatch log. A primary log, issue-<key>.log or its
// issue-<key>.log.prior-run.N quarantine, is a Dispatch of its own; a
// satellite (issue-<key>-fix-P.log, issue-<key>-conflict-resolve.log, with an
// optional .N or .prior-run.N suffix) carries more passes of a Dispatch that
// a primary log names.
func ChainKey(name string) (key string, satellite, ok bool) {
	if m := satelliteName.FindStringSubmatch(name); m != nil {
		return m[1], true, true
	}
	if m := chainName.FindStringSubmatch(name); m != nil {
		return m[1], false, true
	}
	return "", false, false
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

// ParseLog infers a Record from the pass log at path. A log whose first event
// is a dispatch_start stamp yields a stamped Record, whatever its name; any
// other log must be named like a ChainKey file (else ErrUnstamped) and yields an
// inferred one. An unstamped satellite log parses like a primary one; its
// Record's claim time is the log's own start, which the store uses to window it
// into the Dispatch it belongs to. A log with no non-blank line yields
// ErrEmptyLog. The claim time of an inferred Record is the first timestamped
// event in file order, which later appends cannot move. provisional reports
// that no event carried a timestamp, so the claim time fell back to the file
// mtime and appended output can still move the ID.
func ParseLog(path string) (rec Record, provisional bool, err error) {
	rec, provisional, _, err = parseLog(path)
	return rec, provisional, err
}

// StampRecordID returns the Record ID of the log's leading dispatch_start
// stamp, "" when the log is missing, empty, or does not open with one. It
// decodes only the first event, by the same rule parseLog applies.
func StampRecordID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	br := bufio.NewReader(f)
	for {
		line, isPrefix, err := br.ReadLine()
		if err != nil || isPrefix {
			// A stamp is a short line; an oversized one is Box output.
			return ""
		}
		var ev logLine
		if json.Unmarshal(bytes.TrimSpace(line), &ev) != nil || ev.Type == "" {
			continue
		}
		if op := ev.SpindriftOp; ev.Type == "spindrift_op" && op != nil && op.Op == claude.OpDispatchStart && op.Start != nil {
			return op.Start.RecordID
		}
		return ""
	}
}

// parseLog is ParseLog plus the log's segment identity: the stamp's Started, or
// the claim time of an inferred Record. It keys the log's passes within a
// Record that several logs contribute to.
func parseLog(path string) (rec Record, provisional bool, segment time.Time, err error) {
	base := filepath.Base(path)
	key, _, chain := ChainKey(base)
	info, err := os.Stat(path)
	if err != nil {
		return Record{}, false, time.Time{}, err
	}

	var (
		passes    = []Pass{}
		cur       = newPass(1, "", false)
		firstRole string
		sawStart  bool
		firstTS   time.Time
		haveTS    bool
		sawLine   bool
		sawEvent  bool
		stamp     *claude.DispatchStart
		// hashes holds reported prompt hashes by the record_id they claim, since
		// the op need not follow the stamp it must match.
		hashes  = map[string]map[string]string{}
		settled *claude.DispatchSettled
		// boxStatus is the status of the last SPINDRIFT_OUTCOME line in any result.
		boxStatus string
		// status is the last plain-text SPINDRIFT_OUTCOME line's status.
		status string
		// announced is the kind named by the Box's first plain-text announce
		// line ("==> claude <verb> ..."); empty when the log has none.
		announced string
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
		if s == "" {
			return
		}
		sawLine = true
		// The agent's outcome line and the Box's announce line are plain text,
		// not stream-json events, so they must be read before the JSON
		// prefilter below drops them.
		if r, ok := outcome.SelfReportFromLogLine(s); ok {
			status = r.Status
			return
		}
		if k := announcedKind(s); k != "" {
			if announced == "" {
				announced = k
			}
			return
		}
		if !strings.Contains(s, `"timestamp"`) && !strings.Contains(s, `"type":"result"`) &&
			!strings.Contains(s, `"type":"assistant"`) && !strings.Contains(s, `"spindrift_op"`) &&
			!strings.Contains(s, `"tool_result"`) {
			return
		}
		var ev logLine
		if json.Unmarshal([]byte(s), &ev) != nil || ev.Type == "" {
			return
		}
		first := !sawEvent
		sawEvent = true
		// The host appends only dispatch_settled ops after the Box exits, so
		// anything else after a settled op means the Box printed it.
		if ev.Type != "spindrift_op" || ev.SpindriftOp == nil || ev.SpindriftOp.Op != claude.OpDispatchSettled {
			settled = nil
		}
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
			case claude.OpDispatchStart:
				if first && op.Start != nil && op.Start.RecordID != "" {
					stamp = op.Start
				}
			case "prompt_hashes":
				if h := op.PromptHashes; h != nil {
					if hashes[h.RecordID] == nil {
						hashes[h.RecordID] = map[string]string{}
					}
					maps.Copy(hashes[h.RecordID], h.Roles)
				}
			case claude.OpDispatchSettled:
				// An op naming another Record is not this log's to claim, and
				// must not displace one that is.
				if op.Settled != nil && stamp != nil && op.Settled.RecordID == stamp.RecordID {
					settled = op.Settled
				}
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
			if line := outcome.ExtractOutcomeLine(outcome.StripResultText(ev.Result)); line != "" {
				if o, perr := outcome.Parse(line); perr == nil {
					boxStatus = o.Status
				}
			}
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
		return Record{}, false, time.Time{}, err
	}
	if !sawLine {
		return Record{}, false, time.Time{}, ErrEmptyLog
	}
	closeCur()
	for i := range passes {
		passes[i].Log = base
	}

	if stamp != nil {
		claim := stamp.ClaimTime.UTC()
		rec := Record{
			ID:            stamp.RecordID,
			Kind:          stamp.Kind,
			DispatchKey:   stamp.DispatchKey,
			ClaimTime:     claim,
			Attribution:   AttributionStamped,
			Outcome:       OutcomeUnknown,
			OutcomeSource: OutcomeSourceNone,
			Passes:        passes,
			Revision:      stamp.Revision,
			Driver:        stamp.Driver,
			DriverVersion: stamp.DriverVersion,
			RoleModels:    stamp.RoleModels,
			Knobs:         stamp.Knobs,
			PromptHashes:  hashes[stamp.RecordID],
		}
		// Only the log settle read (the one carrying it) speaks for BoxStatus.
		if settled != nil {
			rec.Outcome, rec.OutcomeSource = settled.State, OutcomeSourceSettled
			rec.Reason, rec.Note, rec.PRURL = settled.Reason, settled.Note, settled.PRURL
			rec.BoxStatus = boxStatus
		}
		return rec, false, stamp.Started.UTC(), nil
	}
	if !chain {
		return Record{}, false, time.Time{}, ErrUnstamped
	}

	claim := info.ModTime()
	if haveTS {
		claim = firstTS
	}
	claim = claim.UTC()

	kind := KindUnknown
	sk := statusKind(status)
	switch {
	case dispatchkey.IsChoreKey(key):
		kind = choreKeyedKind()
	case announced != "" && announced != dispatchkind.Work.Name:
		// Only work's verb is ambiguous: logs before #734 announce research as
		// "implementing" too, so a kind-unique status must outrank that verb.
		// A frozen quirk of old logs, not a kind fact, so it stays off the
		// descriptor.
		kind = announced
	case sk != KindUnknown:
		kind = sk
	case announced != "":
		kind = announced
	case firstRole != "":
		kind = dispatchkind.Work.Name
	case status == outcome.StatusBlocked:
		// Pre-orchestrator single-pass run: no pass_start names a role. Of the
		// compiled-default statuses only the shared blocked is still unsettled
		// here; any other status falls through to KindUnknown.
		kind = dispatchkind.Work.Name
	}

	return Record{
		ID:            RecordID(kind, key, claim),
		Kind:          kind,
		DispatchKey:   key,
		ClaimTime:     claim,
		Attribution:   AttributionInferred,
		Outcome:       OutcomeUnknown,
		OutcomeSource: OutcomeSourceNone,
		Passes:        passes,
	}, !haveTS, claim, nil
}

// soleKind is the name of the one kind whose descriptor satisfies match, or
// KindUnknown if none or several do.
func soleKind(match func(*dispatchkind.Descriptor) bool) string {
	name := KindUnknown
	for _, d := range dispatchkind.All {
		if !match(d) {
			continue
		}
		if name != KindUnknown {
			return KindUnknown
		}
		name = d.Name
	}
	return name
}

// choreKeyedKind is the name of the one kind keyed by Ledger Chore, or
// KindUnknown if the descriptors do not single one out.
func choreKeyedKind() string {
	return soleKind(func(d *dispatchkind.Descriptor) bool { return d.Keying == dispatchkind.ByChore })
}

// statusKind is the name of the one kind whose descriptor lists status, or
// KindUnknown if none or several do (blocked is shared, so it names no kind).
func statusKind(status string) string {
	return soleKind(func(d *dispatchkind.Descriptor) bool { return slices.Contains(d.Statuses, status) })
}

// announcedKind is the kind whose Box start line ("==> claude <verb> ...") s
// is, or "" when s is not one. Verbs come from the descriptors, so a new kind
// is inferable without touching this.
func announcedKind(s string) string {
	for _, d := range dispatchkind.All {
		if strings.HasPrefix(s, dispatchkind.AnnouncePrefix+d.AnnounceVerb+" ") {
			return d.Name
		}
	}
	return ""
}
