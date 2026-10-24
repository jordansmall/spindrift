package ecosystem

import "testing"

func TestRegister_SecondCallPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("second Register call did not panic")
		}
	}()
	var fresh []Strategy
	register(&fresh, []Strategy{{Name: "first"}})
	register(&fresh, []Strategy{{Name: "second"}})
}

func TestRegister_DuplicateNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register with a duplicate Name did not panic")
		}
	}()
	var fresh []Strategy
	register(&fresh, []Strategy{{Name: "a"}, {Name: "b"}, {Name: "a"}})
}

func TestRegister_EmptyListPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register with an empty list did not panic")
		}
	}()
	var fresh []Strategy
	register(&fresh, nil)
}

func TestRegister_InstallsTable(t *testing.T) {
	savedRegistered, savedTable := registered, Table
	registered, Table = nil, nil
	defer func() { registered, Table = savedRegistered, savedTable }()

	Register(Strategy{Name: "first"})
	for name, got := range map[string][]Strategy{"Registered()": Registered(), "Table": Table} {
		if len(got) != 1 || got[0].Name != "first" {
			t.Errorf("%s = %+v, want exactly the one Strategy %q", name, got, "first")
		}
	}

	defer func() {
		if recover() == nil {
			t.Error("second Register call did not panic")
		}
	}()
	Register(Strategy{Name: "second"})
}
