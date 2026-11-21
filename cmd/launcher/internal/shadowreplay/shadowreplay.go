// Package shadowreplay replays an archived host Pass log through the shadow
// comparator, so a Connector port can be checked against Passes that already
// ran. The archive is the host's copy of the Box's merged stdout and stderr.
// Replay cuts each ended Pass's lines back out, keeps the JSON-object ones
// (and blanks), hands them to shadow.Run, and writes a sidecar; the archive
// is only read. Residual gap versus live: replay drops a non-JSON stdout line
// live compared, and compares a JSON-object stderr line live never saw.
package shadowreplay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/shadow"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/logscan"
)

// SidecarSuffix ends the name of a replay's output file. It deliberately does
// not end in ".log", so the Dispatch Record scan, which reads issue-*.log, never
// takes a replay for a Pass log. A sidecar opens with its log's dispatch_start;
// one that does not match the log beside it is a leftover and is ignored.
const SidecarSuffix = ".shadow-replay.jsonl"

// Result is what Replay did with one archive.
type Result struct {
	// Sidecar is the file written, empty when the archive was skipped.
	Sidecar string
	// Passes counts the ended Passes replayed, each closed by one shadow_summary.
	Passes int
	// Skipped says why nothing was written, empty when a sidecar was.
	Skipped string
}

// SidecarPath returns where Replay writes the results for the archive.
func SidecarPath(archive string) string {
	return strings.TrimSuffix(archive, ".log") + SidecarSuffix
}

// Replay replays every ended Pass of the archive through native and the
// Connector load builds from wasm, and writes their shadow ops in Pass order
// to SidecarPath(archive), replacing an earlier replay; the sidecar's first
// line is the archive's dispatch_start, verbatim. driver names the Driver
// native is: an archive whose dispatch_start stamps another Driver, or none,
// is skipped, since its lines are in another grammar or its Driver is
// unproven. A Pass with no closing pass_usage was cut short and is skipped.
// The archive lacks the CLI's raw exit code, so each Pass resolves its exit
// against 0 and every shadow_summary is marked Replayed.
func Replay(ctx context.Context, driver string, native driverkit.Strategy, load shadow.Loader, wasm []byte, archive string) (Result, error) {
	var (
		ops       []hostop.SpindriftOp
		res       Result
		stamped   string
		stampLine string
		cur       *passFile
		failure   error
	)
	defer func() { cur.discard() }()

	err := logscan.ForEachLineUntil(archive, logscan.SkipOversized, func(line string) bool {
		op := hostOp(line)
		switch {
		case op != nil && op.Op == hostop.OpDispatchStart && stampLine == "":
			stampLine = line
			if op.Start != nil {
				stamped = op.Start.Driver
			}
			if stamped != driver {
				return false
			}
		case stampLine == "" && op != nil && op.Op == hostop.OpPassStart:
			return false
		case op != nil && op.Op == hostop.OpPassStart:
			cur.discard()
			if cur, failure = newPassFile(); failure != nil {
				return false
			}
		case op != nil && op.Op == hostop.OpPassUsage:
			if cur == nil {
				return true
			}
			var passOps []hostop.SpindriftOp
			passOps, failure = cur.replay(ctx, native, load, wasm)
			cur = nil
			ops = append(ops, passOps...)
			res.Passes++
			return failure == nil
		case op != nil:
			// Every other host op: not a Driver line.
		case cur != nil:
			// Live shadow read stdout only, and both replayable Drivers print one
			// JSON object per stdout line, so a non-JSON line is stderr and is
			// left out. Blanks stay: stdout can carry them.
			if t := strings.TrimSpace(line); t != "" && !(t[0] == '{' && json.Valid([]byte(t))) {
				return true
			}
			failure = cur.add(line)
			return failure == nil
		}
		return true
	})
	if err == nil {
		err = failure
	}
	if err != nil {
		return Result{}, fmt.Errorf("replay %s: %w", archive, err)
	}
	switch {
	case stampLine == "":
		res.Skipped = "no dispatch_start stamp"
		return res, nil
	case stamped != driver:
		res.Skipped = fmt.Sprintf("dispatch_start names Driver %q, not %q", stamped, driver)
		return res, nil
	case res.Passes == 0:
		res.Skipped = "no ended Pass"
		return res, nil
	}
	res.Sidecar = SidecarPath(archive)
	return res, writeSidecar(res.Sidecar, stampLine, ops)
}

// hostOp returns the payload of a spindrift_op line, nil for any other line.
// Every op line carries the type name, which a plain scan finds before the
// cost of decoding a multi-megabyte Driver line.
func hostOp(line string) *hostop.SpindriftOp {
	if !strings.Contains(line, hostop.TypeSpindriftOp) {
		return nil
	}
	var ev hostop.Event
	if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != hostop.TypeSpindriftOp {
		return nil
	}
	if ev.SpindriftOp == nil {
		return &hostop.SpindriftOp{}
	}
	return ev.SpindriftOp
}

// passFile is one Pass's Driver lines, written out as the per-Pass log
// driver-exec's live shadow read.
type passFile struct {
	f *os.File
	w *bufio.Writer
}

func newPassFile() (*passFile, error) {
	f, err := os.CreateTemp("", "shadow-replay-*.log")
	if err != nil {
		return nil, err
	}
	return &passFile{f: f, w: bufio.NewWriter(f)}, nil
}

func (p *passFile) add(line string) error {
	if _, err := p.w.WriteString(line); err != nil {
		return err
	}
	return p.w.WriteByte('\n')
}

// discard closes and removes the file; a nil passFile is a no-op.
func (p *passFile) discard() {
	if p != nil {
		_ = p.f.Close()
		_ = os.Remove(p.f.Name())
	}
}

// replay runs the comparator over the file, then removes it.
func (p *passFile) replay(ctx context.Context, native driverkit.Strategy, load shadow.Loader, wasm []byte) ([]hostop.SpindriftOp, error) {
	defer p.discard()
	if err := p.w.Flush(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, shadow.Budget)
	defer cancel()
	var ops []hostop.SpindriftOp
	shadow.Run(ctx, native, load, wasm, p.f.Name(), 0, func(op hostop.SpindriftOp) {
		if s := op.ShadowSummary; s != nil {
			marked := *s
			marked.Replayed = true
			op.ShadowSummary = &marked
		}
		ops = append(ops, op)
	})
	return ops, nil
}

// writeSidecar replaces path with the stamp line and then ops, one line each,
// through a temp file in the same directory so a reader never sees a
// half-written replay.
func writeSidecar(path, stampLine string, ops []hostop.SpindriftOp) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".shadow-replay-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	w := bufio.NewWriter(tmp)
	if _, err = w.WriteString(stampLine + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	for _, op := range ops {
		if _, err = w.WriteString(hostop.EncodeSpindriftOp(op)); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err = w.Flush(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
