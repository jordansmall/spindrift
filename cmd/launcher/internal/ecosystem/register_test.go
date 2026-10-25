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

func TestRegister_InstallsCopyInOrder(t *testing.T) {
	var fresh []Strategy
	in := []Strategy{{Name: "first"}, {Name: "second"}}
	register(&fresh, in)

	if len(fresh) != 2 || fresh[0].Name != "first" || fresh[1].Name != "second" {
		t.Fatalf("installed = %+v, want first then second", fresh)
	}
	in[0].Name = "mutated"
	if fresh[0].Name != "first" {
		t.Errorf("installed list aliases the caller's slice: %+v", fresh)
	}
}
