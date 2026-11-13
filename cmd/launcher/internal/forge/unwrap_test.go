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

type plainForge struct{ CodeForge }

type namedForge struct {
	CodeForge
	name string
}

func (n namedForge) Name() string { return n.name }

type wrapForge struct{ CodeForge }

func (w wrapForge) UnwrapCodeForge() CodeForge { return w.CodeForge }

type namedWrapForge struct {
	wrapForge
	name string
}

func (n namedWrapForge) Name() string { return n.name }

func TestAsCodeForgeOuterLayerShadowsInner(t *testing.T) {
	inner := namedForge{name: "inner"}
	outer := namedWrapForge{wrapForge: wrapForge{inner}, name: "outer"}
	got, ok := AsCodeForge[namer](outer)
	if !ok || got.Name() != "outer" {
		t.Fatalf("AsCodeForge = %v, %v; want the outer layer", got, ok)
	}
}

func TestAsCodeForgeSeesInnerCapabilityThroughWrapper(t *testing.T) {
	var cf CodeForge = wrapForge{wrapForge{namedForge{name: "inner"}}}
	got, ok := AsCodeForge[namer](cf)
	if !ok || got.Name() != "inner" {
		t.Fatalf("AsCodeForge = %v, %v; want the inner layer", got, ok)
	}
}

func TestAsCodeForgePlainForgeIsTypeAssertion(t *testing.T) {
	var cf CodeForge = namedForge{name: "plain"}
	got, ok := AsCodeForge[namer](cf)
	if !ok || got.Name() != "plain" {
		t.Fatalf("AsCodeForge = %v, %v; want the forge itself", got, ok)
	}
	if _, ok := AsCodeForge[namer](plainForge{}); ok {
		t.Fatal("AsCodeForge found a capability the forge lacks")
	}
}

func TestAsCodeForgeAbsentIsFalse(t *testing.T) {
	if _, ok := AsCodeForge[namer](wrapForge{plainForge{}}); ok {
		t.Fatal("AsCodeForge found a capability no layer has")
	}
	if _, ok := AsCodeForge[namer](nil); ok {
		t.Fatal("AsCodeForge(nil) = true")
	}
	if _, ok := AsCodeForge[namer](wrapForge{}); ok {
		t.Fatal("AsCodeForge over a wrapper of nil = true")
	}
}

type hiderTracker struct {
	wrapTracker
	hidden any
}

func (h hiderTracker) Hides(capability any) bool {
	_, ok := capability.(*namer)
	_, want := h.hidden.(*namer)
	return ok && want
}

func TestAsHiderStopsUnwrapping(t *testing.T) {
	inner := namedTracker{name: "inner"}
	hider := hiderTracker{wrapTracker: wrapTracker{inner}, hidden: (*namer)(nil)}
	if _, ok := As[namer](hider); ok {
		t.Fatal("As looked past a layer that hides the capability")
	}
}

func TestAsHiderLeavesOtherCapabilitiesVisible(t *testing.T) {
	inner := namedTracker{name: "inner"}
	hider := hiderTracker{wrapTracker: wrapTracker{inner}}
	got, ok := As[namer](hider)
	if !ok || got.Name() != "inner" {
		t.Fatalf("As = %v, %v; want the inner layer", got, ok)
	}
}

type namedHiderTracker struct {
	hiderTracker
	name string
}

func (n namedHiderTracker) Name() string { return n.name }

func TestAsLayerAboveHiderStillWins(t *testing.T) {
	hider := hiderTracker{wrapTracker: wrapTracker{namedTracker{name: "inner"}}, hidden: (*namer)(nil)}
	outer := namedWrapTracker{wrapTracker: wrapTracker{hider}, name: "outer"}
	got, ok := As[namer](outer)
	if !ok || got.Name() != "outer" {
		t.Fatalf("As = %v, %v; want the outer layer", got, ok)
	}
	own := namedHiderTracker{hiderTracker: hider, name: "own"}
	got, ok = As[namer](own)
	if !ok || got.Name() != "own" {
		t.Fatalf("As = %v, %v; want the hider's own implementation", got, ok)
	}
}
