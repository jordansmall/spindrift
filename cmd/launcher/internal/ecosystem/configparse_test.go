package ecosystem

import "testing"

// TestConfigParserMatchesInTreeConfigPath guards against a future Table row
// acquiring a non-empty InTreeConfigPath with no ConfigParser (registrydiscover's
// walker silently skips such a row, so a new in-tree ecosystem could go
// undiscovered with no test failure anywhere) or the reverse -- a
// ConfigParser with nothing for it to read. Checks both directions.
func TestConfigParserMatchesInTreeConfigPath(t *testing.T) {
	for _, row := range Table {
		hasPath := row.InTreeConfigPath != ""
		hasParser := row.ConfigParser != nil
		if hasPath && !hasParser {
			t.Errorf("row %q has InTreeConfigPath %q but a nil ConfigParser", row.Name, row.InTreeConfigPath)
		}
		if hasParser && !hasPath {
			t.Errorf("row %q has a ConfigParser but an empty InTreeConfigPath", row.Name)
		}
	}
}
