// Package dispatchrecord infers a Dispatch Record from a pass log file left
// under .spindrift/logs (ADR 0061). It is pure parsing: no store, no CLI.
package dispatchrecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
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
	// SettledSeq is the store's monotonic settle order, 0 until settled.
	SettledSeq int64 `json:"settled_seq,omitempty"`
	// MergeCommit is the forge's merge commit for a merged PR, when the host
	// recorded one.
	MergeCommit string `json:"merge_commit,omitempty"`
	// Reverted, Churn14d and MaturedAt are filled by FillMaturity, never by a
	// log: nil until the window after MergeCommit has been checked against a
	// clone that covers it. Churn14d is the fraction (0 to 1) of the merge's
	// added lines that other commits rewrote within the window; it stays nil
	// on a matured Record whose lines cannot be told (see churnWithinWindow).
	Reverted  *bool      `json:"reverted,omitempty"`
	Churn14d  *float64   `json:"churn_14d,omitempty"`
	MaturedAt *time.Time `json:"matured_at,omitempty"`
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

// passShell is what the host ops say about one pass-log segment; mergePasses
// fills in the rest from the Driver's tally.
type passShell struct {
	ordinal  int
	role     string
	verdict  string
	opCalls  int
	explicit bool
}

// mergePasses joins each segment's shell with its tally into Passes and
// returns the status of the last parseable SPINDRIFT_OUTCOME in any result.
// tallies holds one entry per shell: both split the log at every pass_start.
func mergePasses(shells []*passShell, tallies []driverkit.PassTally, log string) (passes []Pass, boxStatus string) {
	passes = []Pass{}
	for i, sh := range shells {
		t := tallies[i]
		for _, text := range t.Results {
			if line := outcome.ExtractOutcomeLine(outcome.StripResultText(text)); line != "" {
				if o, err := outcome.Parse(line); err == nil {
					boxStatus = o.Status
				}
			}
		}
		// A stray implicit pass is real only if it produced a result; an
		// explicit one stays even when the run died before its first turn.
		if !sh.explicit && len(t.Results) == 0 {
			continue
		}
		// Strictly increasing: an implicit pass 1 before a pass_start that also
		// says pass 1 must not collide in the store.
		ordinal := sh.ordinal
		if n := len(passes); n > 0 && ordinal <= passes[n-1].Ordinal {
			ordinal = passes[n-1].Ordinal + 1
		}
		p := Pass{
			Ordinal: ordinal, Role: sh.role, Models: t.Models, Verdict: sh.verdict, Log: log,
			USD: t.Usage.TotalCostUSD, InputTokens: t.Usage.InputTokens, OutputTokens: t.Usage.OutputTokens,
			CacheReadInputTokens: t.Usage.CacheReadInputTokens, CacheCreationInputTokens: t.Usage.CacheCreationInputTokens,
			APICalls: t.APICalls, Turns: t.Usage.NumTurns, DurationMs: t.Usage.DurationMs, APIDurationMs: t.Usage.DurationApiMs,
		}
		if p.Models == nil {
			p.Models = []string{}
		}
		if sh.opCalls > 0 {
			p.APICalls = sh.opCalls
		}
		if passmachine.Role(sh.role).IsReview() && len(t.Results) > 0 {
			p.VerdictText = t.Results[len(t.Results)-1]
		}
		if sh.role == string(passmachine.RoleFix) && t.Wrote {
			p.Dispositions = t.Written
		}
		passes = append(passes, p)
	}
	return passes, boxStatus
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

// isStamp reports whether op is a dispatch_start naming a Record, the only op
// that can open a log as its stamp.
func isStamp(op *hostop.SpindriftOp) bool {
	return op != nil && op.Op == hostop.OpDispatchStart && op.Start != nil && op.Start.RecordID != ""
}

// ReadStamp returns the log's leading dispatch_start stamp, false when the log is
// missing, empty, or does not open with one. It takes the first line that parses
// as a typed event, a looser rule than the Driver-decoded events parseLog's loop
// counts, so parseLog checks the stamp the loop accepts still names the Driver
// ReadStamp chose. It reads through logscan so an oversized line is skipped
// exactly as the parse loop skips it.
func ReadStamp(path string) (stamp hostop.DispatchStart, ok bool) {
	_ = logscan.ForEachLineUntil(path, logscan.SkipOversized, func(line string) bool {
		var ev hostop.Event
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &ev) != nil || ev.Type == "" {
			return true
		}
		if op := ev.SpindriftOp; ev.Type == hostop.TypeSpindriftOp && isStamp(op) {
			stamp, ok = *op.Start, true
		}
		return false
	})
	return stamp, ok
}

