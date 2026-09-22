package signalsocket_test

import (
	"encoding/json"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/signalsocket"
	"spindrift.dev/launcher/internal/signalwire"
)

func allKinds() []signalwire.Kind {
	return []signalwire.Kind{
		signalwire.KindComment,
		signalwire.KindPRIntent,
		signalwire.KindIssueIntent,
	}
}

func newAll(t *testing.T) *signalsocket.Buffer {
	t.Helper()
	return signalsocket.New(signalsocket.Config{Consumes: allKinds()})
}

func mustAcceptComment(t *testing.T, b *signalsocket.Buffer, c signalwire.Comment) signalwire.Receipt {
	t.Helper()
	got, rej := b.AcceptComment(c)
	if rej != nil {
		t.Fatalf("AcceptComment(%+v): unexpected reject %+v", c, *rej)
	}
	return got
}

func mustAcceptPR(t *testing.T, b *signalsocket.Buffer, p signalwire.PRIntent) signalwire.Receipt {
	t.Helper()
	got, rej := b.AcceptPRIntent(p)
	if rej != nil {
		t.Fatalf("AcceptPRIntent(%+v): unexpected reject %+v", p, *rej)
	}
	return got
}

func mustAcceptIssue(t *testing.T, b *signalsocket.Buffer, i signalwire.IssueIntent) signalwire.Receipt {
	t.Helper()
	got, rej := b.AcceptIssueIntent(i)
	if rej != nil {
		t.Fatalf("AcceptIssueIntent(%+v): unexpected reject %+v", i, *rej)
	}
	return got
}

func mustReject(t *testing.T, rej *signalwire.Reject, wantStatus string, wantCode int) {
	t.Helper()
	if rej == nil {
		t.Fatalf("want reject %q, got accept", wantStatus)
	}
	if rej.Status != wantStatus || rej.Code != wantCode {
		t.Fatalf("reject = (%q, %d), want (%q, %d): %q", rej.Status, rej.Code, wantStatus, wantCode, rej.Reason)
	}
}

func TestCommentReplaces(t *testing.T) {
	b := newAll(t)
	first := mustAcceptComment(t, b, signalwire.Comment{Body: "one"})
	second := mustAcceptComment(t, b, signalwire.Comment{Body: "two"})

	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("sequences = %d, %d; want 1, 2", first.Sequence, second.Sequence)
	}
	if first.Hash == second.Hash {
		t.Fatalf("distinct bodies hashed alike: %q", first.Hash)
	}
	got, ok := b.Comment()
	if !ok || got.Body != "two" {
		t.Fatalf("Comment() = (%+v, %v), want body %q", got, ok, "two")
	}
	st := b.Status()
	if st.Comment == nil || st.Comment.Hash != second.Hash {
		t.Fatalf("Status().Comment = %+v, want hash %q", st.Comment, second.Hash)
	}
}

func TestPRIntentReplaces(t *testing.T) {
	b := newAll(t)
	mustAcceptPR(t, b, signalwire.PRIntent{Title: "a", Body: "b"})
	second := mustAcceptPR(t, b, signalwire.PRIntent{Title: "c", Body: "d"})

	got, ok := b.PRIntent()
	if !ok || got.Title != "c" || got.Body != "d" {
		t.Fatalf("PRIntent() = (%+v, %v), want {c d}", got, ok)
	}
	if second.Sequence != 2 {
		t.Fatalf("second sequence = %d, want 2", second.Sequence)
	}
	if second.Bytes != 2 {
		t.Fatalf("second bytes = %d, want 2", second.Bytes)
	}
}

