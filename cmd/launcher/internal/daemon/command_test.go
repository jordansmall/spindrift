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
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ChildCommand(%+v) = %v, want %v", tc.spec, got, tc.want)
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
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DoctorCommand(%+v) = %v, want %v", tc.spec, got, tc.want)
			}
			for _, arg := range got {
				if arg == "--max-jobs" || arg == "--max-parallel" {
					t.Fatalf("DoctorCommand(%+v) = %v: must not cap a wave doctor never dispatches", tc.spec, got)
				}
			}
		})
	}
}
