package ecosystem

import (
	"os"
	"testing"
)

// The Bundle owns the real Register call; the core package's own tests run
// with it unlinked, so register the same Strategies here or Table is empty.
func TestMain(m *testing.M) {
	Register(Cargo, Npm, Yarn, Pnpm)
	os.Exit(m.Run())
}

func TestRegister_SecondCallPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("second Register call did not panic")
		}
	}()
	Register(Cargo)
}

func TestRegister_DuplicateNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register with a duplicate Name did not panic")
		}
	}()
	var fresh []Strategy
	register(&fresh, []Strategy{Cargo, Npm, Cargo})
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