func TestIssueIntentsAppendAndDedup(t *testing.T) {
	b := newAll(t)
	one := signalwire.IssueIntent{Title: "t1", Body: "b1", Type: "bug"}
	two := signalwire.IssueIntent{Title: "t2", Body: "b2", Type: "chore"}

	r1 := mustAcceptIssue(t, b, one)
	r2 := mustAcceptIssue(t, b, two)
	rDup := mustAcceptIssue(t, b, one)

	if got := b.IssueIntents(); len(got) != 2 || got[0] != one || got[1] != two {
		t.Fatalf("IssueIntents() = %+v, want [%+v %+v]", got, one, two)
	}
	if rDup != r1 {
		t.Fatalf("dup receipt = %+v, want the stored receipt %+v unchanged", rDup, r1)
	}
	if r2.Sequence != 2 {
		t.Fatalf("second sequence = %d, want 2", r2.Sequence)
	}
	st := b.Status()
	if len(st.IssueIntents) != 2 {
		t.Fatalf("Status().IssueIntents = %+v, want 2 entries", st.IssueIntents)
	}
	if st.IssueIntents[0].Sequence != r1.Sequence {
		t.Fatalf("Status().IssueIntents[0].Sequence = %d, want %d (no phantom gap)", st.IssueIntents[0].Sequence, r1.Sequence)
	}

	three := signalwire.IssueIntent{Title: "t3", Body: "b3", Type: "chore"}
	r3 := mustAcceptIssue(t, b, three)
	if r3.Sequence != 3 {
		t.Fatalf("distinct intent sequence = %d, want 3 (dup did not advance the counter)", r3.Sequence)
	}
	if got := b.IssueIntents(); len(got) != 3 || got[2] != three {
		t.Fatalf("IssueIntents() = %+v, want a third distinct intent appended", got)
	}
}

func TestSequenceIsBufferWide(t *testing.T) {
	b := newAll(t)
	c := mustAcceptComment(t, b, signalwire.Comment{Body: "x"})
	i := mustAcceptIssue(t, b, signalwire.IssueIntent{Title: "t", Body: "b", Type: "bug"})
	p := mustAcceptPR(t, b, signalwire.PRIntent{Title: "t", Body: "b"})

	if c.Sequence != 1 || i.Sequence != 2 || p.Sequence != 3 {
		t.Fatalf("sequences = %d, %d, %d; want 1, 2, 3", c.Sequence, i.Sequence, p.Sequence)
	}
	if c.Kind != signalwire.KindComment || i.Kind != signalwire.KindIssueIntent || p.Kind != signalwire.KindPRIntent {
		t.Fatalf("kinds = %q, %q, %q", c.Kind, i.Kind, p.Kind)
	}
}

func TestHashIsFieldFramed(t *testing.T) {
	b := newAll(t)
	split1 := mustAcceptPR(t, b, signalwire.PRIntent{Title: "ab", Body: "c"})
	split2 := mustAcceptPR(t, b, signalwire.PRIntent{Title: "a", Body: "bc"})
	if split1.Hash == split2.Hash {
		t.Fatalf("field-split collision: %q", split1.Hash)
	}
	if !strings.HasPrefix(split1.Hash, "sha256:") {
		t.Fatalf("hash = %q, want sha256: prefix", split1.Hash)
	}

	// The same bytes under a different kind must not collide either.
	c := mustAcceptComment(t, b, signalwire.Comment{Body: "abc"})
	if c.Hash == split1.Hash || c.Hash == split2.Hash {
		t.Fatalf("cross-kind collision: %q", c.Hash)
	}
}

func TestBytesCountsContentFields(t *testing.T) {
	b := newAll(t)
	r := mustAcceptIssue(t, b, signalwire.IssueIntent{Title: "ti", Body: "body", Type: "bug"})
	if want := len("ti") + len("body") + len("bug"); r.Bytes != want {
		t.Fatalf("bytes = %d, want %d", r.Bytes, want)
	}
}

