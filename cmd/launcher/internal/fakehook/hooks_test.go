package fakehook

import (
	"sync"
	"testing"
)

func TestHooks_FireRunsHooksWithOneBasedCallCount(t *testing.T) {
	var h Hooks
	var got []int
	h.On("A", func(n int) { got = append(got, n) })

	h.Fire("A")
	h.Fire("B")
	h.Fire("A")

	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("hook saw calls %v, want [1 2] (per-method counts)", got)
	}
}

func TestHooks_CallsBeforeOnStillCount(t *testing.T) {
	var h Hooks
	h.Fire("A")
	var n int
	h.On("A", func(c int) { n = c })
	h.Fire("A")
	if n != 2 {
		t.Errorf("call count = %d, want 2: calls before On still count", n)
	}
}

func TestHooks_MultipleHooksRunInRegistrationOrder(t *testing.T) {
	var h Hooks
	var order []string
	h.On("A", func(int) { order = append(order, "first") })
	h.On("A", func(int) { order = append(order, "second") })
	h.Fire("A")
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("order = %v, want [first second]", order)
	}
}

func TestHooks_HookRunsOutsideTheLock(t *testing.T) {
	var h Hooks
	h.On("A", func(int) { h.Fire("B") })
	h.On("B", func(int) {})
	h.Fire("A") // deadlocks if Fire holds the lock while running hooks
}

func TestHooks_ConcurrentFireCountsEveryCall(t *testing.T) {
	var h Hooks
	var mu sync.Mutex
	max := 0
	h.On("A", func(n int) {
		mu.Lock()
		defer mu.Unlock()
		if n > max {
			max = n
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.Fire("A") }()
	}
	wg.Wait()
	if max != 50 {
		t.Errorf("max call number = %d, want 50", max)
	}
}
