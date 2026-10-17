package opencode

import "spindrift.dev/launcher/internal/driver/driverkit"

// RenderTranscript returns the box log's text, tool-call and tool-result events
// as "[role]"-prefixed lines (see driverkit.RenderTranscript); topLevelRole
// defaults to driverkit.ImplementorRole. Returns ("", nil) when logPath does not
// exist, as the claude Driver does.
func RenderTranscript(logPath, topLevelRole string) (string, error) {
	return driverkit.RenderTranscript(logPath, NewDecoder, topLevelRole)
}
