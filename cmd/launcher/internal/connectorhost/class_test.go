package connectorhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// scriptedEngine answers every call with one fixed response.
type scriptedEngine struct{ resp []byte }

func (e scriptedEngine) handshake(context.Context) ([]byte, error)    { return nil, nil }
func (e scriptedEngine) call(context.Context, []byte) ([]byte, error) { return e.resp, nil }
func (scriptedEngine) abort()                                         {}
func (scriptedEngine) close(context.Context) error                    { return nil }

// A hostile Connector's error text is capped and stripped before core prints,
// logs or posts it, and the class it reported still reads.
func TestCallErrorTextIsCappedAndSanitised(t *testing.T) {
	hostile := "\x1b[31mred\x1b[0m \x1b]0;title\x07line\r\nnul\x00rlo\u202etail" + strings.Repeat("A", 1<<20)
	resp, err := json.Marshal(map[string]string{"error": hostile, "class": "auth"})
	if err != nil {
		t.Fatal(err)
	}
	c := &Connector{engine: scriptedEngine{resp}, now: time.Now}

	err = c.Call(context.Background(), "issue.get", struct{}{}, nil)

	if got := ErrorClass(err); got != Auth {
		t.Errorf("ErrorClass = %q, want %q", got, Auth)
	}
	var ce *CallError
	if !errors.As(err, &ce) {
		t.Fatalf("no *CallError in %v", err)
	}
	if len(ce.Message) > maxErrorText {
		t.Errorf("message is %d bytes, cap %d", len(ce.Message), maxErrorText)
	}
	if !strings.HasSuffix(ce.Message, driverkit.CapMarker) {
		t.Errorf("message %q lacks the truncation marker", ce.Message)
	}
	text := err.Error()
	if strings.ContainsAny(text, "\x1b\r\x00\u202e\x07") {
		t.Errorf("error text keeps a control character: %q", text)
	}
	// 200 of slack covers the wrap prefix core adds around the capped message.
	if len(text) > maxErrorText+200 {
		t.Errorf("error text is %d bytes", len(text))
	}
}

// A guest string a Driver-call error embeds is capped too.
func TestPatternsErrorCapsGuestStrings(t *testing.T) {
	_, err := patterns([]wirePattern{{Substr: strings.Repeat("x", 1<<16), Reason: "\x1b[31m" + strings.Repeat("y", 1<<16)}})
	if !errors.Is(err, ErrCall) {
		t.Fatalf("err = %v, want ErrCall", err)
	}
	// 4x covers two capped fields plus the format text.
	if got := err.Error(); len(got) > 4*maxErrorText || strings.Contains(got, "\x1b") {
		t.Errorf("error is %d bytes or keeps ESC: %q", len(got), got)
	}
}
