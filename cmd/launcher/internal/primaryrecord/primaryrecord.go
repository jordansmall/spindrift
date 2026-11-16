// Package primaryrecord names and writes the per-process record files a
// primary backend row leaves in the host's log directory: a
// primary-<backend>-<stamp>-<pid>.audit.jsonl of Connector audit lines and a
// .log of the reads and calls that failed (ADR 0073).
package primaryrecord

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/hostpaths"
)

// stamp is taken once per process, so every backend's record files share it
// and a human can count clean primary runs: an empty .log and no refused entry
// in the .audit.jsonl is a clean one.
var stamp = sync.OnceValue(func() string {
	return time.Now().UTC().Format("20060102T150405Z") + "-" + strconv.Itoa(os.Getpid())
})

// Name is this process's record file for backend with suffix (".audit.jsonl"
// or ".log").
func Name(backend, suffix string) string {
	return "primary-" + backend + "-" + stamp() + suffix
}

// OpenFile opens the named file in the host's log directory for appending. An
// unopenable file returns io.Discard and the reason, so a caller whose record
// must not fail the work can ignore it and one that must report a lost record
// can use it.
func OpenFile(name string) (io.Writer, func(), error) {
	pwd, err := os.Getwd()
	if err != nil {
		return io.Discard, func() {}, err
	}
	dir := hostpaths.LogDir(pwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return io.Discard, func() {}, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return io.Discard, func() {}, err
	}
	return f, func() { _ = f.Close() }, nil
}

// Note appends the error a primary-routed operation returned to backend's
// .log and returns err unchanged. A nil err records nothing. A record that
// cannot be written goes to stderr instead, so the failure is not lost.
func Note(backend, op string, err error) error {
	if err == nil {
		return nil
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
		fmt.Fprintf(os.Stderr, "primary %s %q failed (%v) and its record was not written: %v\n", backend, op, err, oerr)
	}
	return err
}