// namesDriver reports whether name, with the empty name reading as the default
// Driver, resolves to d.
func namesDriver(name string, d driver.Driver) bool {
	sd, err := driver.New(name)
	return err == nil && sd.Name() == d.Name()
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
	// The stamp names the Driver whose grammar the rest of the log is in; an
	// unstamped log predates Driver choice and reads as claude.
	pre, _ := ReadStamp(path)
	d, err := driver.New(pre.Driver)
	if err != nil {
		return Record{}, false, time.Time{}, fmt.Errorf("dispatch_start: %w", err)
	}

	var (
		shells    = []*passShell{{ordinal: 1}}
		firstRole string
		sawStart  bool
		firstTS   time.Time
		haveTS    bool
		sawLine   bool
		sawEvent  bool
		stamp     *hostop.DispatchStart
		// hashes holds reported prompt hashes by the record_id they claim, since
		// the op need not follow the stamp it must match.
		hashes  = map[string]map[string]string{}
		settled *hostop.DispatchSettled
		// status is the last plain-text SPINDRIFT_OUTCOME line's status.
		status string
		// announced is the kind named by the Box's first plain-text announce
		// line ("==> claude <verb> ..."); empty when the log has none.
		announced string
	)

	tallies, err := d.ReadPasses(path, passmachine.DispositionsPath, func(l driverkit.PassLine) {
		sawLine = true
		// The agent's outcome line and the Box's announce line are plain text,
		// not events, so they must be read before the event check below.
		if r, ok := outcome.SelfReportFromLogLine(l.Text); ok {
			status = r.Status
			return
		}
		if k := announcedKind(l.Text); k != "" {
			if announced == "" {
				announced = k
			}
			return
		}
		if !l.Event {
			return
		}
		first := !sawEvent
		sawEvent = true
		op := l.HostOp
		// The host appends only dispatch_settled ops after the Box exits, so
		// anything else after a settled op means the Box printed it.
		if op == nil || op.Op != hostop.OpDispatchSettled {
			settled = nil
		}
		if l.Timestamp != 0 && !haveTS {
			firstTS, haveTS = time.UnixMilli(l.Timestamp), true
		}
		if op == nil {
			return
		}
		cur := shells[len(shells)-1]
		switch op.Op {
		case hostop.OpDispatchStart:
			if first && isStamp(op) {
				stamp = op.Start
			}
		case hostop.OpPromptHashes:
			if h := op.PromptHashes; h != nil {
				if hashes[h.RecordID] == nil {
					hashes[h.RecordID] = map[string]string{}
				}
				maps.Copy(hashes[h.RecordID], h.Roles)
			}
		case hostop.OpDispatchSettled:
			// An op naming another Record is not this log's to claim, and
			// must not displace one that is. The Box shares this log and
			// knows the Record ID, so a tokened stamp also demands the host
			// token, which the Box never learns: it cannot read its pass log
			// (runner mounts never bind .spindrift/logs). A legacy token-less
			// stamp falls back to the ordering rule above alone.
			if op.Settled != nil && stamp != nil && op.Settled.RecordID == stamp.RecordID &&
				(stamp.HostToken == "" || op.Settled.HostToken == stamp.HostToken) {
				settled = op.Settled
			}
		case hostop.OpPassStart:
			if !sawStart {
				firstRole = op.Role
			}
			sawStart = true
			shells = append(shells, &passShell{ordinal: op.Pass, role: op.Role, explicit: true})
		case hostop.OpVerdict:
			cur.verdict = op.Verdict
		case hostop.OpPassUsage:
			if op.Usage != nil {
				cur.opCalls = op.Usage.APICalls
			}
		}
	})
	if err != nil {
		return Record{}, false, time.Time{}, err
	}
	if !sawLine {
		return Record{}, false, time.Time{}, ErrEmptyLog
	}
	// ReadStamp picked the Driver before the loop ran; a stamp only the loop
	// accepted is fine when it names that same Driver, else the log was read in
	// the wrong grammar.
	if stamp != nil && !namesDriver(stamp.Driver, d) {
		return Record{}, false, time.Time{}, errors.New("dispatch_start: stamp not first in log, Driver unknown")
	}
	if len(tallies) != len(shells) {
		return Record{}, false, time.Time{}, fmt.Errorf("pass log split into %d tallies for %d pass segments", len(tallies), len(shells))
	}
	passes, boxStatus := mergePasses(shells, tallies, base)

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
			rec.MergeCommit = settled.MergeCommit
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

// ErrUnreadableLog matches the error Ingest returns for logs it could not read
// (permissions, I/O) and skipped; the rest of the root was still ingested, so
// it is a warning, unlike a store failure.
var ErrUnreadableLog = errors.New("dispatchrecord: unreadable log")
