// Package hostpaths holds the host-side `<pwd>/.spindrift` layout. It is a
// leaf so the daemon can compute outbox and log paths without importing
// internal/dispatch's heavy graph; dispatch re-exports these.
package hostpaths

import "path/filepath"

// OutboxRoot is the directory holding every Dispatch key's outbox.
func OutboxRoot(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "outbox")
}

// OutboxDir is key's writable outbox directory.
func OutboxDir(pwd, key string) string {
	return filepath.Join(OutboxRoot(pwd), key)
}

// LogDir is the host-side log directory.
func LogDir(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "logs")
}

// RootOfLogDir is the inverse of LogDir: the pwd whose LogDir is dir, with ok
// false when dir is no root's log directory.
func RootOfLogDir(dir string) (root string, ok bool) {
	dir = filepath.Clean(dir)
	root = filepath.Dir(filepath.Dir(dir))
	return root, LogDir(root) == dir
}

// DispatchRecordsDB is the host-side SQLite store of Dispatch Records.
func DispatchRecordsDB(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "dispatch-records.db")
}
