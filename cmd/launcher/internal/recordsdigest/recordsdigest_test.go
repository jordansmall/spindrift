package recordsdigest

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/settle"
)

func render(Input) Output            { return Output{} }
func anchors(string) map[string]bool { return nil }

func reset(t *testing.T) {
	saved := registry
	registry = nil
	t.Cleanup(func() { registry = saved })
}

func TestRegisterAndByName(t *testing.T) {
	reset(t)
	Register(Strategy{Name: "a", Anchors: anchors, Render: func(in Input) Output { return Output{Text: "t", Latest: in.Cursor} }})
	s, ok := ByName("a")
	if !ok {
		t.Fatal("registered strategy not found")
	}
	if got := s.Render(Input{Cursor: "r1"}); got.Text != "t" || got.Latest != "r1" {
		t.Errorf("Render = %+v", got)
	}
	if _, ok := ByName("b"); ok {
		t.Error("unregistered name found")
	}
}

func TestAllInRegistrationOrder(t *testing.T) {
	reset(t)
	if got := All(); len(got) != 0 {
		t.Fatalf("empty registry: All = %v", got)
	}
	Register(Strategy{Name: "b", Render: render, Anchors: anchors})
	Register(Strategy{Name: "a", Render: render, Anchors: anchors})
	got := All()
	if len(got) != 2 || got[0].Name != "b" || got[1].Name != "a" {
		t.Fatalf("All = %v, want [b a]", got)
	}
	got[0].Name = "x"
	if s, ok := ByName("b"); !ok || s.Name != "b" {
		t.Error("mutating the returned slice changed the registry")
	}
}

func TestRegisterPanics(t *testing.T) {
	for name, s := range map[string]Strategy{
		"empty name":  {Render: render, Anchors: anchors},
		"nil render":  {Name: "x", Anchors: anchors},
		"nil anchors": {Name: "x", Render: render},
	} {
		t.Run(name, func(t *testing.T) {
			reset(t)
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			Register(s)
		})
	}
	t.Run("duplicate", func(t *testing.T) {
		reset(t)
		Register(Strategy{Name: "d", Render: render, Anchors: anchors})
		defer func() {
			if recover() == nil {
				t.Error("no panic")
			}
		}()
		Register(Strategy{Name: "d", Render: render, Anchors: anchors})
	})
}

func TestStrategyKnobs(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	t.Run("nil hook owns none", func(t *testing.T) {
		got, err := Strategy{Name: "n", Render: render}.Knobs(none)
		if err != nil || got != (Knobs{}) {
			t.Errorf("Knobs = %+v, %v; want zero, nil", got, err)
		}
	})
	t.Run("hook result passes through", func(t *testing.T) {
		want := Knobs{Limits: Limits{MinSample: 3, MaxBytes: 9}, Cooldown: time.Hour}
		s := Strategy{Name: "k", Render: render, ReadKnobs: func(l Lookup) (Knobs, error) {
			if v, ok := l("X"); !ok || v != "1" {
				t.Errorf("lookup X = %q, %v", v, ok)
			}
			return want, nil
		}}
		got, err := s.Knobs(func(env string) (string, bool) { return "1", env == "X" })
		if err != nil || got != want {
			t.Errorf("Knobs = %+v, %v; want %+v", got, err, want)
		}
	})
	t.Run("hook error passes through", func(t *testing.T) {
		boom := errors.New("boom")
		s := Strategy{Name: "e", Render: render, ReadKnobs: func(Lookup) (Knobs, error) { return Knobs{}, boom }}
		if _, err := s.Knobs(none); !errors.Is(err, boom) {
			t.Errorf("err = %v, want boom", err)
		}
	})
}

func TestWithPriorSkipsForeignPrior(t *testing.T) {
	var got []string
	var gotPrior []any
	s := WithPrior(Strategy{Name: "p", Anchors: anchors},
		func(f settle.PriorFinding) string { return f.Issue.Number },
		func(in Input, prior []string) Output {
			got, gotPrior = prior, in.Prior
			return Output{Text: "t", Latest: in.Cursor}
		})
	if s.Name != "p" || s.Anchors == nil {
		t.Errorf("WithPrior dropped the base fields: %+v", s)
	}
	var f settle.PriorFinding
	f.Issue.Number = "12"
	if v := s.ParsePrior(f); v != "12" {
		t.Errorf("ParsePrior = %#v, want the typed value", v)
	}
	out := s.Render(Input{Cursor: "r1", Prior: []any{"a", 7, "b"}})
	if out.Text != "t" || out.Latest != "r1" {
		t.Errorf("Render = %+v", out)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("render prior = %v, want %v (a prior of another type is skipped)", got, want)
	}
	if gotPrior != nil {
		t.Errorf("render saw in.Prior = %v, want nil", gotPrior)
	}
}
