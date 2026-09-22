package daemon

import (
	"reflect"
	"strings"
	"testing"
)

func TestChildCommand(t *testing.T) {
	cases := []struct {
		name    string
		spec    ChildSpec
		want    []string
		wantErr bool
		// wantErrSubstr, when set, pins which of two possible errors a spec
		// that is wrong in more than one way reports.
		wantErrSubstr string
	}{
		{
			name: "default attr",
			spec: ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123", Kind: KindDispatch},
			want: []string{"nix", "run", "git+file:///home/op/repo?rev=abc123&allRefs=1", "--", "dispatch", "--max-jobs", "1", "--max-parallel", "1"},
		},
		{
			name: "empty attr treated like default",
			spec: ChildSpec{RepoPath: "/home/op/repo", AppAttr: "", Revision: "abc123", Kind: KindDispatch},
			want: []string{"nix", "run", "git+file:///home/op/repo?rev=abc123&allRefs=1", "--", "dispatch", "--max-jobs", "1", "--max-parallel", "1"},
		},
		{
			name: "named attr",
			spec: ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#dogfood-bwrap", Revision: "abc123", Kind: KindDispatch},
			want: []string{"nix", "run", "git+file:///home/op/repo?rev=abc123&allRefs=1#dogfood-bwrap", "--", "dispatch", "--max-jobs", "1", "--max-parallel", "1"},
		},
		{
			name: "research kind",
			spec: ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123", Kind: KindResearch},
			want: []string{"nix", "run", "git+file:///home/op/repo?rev=abc123&allRefs=1", "--", "research", "--max-jobs", "1", "--max-parallel", "1"},
		},
		{
			name:    "empty repo path",
			spec:    ChildSpec{RepoPath: "", AppAttr: ".#", Revision: "abc123", Kind: KindDispatch},
			wantErr: true,
		},
		{
			name:    "non-absolute repo path",
			spec:    ChildSpec{RepoPath: "relative/path", AppAttr: ".#", Revision: "abc123", Kind: KindDispatch},
			wantErr: true,
		},
		{
			name:    "empty revision",
			spec:    ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "", Kind: KindDispatch},
			wantErr: true,
		},
		{
			name:    "repo path with hash",
			spec:    ChildSpec{RepoPath: "/home/op/re#po", AppAttr: ".#", Revision: "abc123", Kind: KindDispatch},
			wantErr: true,
		},
		{
			name:    "repo path with question mark",
			spec:    ChildSpec{RepoPath: "/home/op/re?po", AppAttr: ".#", Revision: "abc123", Kind: KindDispatch},
			wantErr: true,
		},
		{
			name:    "repo path with ampersand",
			spec:    ChildSpec{RepoPath: "/home/op/re&po", AppAttr: ".#", Revision: "abc123", Kind: KindDispatch},
			wantErr: true,
		},
		{
			name:    "app attr with hash",
			spec:    ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#do#gfood", Revision: "abc123", Kind: KindDispatch},
			wantErr: true,
		},
		{
			name:    "app attr with question mark",
			spec:    ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#do?gfood", Revision: "abc123", Kind: KindDispatch},
			wantErr: true,
		},
		{
			name:    "app attr with ampersand",
			spec:    ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#do&gfood", Revision: "abc123", Kind: KindDispatch},
			wantErr: true,
		},
		{
			name:          "unknown kind",
			spec:          ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123", Kind: Kind("sweep")},
			wantErr:       true,
			wantErrSubstr: "unknown kind",
		},
		{
			// Both halves are wrong: ChildCommand's doc promises the
			// flakeref error wins, so a future reorder that parses the kind
			// first fails here rather than silently changing the message.
			name:          "unpinned beats unknown kind",
			spec:          ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "", Kind: Kind("sweep")},
			wantErr:       true,
			wantErrSubstr: "revision must not be empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ChildCommand(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ChildCommand(%+v): want error, got %v", tc.spec, got)
				}
				if tc.wantErrSubstr != "" && !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Fatalf("ChildCommand(%+v): err = %v, want it to contain %q", tc.spec, err, tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ChildCommand(%+v): unexpected error: %v", tc.spec, err)
			}
			if !reflect.DeepEqual(got.Argv, tc.want) {
				t.Fatalf("ChildCommand(%+v) = %v, want %v", tc.spec, got.Argv, tc.want)
			}
		})
	}
}

