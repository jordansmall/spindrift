package dispatch

import "sync"

// memoizeIssueText resolves a Dispatch's injected issue text at most once. It
// is scoped to one Dispatch by newDispatch: every Box the Dispatch builds
// (Run, each Fix pass, conflict resolution, retries) must see byte-identical
// text because it sits in the prompt's stable prefix (issue #3445). It must
// not widen to the Factory, which a long-lived process reuses across
// dispatches of the same issue (issue #4160). It holds one value because a
// Dispatch has one subject: after the first success it ignores number. An
// error is returned uncached and fails the Dispatch.
func memoizeIssueText(resolve func(string) (string, error)) func(string) (string, error) {
	var (
		mu   sync.Mutex
		text string
		ok   bool
	)
	return func(number string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if ok {
			return text, nil
		}
		t, err := resolve(number)
		if err != nil {
			return "", err
		}
		text, ok = t, true
		return text, nil
	}
}
