// Package connectorrecord names and writes the per-process record files a
// Connector-routed backend row leaves in the host's log directory: a
// connector-<backend>-<stamp>-<pid>.audit.jsonl of Connector audit lines and a
// .log of the reads and calls that failed (ADR 0073).
package connectorrecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
)

// stamp is taken once per process, so every backend's record files share it
// and a human can count clean runs: an empty .log and no refused entry
// in the .audit.jsonl is a clean one.
var stamp = sync.OnceValue(func() string {
	return time.Now().UTC().Format("20060102T150405Z") + "-" + strconv.Itoa(os.Getpid())
})

// Name is this process's record file for backend with suffix (".audit.jsonl"
// or ".log").
func Name(backend, suffix string) string {
	return "connector-" + backend + "-" + stamp() + suffix
}

// OpenFile opens the named file in the host's log directory for appending. An
// unopenable file returns a nil writer and an error naming the file, never a
// discarding writer: a caller that can live without the record ignores the
// error, and one that must not run unrecorded fails on it.
func OpenFile(name string) (io.Writer, func(), error) {
	path, err := logPath(name)
	if err != nil {
		return nil, func() {}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, func() {}, fmt.Errorf("open %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, func() {}, err
	}
	return f, func() { _ = f.Close() }, nil
}

// logPath resolves name under the host log directory of the working directory.
func logPath(name string) (string, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("open %s: %w", name, err)
	}
	return filepath.Join(hostpaths.LogDir(pwd), name), nil
}

var audits = struct {
	mu    sync.Mutex
	files map[string]io.Writer
}{files: map[string]io.Writer{}}

// AuditFile is this process's Connector audit file for backend, opened on
// first use and held until exit: a Connector may be reloaded and the trackers
// and forges of one process share it, so no caller owns the handle. It is keyed
// by path because a test changes directory. A failed open is not remembered.
func AuditFile(backend string) (io.Writer, error) {
	name := Name(backend, ".audit.jsonl")
	path, err := logPath(name)
	if err != nil {
		return nil, fmt.Errorf("Connector audit file: %w", err)
	}
	audits.mu.Lock()
	defer audits.mu.Unlock()
	if w, ok := audits.files[path]; ok {
		return w, nil
	}
	w, _, err := OpenFile(name)
	if err != nil {
		return nil, fmt.Errorf("Connector audit file: %w", err)
	}
	audits.files[path] = w
	return w, nil
}

// Note appends the error a Connector-routed operation returned to backend's
// .log and returns err unchanged. A nil err records nothing. A record that
// cannot be written goes to stderr instead, so the failure is not lost. A lost
// claim race records nothing: forge.ErrAlreadyClaimed is core policy, not a
// Connector fault, and an empty .log is what marks a clean run.
func Note(backend, op string, err error) error {
	if err == nil || errors.Is(err, forge.ErrAlreadyClaimed) {
		return err
	}
	line, _ := json.Marshal(struct {
		Op    string `json:"op"`
		Error string `json:"error"`
	}{op, err.Error()})
	w, closeLog, oerr := OpenFile(Name(backend, ".log"))
	defer closeLog()
	if oerr == nil {
		_, oerr = w.Write(append(line, '\n'))
	}
	if oerr != nil {
		fmt.Fprintf(os.Stderr, "Connector %s %q failed (%v) and its record was not written: %v\n", backend, op, err, oerr)
	}
	return err
}
