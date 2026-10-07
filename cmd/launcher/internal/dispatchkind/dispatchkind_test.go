package dispatchkind

import "testing"

func TestByName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    *Descriptor
		wantErr bool
	}{
		{"work", "work", Work, false},
		{"research", "research", Research, false},
		{"butler", "butler", Butler, false},
		{"unknown", "bogus", nil, true},
		{"empty is not special", "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ByName(tc.in)
			if tc.wantErr {
				if ok {
					t.Fatalf("ByName(%q) = %v, true; want not found", tc.in, got)
				}
				return
			}
			if !ok {
				t.Fatalf("ByName(%q): not found", tc.in)
			}
			if got != tc.want {
				t.Fatalf("ByName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestByVerb(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    *Descriptor
		wantErr bool
	}{
		{"dispatch", "dispatch", Work, false},
		{"research", "research", Research, false},
		{"butler", "butler", Butler, false},
		{"unknown", "bogus", nil, true},
		{"empty is not special", "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ByVerb(tc.in)
			if tc.wantErr {
				if ok {
					t.Fatalf("ByVerb(%q) = %v, true; want not found", tc.in, got)
				}
				return
			}
			if !ok {
				t.Fatalf("ByVerb(%q): not found", tc.in)
			}
			if got != tc.want {
				t.Fatalf("ByVerb(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestKeyingString pins the DISPATCH_KEYING wire values (issue #3996).
func TestKeyingString(t *testing.T) {
	if got := ByIssue.String(); got != "issue" {
		t.Errorf("ByIssue.String() = %q, want %q", got, "issue")
	}
	if got := ByChore.String(); got != "chore" {
		t.Errorf("ByChore.String() = %q, want %q", got, "chore")
	}
}

// TestAllOrder pins the daemon's default pool order (issue #3541): work
// before research before butler before recover.
func TestAllOrder(t *testing.T) {
	if len(All) != 4 || All[0] != Work || All[1] != Research || All[2] != Butler || All[3] != Recover {
		t.Fatalf("All = %v, want [Work, Research, Butler, Recover]", All)
	}
}

// TestUniqueAcrossAll guards against a future kind reusing a name or verb
// already claimed by another descriptor, which would make ByName/ByVerb
// ambiguous.
func TestUniqueAcrossAll(t *testing.T) {
	names := make(map[string]bool, len(All))
	verbs := make(map[string]bool, len(All))
	for _, d := range All {
		if names[d.Name] {
			t.Fatalf("duplicate name %q in All", d.Name)
		}
		names[d.Name] = true
		if verbs[d.Verb] {
			t.Fatalf("duplicate verb %q in All", d.Verb)
		}
		verbs[d.Verb] = true
	}
}

// TestEveryAxisSetForEveryKind guards against a future kind adding a
// Descriptor without setting every axis (issue #3989): a zero value here
// (empty string, 0 for the iota-from-1 enums, or a zero DoctorPreflight)
// would misbehave downstream rather than fail loudly like ByName's
// unknown-family panic.
func TestEveryAxisSetForEveryKind(t *testing.T) {
	findingLabels := make(map[string]*Descriptor, len(All))
	filerRelayGates := make(map[string]*Descriptor, len(All))
	for _, d := range All {
		if d.FindingLabel == "" {
			t.Fatalf("%s: FindingLabel unset", d.Name)
		}
		if d.Contract == 0 {
			t.Fatalf("%s: Contract unset", d.Name)
		}
		if d.FilerRelayGate == "" {
			t.Fatalf("%s: FilerRelayGate unset", d.Name)
		}
		if d.Tracker == 0 {
			t.Fatalf("%s: Tracker unset", d.Name)
		}
		if d.DemandSource == 0 {
			t.Fatalf("%s: DemandSource unset", d.Name)
		}
		if d.AnnounceVerb == "" {
			t.Fatalf("%s: AnnounceVerb unset", d.Name)
		}
		if d.Enablement == 0 {
			t.Fatalf("%s: Enablement unset", d.Name)
		}
		if d.Preflight.when == 0 {
			t.Fatalf("%s: Preflight unset", d.Name)
		}
		// Only recover shares work's finding label and relay gate (it mirrors
		// work's Box-facing labels); any other duplicate is a mistake.
		if prev, ok := findingLabels[d.FindingLabel]; ok && !isRecoverWorkPair(prev, d) {
			t.Fatalf("FindingLabel %q shared by %s and %s", d.FindingLabel, prev.Name, d.Name)
		}
		findingLabels[d.FindingLabel] = d
		if prev, ok := filerRelayGates[d.FilerRelayGate]; ok && !isRecoverWorkPair(prev, d) {
			t.Fatalf("FilerRelayGate %q shared by %s and %s", d.FilerRelayGate, prev.Name, d.Name)
		}
		filerRelayGates[d.FilerRelayGate] = d
	}
}

// TestEnablementRows pins which kinds a bare daemon always draws and which
// wait on a Chore being enabled (issue #4574) or on a relayed outbox (issue
// #4656): the butler and recover are gated.
func TestEnablementRows(t *testing.T) {
	want := map[*Descriptor]Enablement{
		Work:     EnabledAlways,
		Research: EnabledAlways,
		Butler:   EnabledByChores,
		Recover:  EnabledByOutboxRelay,
	}
	for _, d := range All {
		if d.Enablement != want[d] {
			t.Errorf("%s: Enablement = %v, want %v", d.Name, d.Enablement, want[d])
		}
	}
}

// TestDoctorPreflightRows pins how the daemon's startup preflight selects
// each kind's doctor flag (issue #4590).
func TestDoctorPreflightRows(t *testing.T) {
	want := map[*Descriptor]DoctorPreflight{
		Work:     NoPreflight(),
		Research: PreflightWhenNamed("--research"),
		Butler:   PreflightWhenDrawn("--butler"),
		Recover:  NoPreflight(),
	}
	for _, d := range All {
		if d.Preflight != want[d] {
			t.Errorf("%s: Preflight = %+v, want %+v", d.Name, d.Preflight, want[d])
		}
	}
}

// TestPreflightConstructorsRejectEmptyFlag guards the invariant the struct
// exists for: a mode that passes a flag always has one.
func TestPreflightConstructorsRejectEmptyFlag(t *testing.T) {
	for name, build := range map[string]func(string) DoctorPreflight{
		"PreflightWhenDrawn": PreflightWhenDrawn,
		"PreflightWhenNamed": PreflightWhenNamed,
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s(\"\") did not panic", name)
				}
			}()
			build("")
		}()
	}
}

// TestDoctorPreflightFlag pins the flag each preflight mode passes under a
// bare and an explicit selector (issue #4627).
func TestDoctorPreflightFlag(t *testing.T) {
	cases := []struct {
		name     string
		p        DoctorPreflight
		explicit bool
		want     string
	}{
		{"none bare", NoPreflight(), false, ""},
		{"none explicit", NoPreflight(), true, ""},
		{"drawn bare", PreflightWhenDrawn("--x"), false, "--x"},
		{"drawn explicit", PreflightWhenDrawn("--x"), true, "--x"},
		{"named bare", PreflightWhenNamed("--x"), false, ""},
		{"named explicit", PreflightWhenNamed("--x"), true, "--x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Flag(tc.explicit); got != tc.want {
				t.Errorf("Flag(%v) = %q, want %q", tc.explicit, got, tc.want)
			}
		})
	}
}

// TestDoctorPreflightFlagPanicsOnUnset keeps an unset preflight loud: Flag
// panics rather than reading the zero value as no flag (issue #4627).
func TestDoctorPreflightFlagPanicsOnUnset(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Flag on a zero DoctorPreflight did not panic")
		}
	}()
	DoctorPreflight{}.Flag(true)
}

