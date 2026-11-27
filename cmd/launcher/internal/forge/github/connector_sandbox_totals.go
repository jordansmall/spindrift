package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/hostpaths"
)

// RecordRoutedSandboxTotals appends the run's Connector sandbox cost to this
// process's Connector audit file, the only home a routed run has for it: a
// routed row writes no shadow summary. It does nothing for a runtime without
// the github row. Call it once, after the sandbox
// closed, so the totals are final. It writes nothing unless the audit file
// already exists, since a pair for a process that never read through its
// routed Connector would count as a dirty run, and never touches the .log.
func RecordRoutedSandboxTotals(rt *connectorruntime.Runtime, st connectorsandbox.Stats) {
	if _, ok := routedRow(rt); !ok {
		return
	}
	pwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox totals were not recorded: %v\n", err)
		return
	}
	path := filepath.Join(hostpaths.LogDir(pwd), connectorrecord.Name(backend.GitHub.Name, ".audit.jsonl"))
	// Every field is an int64, so Marshal cannot fail.
	line, _ := json.Marshal(struct {
		StartNs   int64 `json:"sandbox_start_ns"`
		Calls     int64 `json:"sandbox_calls"`
		HostCalls int64 `json:"sandbox_host_calls"`
		HopNs     int64 `json:"sandbox_hop_ns"`
	}{int64(st.Start), st.Calls, st.HostCalls, int64(st.Hop)})
	// No O_CREATE: the file already existing is the precondition.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err == nil {
		_, err = f.Write(append(line, '\n'))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox totals were not recorded: %v\n", err)
	}
}