func TestRejectKindNotConsumed(t *testing.T) {
	b := signalsocket.New(signalsocket.Config{Consumes: []signalwire.Kind{signalwire.KindIssueIntent}})

	_, rej := b.AcceptComment(signalwire.Comment{Body: "hi"})
	mustReject(t, rej, "kind_not_consumed", 409)
	_, rej = b.AcceptPRIntent(signalwire.PRIntent{Title: "t", Body: "b"})
	mustReject(t, rej, "kind_not_consumed", 409)

	// It beats every other check: an otherwise-invalid signal of an
	// unconsumed kind still reports the refusal, not the content fault.
	_, rej = b.AcceptComment(signalwire.Comment{Body: "   "})
	mustReject(t, rej, "kind_not_consumed", 409)
}

func TestRejectEmpty(t *testing.T) {
	b := newAll(t)
	_, rej := b.AcceptComment(signalwire.Comment{Body: " \t\n "})
	mustReject(t, rej, "empty", 400)
	_, rej = b.AcceptPRIntent(signalwire.PRIntent{Title: "t", Body: ""})
	mustReject(t, rej, "empty", 400)
	_, rej = b.AcceptPRIntent(signalwire.PRIntent{Title: "", Body: "b"})
	mustReject(t, rej, "empty", 400)
	_, rej = b.AcceptIssueIntent(signalwire.IssueIntent{Title: "t", Body: "b", Type: " "})
	mustReject(t, rej, "empty", 400)
	_, rej = b.AcceptIssueIntent(signalwire.IssueIntent{Title: "", Body: "b", Type: "bug"})
	mustReject(t, rej, "empty", 400)
}

func TestRejectInvalidUTF8(t *testing.T) {
	bad := string([]byte{0x41, 0xff, 0xfe})
	b := newAll(t)
	_, rej := b.AcceptComment(signalwire.Comment{Body: bad})
	mustReject(t, rej, "invalid_utf8", 400)
	_, rej = b.AcceptPRIntent(signalwire.PRIntent{Title: bad, Body: "b"})
	mustReject(t, rej, "invalid_utf8", 400)
	_, rej = b.AcceptIssueIntent(signalwire.IssueIntent{Title: "t", Body: "b", Type: bad})
	mustReject(t, rej, "invalid_utf8", 400)
}

func TestRejectOversize(t *testing.T) {
	big := strings.Repeat("x", signalwire.MaxBodyBytes+1)
	if signalwire.MaxBodyBytes != 64*1024 {
		t.Fatalf("MaxBodyBytes = %d, want 65536", signalwire.MaxBodyBytes)
	}
	b := newAll(t)
	_, rej := b.AcceptComment(signalwire.Comment{Body: big})
	mustReject(t, rej, "oversize", 413)
	_, rej = b.AcceptPRIntent(signalwire.PRIntent{Title: "t", Body: big})
	mustReject(t, rej, "oversize", 413)
	_, rej = b.AcceptIssueIntent(signalwire.IssueIntent{Title: "t", Body: big, Type: "bug"})
	mustReject(t, rej, "oversize", 413)

	// At the limit exactly is fine.
	atLimit := strings.Repeat("x", signalwire.MaxBodyBytes)
	mustAcceptComment(t, b, signalwire.Comment{Body: atLimit})
}

// Every content field is bounded, not just body: an oversize title is caught
// on its own, well under MaxRequestBytes.
func TestRejectOversizeTitle(t *testing.T) {
	big := strings.Repeat("x", signalwire.MaxBodyBytes+1)
	b := newAll(t)
	_, rej := b.AcceptPRIntent(signalwire.PRIntent{Title: big, Body: "b"})
	mustReject(t, rej, "oversize", 413)
	if !strings.HasPrefix(rej.Reason, "title ") {
		t.Fatalf("reason = %q, want it to name title", rej.Reason)
	}
}

// The UTF-8 pass inside validate has no HTTP-path caller to reach it -- decode
// rejects invalid UTF-8 first -- but it is reachable, and must stay correct,
// through the Buffer API directly.
func TestRejectInvalidUTF8DirectBufferCall(t *testing.T) {
	bad := string([]byte{0x41, 0xff, 0xfe})
	b := newAll(t)
	_, rej := b.AcceptPRIntent(signalwire.PRIntent{Title: "t", Body: bad})
	mustReject(t, rej, "invalid_utf8", 400)
	if !strings.HasPrefix(rej.Reason, "body ") {
		t.Fatalf("reason = %q, want it to name body", rej.Reason)
	}
}

