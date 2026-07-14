package console

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

var errBoom = errors.New("boom")

// TestNewModel_Empty verifies a freshly constructed Model starts with no
// issues, no filter, and is not quitting — the zero state before the first
// IssuesLoadedMsg or dogfood-notice check arrives.
func TestNewModel_Empty(t *testing.T) {
	m := NewModel()
	if len(m.Visible()) != 0 {
		t.Errorf("Visible() = %v, want none", m.Visible())
	}
	if m.Quitting {
		t.Error("Quitting = true, want false")
	}
}

// TestUpdate_IssuesLoadedMsg_ReplacesAll verifies Update installs the
// refreshed backlog verbatim, in the order the adapter supplied it (oldest
// first per dispatch order is the adapter's responsibility, not Update's).
func TestUpdate_IssuesLoadedMsg_ReplacesAll(t *testing.T) {
	m := NewModel()
	issues := []forge.Issue{{Number: "1", Title: "first"}, {Number: "2", Title: "second"}}

	m = Update(m, IssuesLoadedMsg{Issues: issues})

	if len(m.Visible()) != 2 || m.Visible()[0].Number != "1" || m.Visible()[1].Number != "2" {
		t.Errorf("Visible() = %+v, want %+v", m.Visible(), issues)
	}
}

// TestUpdate_IssuesLoadedMsg_ErrKeepsStaleListAndRecordsErr verifies a
// failed refresh (Err set) leaves the last-good backlog on screen instead of
// blanking it, and records Err for View to surface — a failed refresh must
// never look like an empty backlog.
func TestUpdate_IssuesLoadedMsg_ErrKeepsStaleListAndRecordsErr(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{{Number: "1"}}})

	wantErr := errBoom
	m = Update(m, IssuesLoadedMsg{Err: wantErr})

	if len(m.Visible()) != 1 || m.Visible()[0].Number != "1" {
		t.Errorf("Visible() = %+v, want stale [1] kept on error", m.Visible())
	}
	if m.Err != wantErr {
		t.Errorf("Err = %v, want %v", m.Err, wantErr)
	}
}

// TestUpdate_FilterChangedMsg_NarrowsAndClearingRestores verifies a label
// filter narrows Visible() to issues carrying a matching label, and setting
// the filter back to "" restores the full backlog — the two acceptance
// criteria ("narrows the list interactively" / "clearing it restores the
// full list") in one round trip.
func TestUpdate_FilterChangedMsg_NarrowsAndClearingRestores(t *testing.T) {
	m := NewModel()
	m = Update(m, IssuesLoadedMsg{Issues: []forge.Issue{
		{Number: "1", Labels: []string{"ready-for-agent"}},
		{Number: "2", Labels: []string{"agent-in-progress"}},
	}})

	m = Update(m, FilterChangedMsg{Filter: "in-progress"})
	if got := m.Visible(); len(got) != 1 || got[0].Number != "2" {
		t.Errorf("Visible() after filter = %+v, want just #2", got)
	}

	m = Update(m, FilterChangedMsg{Filter: ""})
	if got := m.Visible(); len(got) != 2 {
		t.Errorf("Visible() after clearing filter = %+v, want both issues", got)
	}
}

// TestUpdate_QuitMsg_SetsQuitting verifies QuitMsg is the sole way Quitting
// flips true — the run loop's signal to exit its read loop cleanly.
func TestUpdate_QuitMsg_SetsQuitting(t *testing.T) {
	m := NewModel()
	m = Update(m, QuitMsg{})
	if !m.Quitting {
		t.Error("Quitting = false after QuitMsg, want true")
	}
}

// TestUpdate_DogfoodNoticeMsg_SetsLive verifies DogfoodNoticeMsg{Live: true}
// records that a competing headless loop's pid-file is present, and
// {Live: false} clears it — the startup notice is informational only and
// must never block, so it is just a bit on Model that View can render.
func TestUpdate_DogfoodNoticeMsg_SetsLive(t *testing.T) {
	m := NewModel()
	if m.DogfoodLive {
		t.Error("DogfoodLive = true before any message, want false")
	}

	m = Update(m, DogfoodNoticeMsg{Live: true})
	if !m.DogfoodLive {
		t.Error("DogfoodLive = false after Live:true, want true")
	}

	m = Update(m, DogfoodNoticeMsg{Live: false})
	if m.DogfoodLive {
		t.Error("DogfoodLive = true after Live:false, want false")
	}
}