// TestPatchLabelIsButlerOnly guards PatchLabel's butler-only scope (issue
// #4074): unlike FindingLabel, it isn't an axis every kind sets, since only
// the butler ever lands a patch PR (ADR 0057).
func TestPatchLabelIsButlerOnly(t *testing.T) {
	if Butler.PatchLabel == "" {
		t.Fatal("Butler.PatchLabel unset")
	}
	if Butler.PatchLabel == Butler.FindingLabel {
		t.Fatalf("Butler.PatchLabel == FindingLabel (%q); must be distinct provenance labels", Butler.PatchLabel)
	}
	for _, d := range []*Descriptor{Work, Research} {
		if d.PatchLabel != "" {
			t.Errorf("%s: PatchLabel = %q, want unset (patch rung is butler-only)", d.Name, d.PatchLabel)
		}
	}
}

// TestUnclaimedGateIsButlerOnly guards UnclaimedGate's butler-only scope
// (issue #4076): only the butler's landed patch PR settles a PR on an issue
// the kind never claimed.
func TestUnclaimedGateIsButlerOnly(t *testing.T) {
	if !Butler.UnclaimedGate {
		t.Fatal("Butler.UnclaimedGate = false, want true")
	}
	for _, d := range []*Descriptor{Work, Research} {
		if d.UnclaimedGate {
			t.Errorf("%s: UnclaimedGate = true, want false (unclaimed gate is butler-only)", d.Name)
		}
	}
}

