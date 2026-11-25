//go:build !wasip1

package connector

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPagesIntCursor(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		lastPage  int // the page that reports no more
		wantSeen  []int
		wantClass Class // empty: success
		wantText  string
	}{
		{"one page", 5, 1, []int{1}, "", ""},
		{"several pages", 5, 3, []int{1, 2, 3}, "", ""},
		{"exactly limit", 3, 3, []int{1, 2, 3}, "", ""},
		{"past the bound", 3, 4, []int{1, 2, 3}, Permanent, "limit of 3 pages"},
		{"limit of one, more", 1, 2, []int{1}, Permanent, "limit of 1 pages"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var seen []int
			err := Pages(tc.limit, 1, func(page int) (int, bool, error) {
				seen = append(seen, page)
				return page + 1, page < tc.lastPage, nil
			})
			if !reflect.DeepEqual(seen, tc.wantSeen) {
				t.Errorf("fetched %v, want %v", seen, tc.wantSeen)
			}
			if tc.wantClass == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if got := classOf(t, err); got != tc.wantClass {
				t.Errorf("class = %q, want %q", got, tc.wantClass)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("err = %q, want it to contain %q", err, tc.wantText)
			}
		})
	}
}

func TestPagesStringCursor(t *testing.T) {
	chain := map[string]string{"": "a", "a": "b", "b": ""}
	var seen []string
	err := Pages(10, "", func(c string) (string, bool, error) {
		seen = append(seen, c)
		next := chain[c]
		return next, next != "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "a", "b"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("cursors = %v, want %v", seen, want)
	}
}

func TestPagesFetchError(t *testing.T) {
	boom := Errorf(RateLimited, "slow down")
	calls := 0
	err := Pages(5, 0, func(int) (int, bool, error) {
		calls++
		if calls == 2 {
			return 0, true, boom
		}
		return calls, true, nil
	})
	if err != boom {
		t.Errorf("err = %v, want fetch's own error", err)
	}
	if calls != 2 {
		t.Errorf("fetch ran %d times, want 2", calls)
	}
}

func TestPagesLimitBelowOne(t *testing.T) {
	for _, limit := range []int{0, -1} {
		called := false
		err := Pages(limit, 0, func(int) (int, bool, error) {
			called = true
			return 0, false, nil
		})
		var ce *Error
		if !errors.As(err, &ce) || ce.Class != Permanent || called {
			t.Errorf("limit %d: err = %v, called = %v, want a Permanent error and no fetch", limit, err, called)
		}
	}
}
