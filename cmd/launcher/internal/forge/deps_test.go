package forge

import (
	"errors"
	"reflect"
	"testing"
)

// depsStub embeds a nil IssueTracker: only the methods a case exercises are
// defined, and any other call panics, which proves it was never made.
type depsStub struct {
	IssueTracker
	body      string
	issueErr  error
	issueRead bool
}

func (s *depsStub) Issue(string) (Issue, error) {
	s.issueRead = true
	return Issue{Body: s.body}, s.issueErr
}

type nativeStub struct {
	*depsStub
	native []string
	err    error
}

func (s nativeStub) NativeDepsOf(string) ([]string, error) { return s.native, s.err }

type grammarStub struct {
	*depsStub
	g BodyGrammar
}

func (s grammarStub) BodyGrammar() BodyGrammar { return s.g }

type nativeGrammarStub struct {
	nativeStub
	g BodyGrammar
}

func (s nativeGrammarStub) BodyGrammar() BodyGrammar { return s.g }

func ids(deps []Dependency) []string {
	var out []string
	for _, d := range deps {
		out = append(out, d.ID)
	}
	return out
}

func TestDepsOf(t *testing.T) {
	nativeErr := errors.New("graphql down")
	const refBody = "Depends on #7\n"
	const slugBody = "## Blocked by\n\n- other-slug\n\n## Comments\n\n- Blocked by: comment-slug\n"

	tests := []struct {
		name       string
		build      func(*depsStub) IssueTracker
		body       string
		issueErr   error
		want       []string
		wantSource DepSource
		wantErr    error
		wantRead   bool
	}{
		{
			name:       "body only",
			build:      func(s *depsStub) IssueTracker { return s },
			body:       refBody,
			want:       []string{"7"},
			wantSource: DepSourceBody,
			wantRead:   true,
		},
		{
			name:       "native non-empty wins and is not merged",
			build:      func(s *depsStub) IssueTracker { return nativeStub{s, []string{"3"}, nil} },
			body:       refBody,
			want:       []string{"3"},
			wantSource: DepSourceNative,
		},
		{
			name:       "native empty falls back to body",
			build:      func(s *depsStub) IssueTracker { return nativeStub{s, nil, nil} },
			body:       refBody,
			want:       []string{"7"},
			wantSource: DepSourceBody,
			wantRead:   true,
		},
		{
			name:       "native error falls back to body",
			build:      func(s *depsStub) IssueTracker { return nativeStub{s, nil, nativeErr} },
			body:       refBody,
			want:       []string{"7"},
			wantSource: DepSourceBody,
			wantRead:   true,
		},
		{
			name:     "issue error propagates",
			build:    func(s *depsStub) IssueTracker { return s },
			issueErr: errors.New("gone"),
			wantErr:  errors.New("gone"),
			wantRead: true,
		},
		{
			name:     "native error then issue error propagates the issue error",
			build:    func(s *depsStub) IssueTracker { return nativeStub{s, nil, nativeErr} },
			issueErr: errors.New("gone"),
			wantErr:  errors.New("gone"),
			wantRead: true,
		},
		{
			name:       "local grammar reads slugs from the description only",
			build:      func(s *depsStub) IssueTracker { return grammarStub{s, BodyGrammarLocal} },
			body:       slugBody,
			want:       []string{"other-slug"},
			wantSource: DepSourceBody,
			wantRead:   true,
		},
		{
			name: "no-deps grammar returns the native error as-is",
			build: func(s *depsStub) IssueTracker {
				return nativeGrammarStub{nativeStub{s, nil, nativeErr}, BodyGrammarNoDeps}
			},
			body:    refBody,
			wantErr: nativeErr,
		},
		{
			name: "no-deps grammar returns native empty without reading the body",
			build: func(s *depsStub) IssueTracker {
				return nativeGrammarStub{nativeStub{s, nil, nil}, BodyGrammarNoDeps}
			},
			body:       refBody,
			wantSource: DepSourceNative,
		},
		{
			name: "no-deps grammar returns native links",
			build: func(s *depsStub) IssueTracker {
				return nativeGrammarStub{nativeStub{s, []string{"K-1"}, nil}, BodyGrammarNoDeps}
			},
			body:       refBody,
			want:       []string{"K-1"},
			wantSource: DepSourceNative,
		},
		{
			name:  "no-deps grammar without a native reader returns nothing",
			build: func(s *depsStub) IssueTracker { return grammarStub{s, BodyGrammarNoDeps} },
			body:  refBody,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &depsStub{body: tc.body, issueErr: tc.issueErr}
			deps, err := DepsOf(tc.build(s), "1")
			if tc.wantErr != nil {
				if err == nil || err.Error() != tc.wantErr.Error() {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got := ids(deps); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("deps = %v, want %v", got, tc.want)
			}
			for _, d := range deps {
				if d.Source != tc.wantSource {
					t.Errorf("dep %s source = %v, want %v", d.ID, d.Source, tc.wantSource)
				}
			}
			if s.issueRead != tc.wantRead {
				t.Errorf("issue read = %v, want %v", s.issueRead, tc.wantRead)
			}
		})
	}
}

func TestDepsOfNoDepsNativeEmptyIsNonNil(t *testing.T) {
	s := nativeGrammarStub{nativeStub{&depsStub{}, nil, nil}, BodyGrammarNoDeps}
	deps, err := DepsOf(s, "1")
	if err != nil || deps == nil {
		t.Fatalf("deps = %#v, err = %v; want empty non-nil, nil", deps, err)
	}
}

func TestTouchesOf(t *testing.T) {
	const body = "## Touches\n\n- a/**\n\n## Comments\n\n- ## Touches\n- b/**\n"
	const whole = "intro\n\n## Touches\n\n- a/**\n\n## Comments\n\n## Touches\n\n- b/**\n"
	tests := []struct {
		name  string
		build func(*depsStub) IssueTracker
		body  string
		want  []string
	}{
		{"default parses the whole body", func(s *depsStub) IssueTracker { return s }, whole, []string{"a/**", "b/**"}},
		{"local grammar ignores comments", func(s *depsStub) IssueTracker { return grammarStub{s, BodyGrammarLocal} }, whole, []string{"a/**"}},
		{"no-deps grammar still parses touches", func(s *depsStub) IssueTracker { return grammarStub{s, BodyGrammarNoDeps} }, body, []string{"a/**"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TouchesOf(tc.build(&depsStub{body: tc.body}), "1")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("touches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTouchesOfIssueError(t *testing.T) {
	want := errors.New("gone")
	if _, err := TouchesOf(&depsStub{issueErr: want}, "1"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestParseBlockerSlugs(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"slugs under header", "## Blocked by\n\n- a-slug\n- `b-slug`\n- a-slug\n", []string{"a-slug", "b-slug"}},
		{"sentinels skipped, including quoted", "## Blocked by\n\n- None\n- `None`\n- n/a - nothing\n", nil},
		{"comments ignored", "## Blocked by\n\n- a\n\n## Comments\n\n## Blocked by\n\n- c\n", []string{"a"}},
		{"bullets outside the section ignored", "- stray\n\n## Blocked by\n\n- a\n\n## Other\n\n- b\n", []string{"a"}},
		{"fenced bullets ignored", "## Blocked by\n\n```\n- fenced\n```\n- a\n", []string{"a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseBlockerSlugs(tc.body); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseBlockerSlugs = %v, want %v", got, tc.want)
			}
		})
	}
}
