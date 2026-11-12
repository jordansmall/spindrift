package connectorhost_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/tokenrefresh"
)

// rotate replaces path with token the way the minter does: write beside it,
// then rename, so a reader never sees a half-written file.
func rotate(t testing.TB, path, token string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		t.Error(err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Error(err)
	}
}

// followBearer wires a Network's credential to a followed refresh file, read
// on every request.
func followBearer(static, path string) func(*connectorhost.Network) {
	token := tokenrefresh.Follow(static, path)
	return func(n *connectorhost.Network) {
		n.Authorize = func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+token())
			return nil
		}
	}
}

func TestNetworkFixtureCarriesARotatedTokenOnTheNextRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	rotate(t, file, "token-A")
	c, b, audit, params := loadNetworkFixtureWith(t, "network.wasm", followBearer("", file))

	want := []string{"Bearer token-A", "Bearer token-A", "Bearer token-B", "Bearer token-B"}
	for i, w := range want {
		if i == 2 {
			rotate(t, file, "token-B")
		}
		var out fetched
		if err := c.Call(context.Background(), "fetch", params, &out); err != nil || out.Status != 200 {
			t.Fatalf("call %d = %+v, %v: a rotation dropped a request", i, out, err)
		}
		if got := b.last.Load().Header.Get("Authorization"); got != w {
			t.Errorf("call %d Authorization = %q, want %q", i, got, w)
		}
	}
	if b.hits.Load() != int32(len(want)) {
		t.Errorf("backend saw %d requests, want %d", b.hits.Load(), len(want))
	}
	lines := auditLines(t, audit)
	if len(lines) != len(want) {
		t.Fatalf("audit = %+v, want one entry per call", lines)
	}
	for i, l := range lines {
		if l.Decision != connectorhost.DecisionSent || l.Status != 200 {
			t.Errorf("audit[%d] = %+v, want a sent 200", i, l)
		}
	}
	if strings.Contains(audit.String(), "token-") {
		t.Error("credential in the audit log")
	}
}

// Concurrent requests race a rotating file: each must carry one whole token.
func TestNetworkRequestsRacingARotationCarryAWholeToken(t *testing.T) {
	const workers, perWorker = 4, 25
	oldTok, newTok := strings.Repeat("o", 2048), strings.Repeat("n", 2048)
	file := filepath.Join(t.TempDir(), "token")
	rotate(t, file, oldTok)

	var mu sync.Mutex
	seen := map[string]int{}
	served := make(chan struct{})
	var once sync.Once
	b := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("Authorization")]++
		mu.Unlock()
		once.Do(func() { close(served) })
	})
	var audit bytes.Buffer
	n := netFor(b, &audit, connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
	followBearer("", file)(n)

	count := func(tok string) int {
		mu.Lock()
		defer mu.Unlock()
		return seen["Bearer "+tok]
	}

	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		// The file starts as oldTok, so waiting for a served request before
		// the first rotation guarantees both tokens are observed.
		select {
		case <-served:
		case <-stop:
			return
		}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				rotate(t, file, newTok)
			} else {
				rotate(t, file, oldTok)
			}
		}
	}()

	raw := []byte(fmt.Sprintf(`{"method":"GET","url":"%s/repos/x"}`, b.URL))
	deadline := time.Now().Add(10 * time.Second)
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker || count(newTok) == 0; i++ {
				if time.Now().After(deadline) {
					t.Error("no request carried the rotated token before the deadline")
					return
				}
				calls.Add(1)
				if status, _, err := n.Do(context.Background(), raw, 1<<10); err != nil || status != 200 {
					t.Errorf("Do = %d, %v: a request raced a rotation and failed", status, err)
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-done

	if got := b.hits.Load(); got != calls.Load() {
		t.Errorf("backend saw %d requests, want %d", got, calls.Load())
	}
	for auth, n := range seen {
		if auth != "Bearer "+oldTok && auth != "Bearer "+newTok {
			t.Errorf("%d requests carried a partial or foreign token of length %d", n, len(auth))
		}
	}
	if count(oldTok) == 0 || count(newTok) == 0 {
		t.Errorf("saw %d old and %d new requests, want both", count(oldTok), count(newTok))
	}
}

// With no refresh file the credential is the static one, and a refresh file
// that has not appeared yet falls back to it rather than sending nothing.
func TestNetworkFollowFallsBackToTheStaticToken(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	for name, path := range map[string]string{"no refresh file": "", "file not yet written": missing} {
		t.Run(name, func(t *testing.T) {
			b := newBackend(t, func(http.ResponseWriter, *http.Request) {})
			n := netFor(b, new(bytes.Buffer), connectorhost.Route{Method: "GET", Host: b.host(), Path: "/repos"})
			followBearer("s3cret", path)(n)
			raw := []byte(fmt.Sprintf(`{"method":"GET","url":"%s/repos/x"}`, b.URL))
			if status, _, err := n.Do(context.Background(), raw, 1<<10); err != nil || status != 200 {
				t.Fatalf("Do = %d, %v", status, err)
			}
			if got := b.last.Load().Header.Get("Authorization"); got != "Bearer s3cret" {
				t.Errorf("Authorization = %q, want the static token", got)
			}
		})
	}
}
