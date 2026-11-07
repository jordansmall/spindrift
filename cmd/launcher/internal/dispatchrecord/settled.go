package dispatchrecord

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/hostpaths"
)

// AppendSettled appends ds as a dispatch_settled op to the existing log at
// logPath and reports whether it did. It is the only writer of that op: the
// shared append step of RecordSettled (append, then ingest) and of late-merge,
// which calls it directly to keep its batched ingest. It is best-effort: a
// failure writes a warning to warn (prefixed with label) and never errors.
//
// The log is opened without O_CREATE: a Dispatch that wrote no primary log has
// no Record to settle, and a stub file would only fake one. With no logPath
// nothing is appended and nothing warned; with no Record ID nothing is appended
// either, and the log's stamp is deliberately not consulted — it may name an
// earlier Dispatch's Record that this one must not rewrite.
func AppendSettled(warn io.Writer, label, logPath string, ds hostop.DispatchSettled) bool {
	switch {
	case logPath == "":
		return false
	case ds.RecordID == "":
		fmt.Fprintf(warn, "    ?? %s: no record id, not appending %s to %s\n", label, hostop.OpDispatchSettled, logPath)
		return false
	}
	if err := appendSettledOp(logPath, ds); err != nil {
		fmt.Fprintf(warn, "    ?? %s: could not append %s to %s: %v\n", label, hostop.OpDispatchSettled, logPath, err)
		return false
	}
	return true
}

// RecordSettled appends ds (see AppendSettled) and, only on success, ingests
// the log's chain so the Dispatch's Record is in the store with no `stats` run.
// The store's root is derived from logPath: a log outside a root's
// .spindrift/logs has no store, so nothing is ingested.
func RecordSettled(warn io.Writer, label, logPath string, ds hostop.DispatchSettled) {
	if !AppendSettled(warn, label, logPath, ds) {
		return
	}
	root, ok := hostpaths.RootOfLogDir(filepath.Dir(logPath))
	if !ok {
		return
	}
	if err := ingestChainAt(root, filepath.Base(logPath)); err != nil {
		fmt.Fprintf(warn, "    ?? %s: could not ingest into %s: %v\n", label, hostpaths.DispatchRecordsDB(root), err)
	}
}

func ingestChainAt(root, base string) (err error) {
	store, err := Open(root)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := store.Close(); err == nil {
			err = closeErr
		}
	}()
	_, err = store.IngestChain(base)
	return err
}

func appendSettledOp(logPath string, ds hostop.DispatchSettled) error {
	if s, ok := ReadStamp(logPath); ok && s.RecordID == ds.RecordID {
		ds.HostToken = s.HostToken
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchSettled, Settled: &ds}))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