func TestRejectInvalidType(t *testing.T) {
	b := newAll(t)
	_, rej := b.AcceptIssueIntent(signalwire.IssueIntent{Title: "t", Body: "b", Type: "regression"})
	mustReject(t, rej, "invalid_type", 400)

	for _, typ := range []string{"bug", "enhancement", "chore"} {
		mustAcceptIssue(t, b, signalwire.IssueIntent{Title: "t-" + typ, Body: "b", Type: typ})
	}
}

func TestRejectIntentCap(t *testing.T) {
	b := signalsocket.New(signalsocket.Config{Consumes: allKinds(), MaxIssueIntents: 2})
	first := signalwire.IssueIntent{Title: "t1", Body: "b", Type: "bug"}
	mustAcceptIssue(t, b, first)
	mustAcceptIssue(t, b, signalwire.IssueIntent{Title: "t2", Body: "b", Type: "bug"})

	_, rej := b.AcceptIssueIntent(signalwire.IssueIntent{Title: "t3", Body: "b", Type: "bug"})
	mustReject(t, rej, "intent_cap", 429)

	// A duplicate past the cap does not grow the list, so it stays idempotent,
	// and it returns the original's stored receipt rather than minting a
	// fresh sequence.
	dup := mustAcceptIssue(t, b, first)
	if len(b.IssueIntents()) != 2 {
		t.Fatalf("IssueIntents() = %+v, want 2 entries", b.IssueIntents())
	}
	if dup.Sequence != 1 {
		t.Fatalf("dup sequence = %d, want 1 (the stored receipt for first)", dup.Sequence)
	}
}

func TestCheckOrder(t *testing.T) {
	bad := string([]byte{0xff})
	big := strings.Repeat("x", signalwire.MaxBodyBytes+1)
	b := signalsocket.New(signalsocket.Config{Consumes: allKinds(), MaxIssueIntents: 1})
	mustAcceptIssue(t, b, signalwire.IssueIntent{Title: "t0", Body: "b", Type: "bug"})

	// empty beats invalid_utf8
	_, rej := b.AcceptIssueIntent(signalwire.IssueIntent{Title: "", Body: bad, Type: "bug"})
	mustReject(t, rej, "empty", 400)
	// invalid_utf8 beats oversize
	_, rej = b.AcceptIssueIntent(signalwire.IssueIntent{Title: bad, Body: big, Type: "bug"})
	mustReject(t, rej, "invalid_utf8", 400)
	// oversize beats invalid_type
	_, rej = b.AcceptIssueIntent(signalwire.IssueIntent{Title: "t", Body: big, Type: "nope"})
	mustReject(t, rej, "oversize", 413)
	// invalid_type beats the cap
	_, rej = b.AcceptIssueIntent(signalwire.IssueIntent{Title: "t", Body: "b", Type: "nope"})
	mustReject(t, rej, "invalid_type", 400)
}