// TestDemandSource pins which kinds the host can probe a tracker for: only
// the issue-keyed kinds. The butler's work lives in the Ledger, so only its
// child can say whether any is left.
func TestDemandSource(t *testing.T) {
	for _, tc := range []struct {
		d    *Descriptor
		want DemandSource
	}{
		{Work, DemandTrackerProbe},
		{Research, DemandTrackerProbe},
		{Butler, DemandChildReported},
	} {
		if tc.d.DemandSource != tc.want {
			t.Errorf("%s: DemandSource = %v, want %v", tc.d.Name, tc.d.DemandSource, tc.want)
		}
	}
}

// TestRecoverRow pins the recover kind's place on the descriptor (issue
// #4656): it runs ahead of every other kind, learns of work only from its
// own exit code, and settles through the work merge gate on work's tracker.
func TestRecoverRow(t *testing.T) {
	d, ok := ByVerb("recover")
	if !ok || d != Recover || d.Name != "recover" {
		t.Fatalf(`ByVerb("recover") = %v, %v, want Recover`, d, ok)
	}
	if got, ok := ByName("recover"); !ok || got != Recover {
		t.Errorf(`ByName("recover") = %v, %v, want Recover`, got, ok)
	}
	if d.DaemonPriority != PriorityFirst {
		t.Errorf("DaemonPriority = %v, want PriorityFirst", d.DaemonPriority)
	}
	if d.DemandSource != DemandChildReported {
		t.Errorf("DemandSource = %v, want DemandChildReported", d.DemandSource)
	}
	if d.Settle != SettleMerge || d.Tracker != TrackerWork || d.Labels != LabelsConfigured || d.Keying != ByIssue {
		t.Errorf("recover must settle through work's merge gate on work's tracker and labels: %+v", d)
	}
	if d.AdviseOnly || d.ReadOnlyBox {
		t.Errorf("recover lands code through the work gate: AdviseOnly=%v ReadOnlyBox=%v", d.AdviseOnly, d.ReadOnlyBox)
	}
}

// TestPriorityFirstIsRecoverOnly keeps the first tier a deliberate exception:
// a second kind claiming it would silently outrank work.
func TestPriorityFirstIsRecoverOnly(t *testing.T) {
	for _, d := range All {
		if (d.DaemonPriority == PriorityFirst) != (d == Recover) {
			t.Errorf("%s: DaemonPriority = %v; only recover may be PriorityFirst", d.Name, d.DaemonPriority)
		}
	}
}

// TestRecoverMirrorsWorkBoxFacingFields pins that recover, which runs no Box
// of its own, takes the Box-facing fields of the work Box whose bundle it
// relays rather than inventing a fourth label or gate.
func TestRecoverMirrorsWorkBoxFacingFields(t *testing.T) {
	if Recover.FindingLabel != Work.FindingLabel || Recover.FilerRelayGate != Work.FilerRelayGate || Recover.Contract != Work.Contract {
		t.Errorf("Recover = %+v, want work's FindingLabel, FilerRelayGate and Contract", Recover)
	}
}

func isRecoverWorkPair(a, b *Descriptor) bool {
	return (a == Recover && b == Work) || (a == Work && b == Recover)
}