func TestSelfCommand(t *testing.T) {
	cases := []struct {
		name    string
		spec    SelfSpec
		want    []string
		wantErr bool
	}{
		{
			name: "default self attr",
			spec: SelfSpec{RepoPath: "/home/op/repo", SelfAttr: ".#daemon", Revision: "abc123", System: "x86_64-linux"},
			want: []string{"nix", "eval", "--raw", "git+file:///home/op/repo?rev=abc123&allRefs=1#apps.x86_64-linux.daemon.program"},
		},
		{
			name: "re-exported self attr",
			spec: SelfSpec{RepoPath: "/home/op/repo", SelfAttr: ".#dogfood-bwrap-daemon", Revision: "abc123", System: "aarch64-linux"},
			want: []string{"nix", "eval", "--raw", "git+file:///home/op/repo?rev=abc123&allRefs=1#apps.aarch64-linux.dogfood-bwrap-daemon.program"},
		},
		{
			name:    "empty repo path",
			spec:    SelfSpec{RepoPath: "", SelfAttr: ".#daemon", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "non-absolute repo path",
			spec:    SelfSpec{RepoPath: "relative/path", SelfAttr: ".#daemon", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "empty revision",
			spec:    SelfSpec{RepoPath: "/home/op/repo", SelfAttr: ".#daemon", Revision: "", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "empty system",
			spec:    SelfSpec{RepoPath: "/home/op/repo", SelfAttr: ".#daemon", Revision: "abc123", System: ""},
			wantErr: true,
		},
		{
			name:    "empty self attr",
			spec:    SelfSpec{RepoPath: "/home/op/repo", SelfAttr: "", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "self attr that is just the default prefix",
			spec:    SelfSpec{RepoPath: "/home/op/repo", SelfAttr: ".#", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "repo path with hash",
			spec:    SelfSpec{RepoPath: "/home/op/re#po", SelfAttr: ".#daemon", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "repo path with question mark",
			spec:    SelfSpec{RepoPath: "/home/op/re?po", SelfAttr: ".#daemon", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "repo path with ampersand",
			spec:    SelfSpec{RepoPath: "/home/op/re&po", SelfAttr: ".#daemon", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "self attr with hash",
			spec:    SelfSpec{RepoPath: "/home/op/repo", SelfAttr: ".#dae#mon", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "self attr with question mark",
			spec:    SelfSpec{RepoPath: "/home/op/repo", SelfAttr: ".#dae?mon", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
		{
			name:    "self attr with ampersand",
			spec:    SelfSpec{RepoPath: "/home/op/repo", SelfAttr: ".#dae&mon", Revision: "abc123", System: "x86_64-linux"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelfCommand(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SelfCommand(%+v): want error, got %v", tc.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelfCommand(%+v): unexpected error: %v", tc.spec, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SelfCommand(%+v) = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}
}

func TestDoctorCommand(t *testing.T) {
	cases := []struct {
		name    string
		spec    DoctorSpec
		want    []string
		wantErr bool
	}{
		{
			name: "default attr",
			spec: DoctorSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123"},
			want: []string{"nix", "run", "git+file:///home/op/repo?rev=abc123&allRefs=1", "--", "doctor"},
		},
		{
			name: "named attr",
			spec: DoctorSpec{RepoPath: "/home/op/repo", AppAttr: ".#dogfood-bwrap", Revision: "abc123"},
			want: []string{"nix", "run", "git+file:///home/op/repo?rev=abc123&allRefs=1#dogfood-bwrap", "--", "doctor"},
		},
		{
			name:    "empty revision",
			spec:    DoctorSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: ""},
			wantErr: true,
		},
		{
			name:    "non-absolute repo path",
			spec:    DoctorSpec{RepoPath: "relative/path", AppAttr: ".#", Revision: "abc123"},
			wantErr: true,
		},
		{
			name:    "app attr with hash",
			spec:    DoctorSpec{RepoPath: "/home/op/repo", AppAttr: ".#do#gfood", Revision: "abc123"},
			wantErr: true,
		},
		{
			name:    "app attr with question mark",
			spec:    DoctorSpec{RepoPath: "/home/op/repo", AppAttr: ".#do?gfood", Revision: "abc123"},
			wantErr: true,
		},
		{
			name:    "app attr with ampersand",
			spec:    DoctorSpec{RepoPath: "/home/op/repo", AppAttr: ".#do&gfood", Revision: "abc123"},
			wantErr: true,
		},
		{
			name:    "repo path with hash",
			spec:    DoctorSpec{RepoPath: "/home/op/re#po", AppAttr: ".#", Revision: "abc123"},
			wantErr: true,
		},
		{
			name:    "repo path with question mark",
			spec:    DoctorSpec{RepoPath: "/home/op/re?po", AppAttr: ".#", Revision: "abc123"},
			wantErr: true,
		},
		{
			name:    "repo path with ampersand",
			spec:    DoctorSpec{RepoPath: "/home/op/re&po", AppAttr: ".#", Revision: "abc123"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DoctorCommand(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("DoctorCommand(%+v): want error, got %v", tc.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("DoctorCommand(%+v): unexpected error: %v", tc.spec, err)
			}
			if !reflect.DeepEqual(got.Argv, tc.want) {
				t.Fatalf("DoctorCommand(%+v) = %v, want %v", tc.spec, got.Argv, tc.want)
			}
			for _, arg := range got.Argv {
				if arg == "--max-jobs" || arg == "--max-parallel" {
					t.Fatalf("DoctorCommand(%+v) = %v: must not cap a wave doctor never dispatches", tc.spec, got.Argv)
				}
			}
		})
	}
}

func TestChildEnv(t *testing.T) {
	cases := []struct {
		name  string
		env   []string
		knobs []string
		want  []string
	}{
		{
			name:  "knob key present in env and knobs is stripped",
			env:   []string{"PATH=/bin", "MODEL=opus", "HOME=/home/op"},
			knobs: []string{"MODEL"},
			want:  []string{"PATH=/bin", "HOME=/home/op"},
		},
		{
			name:  "non-knob and secret-shaped var survive",
			env:   []string{"PATH=/bin", "GH_TOKEN=secret"},
			knobs: []string{"MODEL"},
			want:  []string{"PATH=/bin", "GH_TOKEN=secret"},
		},
		{
			name:  "order is preserved relative to input env",
			env:   []string{"Z=1", "MODEL=opus", "A=2", "MAX_JOBS=1"},
			knobs: []string{"MODEL", "MAX_JOBS"},
			want:  []string{"Z=1", "A=2"},
		},
		{
			name:  "empty knobs returns every entry",
			env:   []string{"PATH=/bin", "MODEL=opus"},
			knobs: nil,
			want:  []string{"PATH=/bin", "MODEL=opus"},
		},
		{
			name:  "exact-key match: MODEL_EXTRA survives, MODEL is stripped",
			env:   []string{"MODEL_EXTRA=x", "MODEL="},
			knobs: []string{"MODEL"},
			want:  []string{"MODEL_EXTRA=x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withoutKeys(tc.env, tc.knobs)
			if got == nil {
				t.Fatalf("withoutKeys(%v, %v) = nil, want non-nil", tc.env, tc.knobs)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("withoutKeys(%v, %v) = %v, want %v", tc.env, tc.knobs, got, tc.want)
			}
		})
	}
}

func TestChildEnvEmptyEnvReturnsNonNil(t *testing.T) {
	got := withoutKeys(nil, []string{"MODEL"})
	if got == nil {
		t.Fatalf("withoutKeys(nil, ...) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("withoutKeys(nil, ...) = %v, want empty", got)
	}
}

func TestChildCommandReturnsChildEnv(t *testing.T) {
	spec := ChildSpec{
		RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123", Kind: KindDispatch,
		Env:   []string{"PATH=/bin", "MODEL=opus"},
		Knobs: []string{"MODEL"},
	}
	got, err := ChildCommand(spec)
	if err != nil {
		t.Fatalf("ChildCommand(%+v): unexpected error: %v", spec, err)
	}
	// SPINDRIFT_REPORT_FD=3 rides every ChildCommand result now (see
	// TestChildCommandSetsReportFD below for that half in isolation); this
	// test's own focus stays on the knob-stripping behaviour it shares with
	// DoctorCommand.
	want := []string{"PATH=/bin", "SPINDRIFT_REPORT_FD=3"}
	if !reflect.DeepEqual(got.Env, want) {
		t.Fatalf("ChildCommand(%+v) env = %v, want %v", spec, got.Env, want)
	}
}

func TestDoctorCommandReturnsChildEnv(t *testing.T) {
	spec := DoctorSpec{
		RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123",
		Env:   []string{"PATH=/bin", "MODEL=opus"},
		Knobs: []string{"MODEL"},
	}
	got, err := DoctorCommand(spec)
	if err != nil {
		t.Fatalf("DoctorCommand(%+v): unexpected error: %v", spec, err)
	}
	want := []string{"PATH=/bin"}
	if !reflect.DeepEqual(got.Env, want) {
		t.Fatalf("DoctorCommand(%+v) env = %v, want %v", spec, got.Env, want)
	}
}

// TestChildCommandSetsReportFD pins the host seam's half of issue #3627: a
// dispatch or research child gets SPINDRIFT_REPORT_FD=3, doctor gets
// neither the variable nor (elsewhere, at the exec.Cmd level) the
// descriptor.
func TestChildCommandSetsReportFD(t *testing.T) {
	for _, kind := range []Kind{KindDispatch, KindResearch} {
		spec := ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123", Kind: kind, Env: []string{"PATH=/bin"}}
		got, err := ChildCommand(spec)
		if err != nil {
			t.Fatalf("ChildCommand(%+v): unexpected error: %v", spec, err)
		}
		want := []string{"PATH=/bin", "SPINDRIFT_REPORT_FD=3"}
		if !reflect.DeepEqual(got.Env, want) {
			t.Errorf("ChildCommand(%+v) env = %v, want %v", spec, got.Env, want)
		}
	}
}

// TestChildCommandStripsAmbientReportFD guards withoutKeys's promise: an
// operator's stray SPINDRIFT_REPORT_FD (or a nested daemon's) already
// sitting in the daemon's own environment must not survive alongside the
// value ChildCommand sets, or the child would see two conflicting
// definitions of the same variable.
func TestChildCommandStripsAmbientReportFD(t *testing.T) {
	spec := ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123", Kind: KindDispatch, Env: []string{"PATH=/bin", "SPINDRIFT_REPORT_FD=9"}}
	got, err := ChildCommand(spec)
	if err != nil {
		t.Fatalf("ChildCommand(%+v): unexpected error: %v", spec, err)
	}
	want := []string{"PATH=/bin", "SPINDRIFT_REPORT_FD=3"}
	if !reflect.DeepEqual(got.Env, want) {
		t.Fatalf("ChildCommand(%+v) env = %v, want %v (ambient SPINDRIFT_REPORT_FD=9 must not survive)", spec, got.Env, want)
	}
}

// TestChildCommandCarriesSignalCarrier pins the knob's whole reason for
// being env-only (see flagtable_gen.go): BOX_SIGNAL_CARRIER never lands in
// the Launcher input document's settings map, so it is never a Knobs entry
// and withoutKeys never strips it — it rides s.Env straight through. The
// "beside a stripped knob" case is the one that exercises the strip path:
// Knobs names a settings key, that key goes, and the carrier beside it
// stays.
func TestChildCommandCarriesSignalCarrier(t *testing.T) {
	tests := []struct {
		name  string
		env   []string
		knobs []string
		want  []string
	}{
		{
			name: "set",
			env:  []string{"PATH=/bin", "BOX_SIGNAL_CARRIER=socket"},
			want: []string{"PATH=/bin", "BOX_SIGNAL_CARRIER=socket", "SPINDRIFT_REPORT_FD=3"},
		},
		{
			name: "absent",
			env:  []string{"PATH=/bin"},
			want: []string{"PATH=/bin", "SPINDRIFT_REPORT_FD=3"},
		},
		{
			name:  "beside a stripped knob",
			env:   []string{"PATH=/bin", "MODEL=opus", "BOX_SIGNAL_CARRIER=socket"},
			knobs: []string{"MODEL"},
			want:  []string{"PATH=/bin", "BOX_SIGNAL_CARRIER=socket", "SPINDRIFT_REPORT_FD=3"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := ChildSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123", Kind: KindDispatch, Env: tc.env, Knobs: tc.knobs}
			got, err := ChildCommand(spec)
			if err != nil {
				t.Fatalf("ChildCommand(%+v): unexpected error: %v", spec, err)
			}
			if !reflect.DeepEqual(got.Env, tc.want) {
				t.Fatalf("ChildCommand(%+v) env = %v, want %v", spec, got.Env, tc.want)
			}
		})
	}
}

// TestDoctorCommandOmitsReportFD guards the other half: the doctor
// preflight dispatches nothing, so it gets no report pipe and no
// SPINDRIFT_REPORT_FD.
func TestDoctorCommandOmitsReportFD(t *testing.T) {
	spec := DoctorSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123", Env: []string{"PATH=/bin"}}
	got, err := DoctorCommand(spec)
	if err != nil {
		t.Fatalf("DoctorCommand(%+v): unexpected error: %v", spec, err)
	}
	want := []string{"PATH=/bin"}
	if !reflect.DeepEqual(got.Env, want) {
		t.Fatalf("DoctorCommand(%+v) env = %v, want %v (doctor must not get SPINDRIFT_REPORT_FD)", spec, got.Env, want)
	}
}

// TestDoctorCommandStripsAmbientReportFD is TestChildCommandStripsAmbientReportFD's
// other half (issue #3627's review finding): unlike ChildCommand, DoctorCommand
// never re-adds SPINDRIFT_REPORT_FD, so an ambient one in DoctorSpec.Env must
// still be stripped rather than left to leak through untouched — the preflight
// gets neither an ambient nor a daemon-assigned report fd.
func TestDoctorCommandStripsAmbientReportFD(t *testing.T) {
	spec := DoctorSpec{RepoPath: "/home/op/repo", AppAttr: ".#", Revision: "abc123", Env: []string{"PATH=/bin", "SPINDRIFT_REPORT_FD=9"}}
	got, err := DoctorCommand(spec)
	if err != nil {
		t.Fatalf("DoctorCommand(%+v): unexpected error: %v", spec, err)
	}
	want := []string{"PATH=/bin"}
	if !reflect.DeepEqual(got.Env, want) {
		t.Fatalf("DoctorCommand(%+v) env = %v, want %v (ambient SPINDRIFT_REPORT_FD=9 must not survive)", spec, got.Env, want)
	}
}
