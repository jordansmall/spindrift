package daemon

import (
	"reflect"
	"testing"
)

func TestChildCommand(t *testing.T) {
	cases := []struct {
		name    string
		spec    ChildSpec
		want    []string
		wantErr bool
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ChildCommand(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ChildCommand(%+v): want error, got %v", tc.spec, got)
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
