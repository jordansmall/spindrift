package main

import (
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestAutoMergePreflight verifies that checkAutoMergePreflight aborts dispatch
// when MERGE_MODE=auto and the repo disallows auto-merge, and is a no-op for
// other modes.
func TestAutoMergePreflight(t *testing.T) {
	cases := []struct {
		name             string
		mergeMode        string
		codeForge        string
		mergeMethod      string
		autoMergeAllowed bool
		autoMergeErr     error
		wantErr          bool
		wantErrContains  string
		wantErrOmits     string
	}{
		{
			name:             "auto mode and repo allows auto-merge — ok",
			mergeMode:        "auto",
			autoMergeAllowed: true,
			wantErr:          false,
		},
		{
			name:             "auto mode and repo disallows auto-merge — abort",
			mergeMode:        "auto",
			autoMergeAllowed: false,
			wantErr:          true,
			wantErrContains:  `enable "Allow auto-merge" in repo Settings`,
			wantErrOmits:     "merge style",
		},
		{
			name:             "auto mode on forgejo and repo disallows auto-merge — merge-style remedy",
			mergeMode:        "auto",
			codeForge:        "forgejo",
			autoMergeAllowed: false,
			wantErr:          true,
			wantErrContains:  `enable the "rebase" merge style (MERGE_METHOD) in the repo's settings, or change MERGE_METHOD`,
			wantErrOmits:     "Allow auto-merge",
		},
		{
			name:             "auto mode on forgejo with MERGE_METHOD=merge — remedy names merge",
			mergeMode:        "auto",
			codeForge:        "forgejo",
			mergeMethod:      "merge",
			autoMergeAllowed: false,
			wantErr:          true,
			wantErrContains:  `enable the "merge" merge style (MERGE_METHOD)`,
			wantErrOmits:     "Allow auto-merge",
		},
		{
			name:             "auto mode on forgejo with MERGE_METHOD=squash — remedy names squash",
			mergeMode:        "auto",
			codeForge:        "forgejo",
			mergeMethod:      "squash",
			autoMergeAllowed: false,
			wantErr:          true,
			wantErrContains:  `enable the "squash" merge style (MERGE_METHOD)`,
			wantErrOmits:     "Allow auto-merge",
		},
		{
			name:            "auto mode and CanAutoMerge API error — abort",
			mergeMode:       "auto",
			autoMergeErr:    fmt.Errorf("gh api graphql: 403 Forbidden"),
			wantErr:         true,
			wantErrContains: "403",
		},
		{
			name:             "immediate mode — no preflight check",
			mergeMode:        "immediate",
			autoMergeAllowed: false, // would fail if checked
			wantErr:          false,
		},
		{
			name:             "manual mode — no preflight check",
			mergeMode:        "manual",
			autoMergeAllowed: false,
			wantErr:          false,
		},
		{
			name:            "auto mode with CODE_FORGE=git — abort before any CanAutoMerge call",
			mergeMode:       "auto",
			codeForge:       "git",
			wantErr:         true,
			wantErrContains: "CODE_FORGE=github or forgejo",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.mergeMode = tc.mergeMode
			c.mergeMethod = tc.mergeMethod
			if tc.codeForge != "" {
				c.codeForge = tc.codeForge
			}
			fc := forge.NewFake()
			fc.AutoMergeAllowed = tc.autoMergeAllowed
			fc.AutoMergeErr = tc.autoMergeErr
			var cf forge.CodeForge = fc
			if tc.codeForge == "git" {
				cf = fc.AsPushOnly()
			}

			err := checkAutoMergePreflight(c, capsFor(fc, cf))

			if (err != nil) != tc.wantErr {
				t.Errorf("checkAutoMergePreflight err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErrContains != "" && err != nil && !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrContains)
			}
			if tc.wantErrOmits != "" && err != nil && strings.Contains(err.Error(), tc.wantErrOmits) {
				t.Errorf("error %q must not contain %q", err.Error(), tc.wantErrOmits)
			}
		})
	}
}
