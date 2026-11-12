package forge

import "testing"

type namer interface{ Name() string }

type plainTracker struct{ IssueTracker }

type namedTracker struct {
	IssueTracker
	name string
}

func (n namedTracker) Name() string { return n.name }

type wrapTracker struct{ IssueTracker }

func (w wrapTracker) Unwrap() IssueTracker { return w.IssueTracker }

type namedWrapTracker struct {
	wrapTracker
	name string
}

func (n namedWrapTracker) Name() string { return n.name }

func TestAsOuterLayerShadowsInner(t *testing.T) {
	inner := namedTracker{name: "inner"}
	outer := namedWrapTracker{wrapTracker: wrapTracker{inner}, name: "outer"}
	got, ok := As[namer](outer)
	if !ok || got.Name() != "outer" {
		t.Fatalf("As = %v, %v; want the outer layer", got, ok)
	}
}

func TestAsSeesInnerCapabilityThroughWrapper(t *testing.T) {
	var it IssueTracker = wrapTracker{wrapTracker{namedTracker{name: "inner"}}}
	got, ok := As[namer](it)
	if !ok || got.Name() != "inner" {
		t.Fatalf("As = %v, %v; want the inner layer", got, ok)
	}
}

func TestAsPlainTrackerIsTypeAssertion(t *testing.T) {
	var it IssueTracker = namedTracker{name: "plain"}
	got, ok := As[namer](it)
	if !ok || got.Name() != "plain" {
		t.Fatalf("As = %v, %v; want the tracker itself", got, ok)
	}
	if _, ok := As[namer](plainTracker{}); ok {
		t.Fatal("As found a capability the tracker lacks")
	}
}

func TestAsAbsentIsFalse(t *testing.T) {
	if _, ok := As[namer](wrapTracker{plainTracker{}}); ok {
		t.Fatal("As found a capability no layer has")
	}
	if _, ok := As[namer](nil); ok {
		t.Fatal("As(nil) = true")
	}
	if _, ok := As[namer](wrapTracker{}); ok {
		t.Fatal("As over a wrapper of nil = true")
	}
}
