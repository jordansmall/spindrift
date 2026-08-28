package main

import (
	"strings"
	"testing"
)

// TestValidateGHAppConfig_AllUnset_NoError verifies the pre-issue-#2867
// default (none of the three GitHub App knobs set, no GH_TOKEN_REFRESH_FILE
// either) is a no-op: local dispatch auth is opt-in.
func TestValidateGHAppConfig_AllUnset_NoError(t *testing.T) {
	if err := validateGHAppConfig("", "", "", ""); err != nil {
		t.Errorf("validateGHAppConfig(all unset) = %v, want nil", err)
	}
}

// TestValidateGHAppConfig_AllSet_NoError verifies a fully configured trio,
// with no GH_TOKEN_REFRESH_FILE set, passes.
func TestValidateGHAppConfig_AllSet_NoError(t *testing.T) {
	if err := validateGHAppConfig("123", "/key.pem", "456", ""); err != nil {
		t.Errorf("validateGHAppConfig(all set) = %v, want nil", err)
	}
}

// TestValidateGHAppConfig_OneSet_ErrorsNamingMissing verifies exactly one of
// the three set is a misconfiguration, and the error names the two missing
// knobs.
func TestValidateGHAppConfig_OneSet_ErrorsNamingMissing(t *testing.T) {
	err := validateGHAppConfig("123", "", "", "")
	if err == nil {
		t.Fatal("validateGHAppConfig(only GH_APP_ID set) = nil, want an error")
	}
	for _, want := range []string{"GH_APP_PRIVATE_KEY_FILE", "GH_APP_INSTALLATION_ID"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validateGHAppConfig error = %q, want it to mention %q", err.Error(), want)
		}
	}
}

// TestValidateGHAppConfig_TwoSet_ErrorsNamingMissing verifies exactly two of
// the three set is also a misconfiguration, naming the one still missing.
func TestValidateGHAppConfig_TwoSet_ErrorsNamingMissing(t *testing.T) {
	err := validateGHAppConfig("123", "/key.pem", "", "")
	if err == nil {
		t.Fatal("validateGHAppConfig(GH_APP_ID+GH_APP_PRIVATE_KEY_FILE set) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "GH_APP_INSTALLATION_ID") {
		t.Errorf("validateGHAppConfig error = %q, want it to mention GH_APP_INSTALLATION_ID", err.Error())
	}
}

// TestValidateGHAppConfig_AllSetWithTokenRefreshFile_Errors verifies a fully
// configured App trio combined with an explicit GH_TOKEN_REFRESH_FILE is
// rejected: once the trio is complete, bootstrap's own minting loop owns and
// rewrites its own token file, so an operator-supplied
// GH_TOKEN_REFRESH_FILE at the same time is ambiguous about which mechanism
// owns the file.
func TestValidateGHAppConfig_AllSetWithTokenRefreshFile_Errors(t *testing.T) {
	err := validateGHAppConfig("123", "/key.pem", "456", "/some/refresh/file")
	if err == nil {
		t.Fatal("validateGHAppConfig(all set + GH_TOKEN_REFRESH_FILE) = nil, want a mutual-exclusion error")
	}
	if !strings.Contains(err.Error(), "GH_TOKEN_REFRESH_FILE") {
		t.Errorf("validateGHAppConfig error = %q, want it to mention GH_TOKEN_REFRESH_FILE", err.Error())
	}
}

// TestValidateGHAppConfig_PartialWithTokenRefreshFile_NamesMissingNotRefreshFile
// verifies a partial trio combined with GH_TOKEN_REFRESH_FILE surfaces the
// partial-config error (naming the missing knobs), not the mutual-exclusion
// one -- the two error conditions are distinct and a partial trio is the
// more actionable diagnosis regardless of GH_TOKEN_REFRESH_FILE.
func TestValidateGHAppConfig_PartialWithTokenRefreshFile_NamesMissingNotRefreshFile(t *testing.T) {
	err := validateGHAppConfig("123", "", "", "/some/refresh/file")
	if err == nil {
		t.Fatal("validateGHAppConfig(partial + GH_TOKEN_REFRESH_FILE) = nil, want a partial-config error")
	}
	if !strings.Contains(err.Error(), "GH_APP_PRIVATE_KEY_FILE") {
		t.Errorf("validateGHAppConfig error = %q, want it to mention the missing GH_APP_PRIVATE_KEY_FILE", err.Error())
	}
}
