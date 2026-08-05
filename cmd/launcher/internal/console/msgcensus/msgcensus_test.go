package msgcensus

import (
	"reflect"
	"testing"
)

func TestCollectFindsMarkedTypes(t *testing.T) {
	got, err := Collect("testdata/fixture")
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}

	want := []string{"AMsg", "BMsg"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Collect(testdata/fixture) = %v, want %v", got, want)
	}
}

func TestCollectNoMatches(t *testing.T) {
	got, err := Collect("testdata/empty")
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("Collect(testdata/empty) = %v, want empty result", got)
	}
}
