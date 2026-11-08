package settle

import "testing"

func TestStepLine(t *testing.T) {
	tests := []struct {
		name string
		step Step
		want string
	}{
		{"status only", Step{Key: "7", Landing: "pr/1", Status: StatusPostForcePushWait}, "    #7  landing=pr/1  status=post-force-push-wait\n"},
		{"status and detail", Step{Key: "7", Landing: "pr/1", Status: StatusMarkReadyFailed, Detail: "!! boom"}, "    #7  landing=pr/1  status=mark-ready-failed  !! boom\n"},
		{"no landing", Step{Key: "7", Status: StatusPostForcePushRed}, "    #7  status=post-force-push-red\n"},
		{"no status", Step{Key: "7", Landing: "pr/1", Detail: "fix-pass=1/3"}, "    #7  landing=pr/1  fix-pass=1/3\n"},
		{"empty landing drops the segment", Step{Key: "5", Status: StatusMarkReadyFailed, Detail: "!! x"}, "    #5  status=mark-ready-failed  !! x\n"},
		{"key only", Step{Key: "7"}, "    #7\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.step.Line(); got != tc.want {
				t.Errorf("Line() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStdoutProgress_WritesToCurrentStdout(t *testing.T) {
	st := Step{Key: "7", Landing: "pr/1", Status: StatusPostForcePushWait}
	got := captureStdout(t, func() { StdoutProgress{}.Step(st) })
	if want := st.Line(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestProgressRecorder_CollectsStepsInOrder(t *testing.T) {
	var r ProgressRecorder
	a := Step{Key: "1", Status: StatusPostForcePushWait}
	b := Step{Key: "2", Detail: "note=x"}
	r.Step(a)
	r.Step(b)
	got := r.Steps()
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("Steps() = %+v, want [%+v %+v]", got, a, b)
	}
	got[0].Key = "mutated"
	if r.Steps()[0].Key != "1" {
		t.Error("Steps() must return a copy")
	}
}