func TestRejectReasonsAreOneQuietLine(t *testing.T) {
	bad := string([]byte{0xff})
	big := strings.Repeat("x", signalwire.MaxBodyBytes+1)
	unconsumed := signalsocket.New(signalsocket.Config{Consumes: nil})
	b := signalsocket.New(signalsocket.Config{Consumes: allKinds(), MaxIssueIntents: 1})
	mustAcceptIssue(t, b, signalwire.IssueIntent{Title: "t0", Body: "b", Type: "bug"})

	var rejects []*signalwire.Reject
	collect := func(_ signalwire.Receipt, rej *signalwire.Reject) {
		if rej == nil {
			t.Fatal("want reject, got accept")
		}
		rejects = append(rejects, rej)
	}
	collect(unconsumed.AcceptComment(signalwire.Comment{Body: "hi"}))
	collect(b.AcceptComment(signalwire.Comment{Body: " "}))
	collect(b.AcceptComment(signalwire.Comment{Body: bad}))
	collect(b.AcceptComment(signalwire.Comment{Body: big}))
	collect(b.AcceptIssueIntent(signalwire.IssueIntent{Title: "t", Body: "b", Type: "nope"}))
	collect(b.AcceptIssueIntent(signalwire.IssueIntent{Title: "t9", Body: "b", Type: "bug"}))

	seen := map[string]bool{}
	for _, rej := range rejects {
		if strings.ContainsAny(rej.Reason, "\n\r") {
			t.Errorf("reason %q spans lines", rej.Reason)
		}
		if rej.Reason != strings.ToLower(rej.Reason) {
			t.Errorf("reason %q is not lower-case", rej.Reason)
		}
		if strings.TrimSpace(rej.Reason) == "" {
			t.Errorf("empty reason for status %q", rej.Status)
		}
		if strings.ContainsAny(rej.Reason, "/#") {
			t.Errorf("reason %q names a path or issue", rej.Reason)
		}
		if seen[rej.Status] {
			t.Errorf("status %q reused across reject classes", rej.Status)
		}
		seen[rej.Status] = true
	}
	if len(seen) != 6 {
		t.Fatalf("distinct statuses = %d, want 6: %v", len(seen), seen)
	}
}

