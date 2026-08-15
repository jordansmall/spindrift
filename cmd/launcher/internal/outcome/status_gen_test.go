package outcome_test

import (
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// TestStatusGenConstants pins the regen-generated status constants and
// ordered per-kind status sets (cmd/launcher/internal/outcome/status_gen.go,
// issue #2504) against lib/prompt-contract.nix's outcomeStatusSets registry.
func TestStatusGenConstants(t *testing.T) {
	if want, got := "ready", outcome.StatusReady; got != want {
		t.Errorf("StatusReady = %q, want %q", got, want)
	}
	if want, got := "blocked", outcome.StatusBlocked; got != want {
		t.Errorf("StatusBlocked = %q, want %q", got, want)
	}
	if want, got := "ambiguous", outcome.StatusAmbiguous; got != want {
		t.Errorf("StatusAmbiguous = %q, want %q", got, want)
	}
	if want, got := "recommend", outcome.StatusRecommend; got != want {
		t.Errorf("StatusRecommend = %q, want %q", got, want)
	}
	if want, got := "reject", outcome.StatusReject; got != want {
		t.Errorf("StatusReject = %q, want %q", got, want)
	}
	if want, got := "unclear", outcome.StatusUnclear; got != want {
		t.Errorf("StatusUnclear = %q, want %q", got, want)
	}

	wantWork := []string{"ready", "blocked", "ambiguous"}
	if !reflect.DeepEqual(outcome.WorkStatuses, wantWork) {
		t.Errorf("WorkStatuses = %v, want %v", outcome.WorkStatuses, wantWork)
	}

	wantResearch := []string{"recommend", "reject", "unclear", "blocked"}
	if !reflect.DeepEqual(outcome.ResearchStatuses, wantResearch) {
		t.Errorf("ResearchStatuses = %v, want %v", outcome.ResearchStatuses, wantResearch)
	}
}
