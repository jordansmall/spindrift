package recordsdigest

import "testing"

func render(Input) Output { return Output{} }

func reset(t *testing.T) {
	saved := registry
	registry = nil
	t.Cleanup(func() { registry = saved })
}

func TestRegisterAndByName(t *testing.T) {
	reset(t)
	Register(Strategy{Name: "a", Render: func(in Input) Output { return Output{Text: "t", Latest: in.Cursor} }})
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

func TestRegisterPanics(t *testing.T) {
	for name, s := range map[string]Strategy{
		"empty name": {Render: render},
		"nil render": {Name: "x"},
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
		Register(Strategy{Name: "d", Render: render})
		defer func() {
			if recover() == nil {
				t.Error("no panic")
			}
		}()
		Register(Strategy{Name: "d", Render: render})
	})
}
