package exitcode

import (
	"strings"
	"testing"
)

func TestDispatchTableIsIndexedByCode(t *testing.T) {
	seen := map[string]bool{}
	for i, r := range DispatchTable {
		if int(r.Code) != i {
			t.Errorf("row %d has Code %d", i, r.Code)
		}
		if r.Name == "" {
			t.Errorf("row %d has no Name", i)
		}
		if seen[r.Outcome] {
			t.Errorf("duplicate outcome %q", r.Outcome)
		}
		seen[r.Outcome] = true
	}
	if want := int(SignalledStop) + 1; len(DispatchTable) != want {
		t.Errorf("DispatchTable has %d rows, want %d", len(DispatchTable), want)
	}
}

func TestDispatchTableNamesMatchConsts(t *testing.T) {
	want := map[Dispatch]string{
		Dispatched:       "Dispatched",
		Failed:           "Failed",
		QueueEmpty:       "QueueEmpty",
		NoneDispatchable: "NoneDispatchable",
		ImageStale:       "ImageStale",
		HostTainted:      "HostTainted",
		ConfigInvalid:    "ConfigInvalid",
		SignalledStop:    "SignalledStop",
	}
	if len(want) != len(DispatchTable) {
		t.Fatalf("name map has %d entries, table has %d rows", len(want), len(DispatchTable))
	}
	for _, r := range DispatchTable {
		if r.Name != want[r.Code] {
			t.Errorf("code %d Name = %q, want %q", r.Code, r.Name, want[r.Code])
		}
	}
}

func TestLookupDispatch(t *testing.T) {
	for i := range DispatchTable {
		if got := LookupDispatch(i); got != DispatchTable[i] {
			t.Errorf("LookupDispatch(%d) = %+v, want %+v", i, got, DispatchTable[i])
		}
	}
	for _, code := range []int{-1, len(DispatchTable), 42} {
		if got := LookupDispatch(code); got != DispatchTable[Failed] {
			t.Errorf("LookupDispatch(%d) = %+v, want the Failed row", code, got)
		}
	}
	if f := DispatchTable[Failed]; f.Outcome != "error" || f.Action != Backoff || f.Halt != NoHalt {
		t.Errorf("Failed row = %+v", f)
	}
}

func TestVerdict(t *testing.T) {
	r := LookupDispatch(int(SignalledStop))
	if a, h := r.Verdict(true); a != HaltPool || h != HaltSignalled {
		t.Errorf("closed Stop: got %v, %v", a, h)
	}
	if a, h := r.Verdict(false); a != Backoff || h != NoHalt {
		t.Errorf("open Stop: got %v, %v", a, h)
	}
	for _, stop := range []bool{true, false} {
		r := LookupDispatch(int(HostTainted))
		if a, h := r.Verdict(stop); a != HaltPool || h != HaltHostTainted {
			t.Errorf("non-gated row with stopClosed=%v: got %v, %v", stop, a, h)
		}
	}
}

func TestDoctorTable(t *testing.T) {
	seen := map[string]bool{}
	for i, r := range DoctorTable {
		if int(r.Code) != i {
			t.Errorf("row %d has Code %d", i, r.Code)
		}
		if r.Name == "" {
			t.Errorf("row %d has no Name", i)
		}
		if !strings.HasPrefix(r.Outcome, "doctor-") {
			t.Errorf("outcome %q lacks the doctor- prefix", r.Outcome)
		}
		if seen[r.Outcome] {
			t.Errorf("duplicate outcome %q", r.Outcome)
		}
		seen[r.Outcome] = true
		if r.Healthy != (r.Detail == "" && r.Remedy == "") {
			t.Errorf("row %d: Healthy=%v but Detail/Remedy = %q/%q", i, r.Healthy, r.Detail, r.Remedy)
		}
	}
	if want := int(DoctorRequiredLabelsMissing) + 1; len(DoctorTable) != want {
		t.Errorf("DoctorTable has %d rows, want %d", len(DoctorTable), want)
	}
}

func TestDoctorTableNamesMatchConsts(t *testing.T) {
	want := map[Doctor]string{
		DoctorHealthy:               "DoctorHealthy",
		DoctorUnclassified:          "DoctorUnclassified",
		DoctorConfigInvalid:         "DoctorConfigInvalid",
		DoctorConnectivity:          "DoctorConnectivity",
		DoctorRequiredLabelsMissing: "DoctorRequiredLabelsMissing",
	}
	if len(want) != len(DoctorTable) {
		t.Fatalf("name map has %d entries, table has %d rows", len(want), len(DoctorTable))
	}
	for _, r := range DoctorTable {
		if r.Name != want[r.Code] {
			t.Errorf("code %d Name = %q, want %q", r.Code, r.Name, want[r.Code])
		}
	}
}

func TestLookupDoctor(t *testing.T) {
	for i := range DoctorTable {
		r, ok := LookupDoctor(i)
		if !ok || r != DoctorTable[i] {
			t.Errorf("LookupDoctor(%d) = %+v, %v", i, r, ok)
		}
	}
	for _, code := range []int{-1, len(DoctorTable)} {
		if _, ok := LookupDoctor(code); ok {
			t.Errorf("LookupDoctor(%d) ok, want false", code)
		}
	}
}