func TestStatusMarshalsQuietly(t *testing.T) {
	b := newAll(t)
	empty, err := json.Marshal(b.Status())
	if err != nil {
		t.Fatalf("marshal empty status: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(empty, &decoded); err != nil {
		t.Fatalf("unmarshal empty status: %v", err)
	}

	mustAcceptComment(t, b, signalwire.Comment{Body: "hello"})
	mustAcceptIssue(t, b, signalwire.IssueIntent{Title: "t", Body: "b", Type: "bug"})
	full, err := json.Marshal(b.Status())
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	// Status reports kinds and hashes, never the content itself.
	for _, leak := range []string{"hello", "\"title\"", "\"body\""} {
		if strings.Contains(string(full), leak) {
			t.Errorf("status %s leaks %s", full, leak)
		}
	}
	if !strings.Contains(string(full), "sha256:") {
		t.Errorf("status %s carries no hash", full)
	}
}

func TestReadAccessorsEmpty(t *testing.T) {
	b := newAll(t)
	if _, ok := b.Comment(); ok {
		t.Error("Comment() ok on empty buffer")
	}
	if _, ok := b.PRIntent(); ok {
		t.Error("PRIntent() ok on empty buffer")
	}
	if got := b.IssueIntents(); len(got) != 0 {
		t.Errorf("IssueIntents() = %+v, want empty", got)
	}
	st := b.Status()
	if st.Comment != nil || st.PRIntent != nil || len(st.IssueIntents) != 0 {
		t.Errorf("Status() = %+v, want zero", st)
	}
}

func TestDefaultIntentCapApplies(t *testing.T) {
	b := signalsocket.New(signalsocket.Config{Consumes: allKinds()})
	for n := range signalsocket.DefaultMaxIssueIntents {
		mustAcceptIssue(t, b, signalwire.IssueIntent{Title: string(rune('a' + n)), Body: "b", Type: "bug"})
	}
	_, rej := b.AcceptIssueIntent(signalwire.IssueIntent{Title: "over", Body: "b", Type: "bug"})
	mustReject(t, rej, "intent_cap", 429)
}

// wantReceiptDescribesContent checks the "receipt describes content" rule: a
// reject's Receipt still carries the kind, the summed byte length and a
// sha256: hash of values, but Sequence stays zero since nothing was buffered.
func wantReceiptDescribesContent(t *testing.T, got signalwire.Receipt, k signalwire.Kind, values ...string) {
	t.Helper()
	if got.Kind != k {
		t.Fatalf("Kind = %q, want %q", got.Kind, k)
	}
	sum := 0
	for _, v := range values {
		sum += len(v)
	}
	if got.Bytes != sum {
		t.Fatalf("Bytes = %d, want %d", got.Bytes, sum)
	}
	if !strings.HasPrefix(got.Hash, "sha256:") {
		t.Fatalf("Hash = %q, want sha256: prefix", got.Hash)
	}
	if got.Sequence != 0 {
		t.Fatalf("Sequence = %d, want 0", got.Sequence)
	}
}

func TestRejectReceiptOversize(t *testing.T) {
	body := strings.Repeat("x", signalwire.MaxBodyBytes+1)
	b := newAll(t)
	got, rej := b.AcceptComment(signalwire.Comment{Body: body})
	mustReject(t, rej, "oversize", 413)
	wantReceiptDescribesContent(t, got, signalwire.KindComment, body)
}

func TestRejectReceiptEveryClass(t *testing.T) {
	bad := string([]byte{0xff})

	t.Run("empty", func(t *testing.T) {
		b := newAll(t)
		got, rej := b.AcceptComment(signalwire.Comment{Body: " "})
		mustReject(t, rej, "empty", 400)
		wantReceiptDescribesContent(t, got, signalwire.KindComment, " ")
	})
	t.Run("invalid_utf8", func(t *testing.T) {
		b := newAll(t)
		got, rej := b.AcceptComment(signalwire.Comment{Body: bad})
		mustReject(t, rej, "invalid_utf8", 400)
		wantReceiptDescribesContent(t, got, signalwire.KindComment, bad)
	})
	t.Run("invalid_type", func(t *testing.T) {
		b := newAll(t)
		i := signalwire.IssueIntent{Title: "t", Body: "b", Type: "nope"}
		got, rej := b.AcceptIssueIntent(i)
		mustReject(t, rej, "invalid_type", 400)
		wantReceiptDescribesContent(t, got, signalwire.KindIssueIntent, i.Title, i.Body, i.Type)
	})
	t.Run("kind_not_consumed", func(t *testing.T) {
		b := signalsocket.New(signalsocket.Config{Consumes: []signalwire.Kind{signalwire.KindIssueIntent}})
		got, rej := b.AcceptComment(signalwire.Comment{Body: "hi"})
		mustReject(t, rej, "kind_not_consumed", 409)
		wantReceiptDescribesContent(t, got, signalwire.KindComment, "hi")
	})
	t.Run("intent_cap", func(t *testing.T) {
		b := signalsocket.New(signalsocket.Config{Consumes: allKinds(), MaxIssueIntents: 1})
		mustAcceptIssue(t, b, signalwire.IssueIntent{Title: "t1", Body: "b", Type: "bug"})
		i := signalwire.IssueIntent{Title: "t2", Body: "b", Type: "bug"}
		got, rej := b.AcceptIssueIntent(i)
		mustReject(t, rej, "intent_cap", 429)
		wantReceiptDescribesContent(t, got, signalwire.KindIssueIntent, i.Title, i.Body, i.Type)
	})
}

func TestRejectReceiptHashMatchesAccept(t *testing.T) {
	body := "same content"
	rejecting := signalsocket.New(signalsocket.Config{Consumes: []signalwire.Kind{signalwire.KindIssueIntent}})
	rejGot, rej := rejecting.AcceptComment(signalwire.Comment{Body: body})
	mustReject(t, rej, "kind_not_consumed", 409)

	accepting := newAll(t)
	accGot := mustAcceptComment(t, accepting, signalwire.Comment{Body: body})

	if rejGot.Hash != accGot.Hash {
		t.Fatalf("reject hash %q != accept hash %q", rejGot.Hash, accGot.Hash)
	}
	if rejGot.Bytes != accGot.Bytes {
		t.Fatalf("reject bytes %d != accept bytes %d", rejGot.Bytes, accGot.Bytes)
	}
}
