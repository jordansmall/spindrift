// Package signalsocket holds the in-memory buffer behind the Box's Signal
// socket (ADR 0052, issue #3724).
//
// Three rules shape every type here. The kind is the route, so no request
// field can select one: signalwire.Kind exists to tag a receipt, never to be
// parsed off the wire. Content is never destination — a signal carries only
// prose, so an issue number, label, branch or target is unrepresentable rather
// than merely rejected. And the buffer never acts: an accept stores the signal
// in memory and nothing else, with the comment posted, PR opened, or issue
// filed at settle, from the host, on the host's terms.
//
// A signalwire.Receipt describes the content — Kind, Bytes, Hash — while a
// Reject describes the verdict; the two are independent, so every Accept*
// method returns both on a reject, computed the same way an accept would.
// Sequence is the one field that means "buffered": it stays zero on a
// reject, since nothing was stored.
package signalsocket

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/signalwire"
)

// DefaultMaxIssueIntents caps distinct issue intents per run when Config
// leaves MaxIssueIntents zero.
const DefaultMaxIssueIntents = 8

// Config configures a Buffer.
type Config struct {
	// Consumes is the set of kinds this Dispatch will actually consume at
	// settle. A kind outside it is refused on receipt rather than accepted
	// and silently dropped later.
	Consumes []signalwire.Kind
	// MaxIssueIntents caps distinct issue intents per run. Zero means
	// DefaultMaxIssueIntents.
	MaxIssueIntents int
}

// Buffer holds one run's accepted signals. It is safe for concurrent use: the
// handler serves requests concurrently.
type Buffer struct {
	consumes   []signalwire.Kind
	maxIntents int

	mu       sync.Mutex
	seq      int
	comment  *storedComment
	prIntent *storedPRIntent
	intents  []storedIssueIntent
}

type storedComment struct {
	content signalwire.Comment
	receipt signalwire.Receipt
}

type storedPRIntent struct {
	content signalwire.PRIntent
	receipt signalwire.Receipt
}

type storedIssueIntent struct {
	content signalwire.IssueIntent
	receipt signalwire.Receipt
}

// New returns an empty Buffer for cfg.
func New(cfg Config) *Buffer {
	maxIntents := cfg.MaxIssueIntents
	if maxIntents <= 0 {
		maxIntents = DefaultMaxIssueIntents
	}
	return &Buffer{consumes: slices.Clone(cfg.Consumes), maxIntents: maxIntents}
}

// AcceptComment validates c and replaces any comment already buffered.
func (b *Buffer) AcceptComment(c signalwire.Comment) (signalwire.Receipt, *signalwire.Reject) {
	partial := signalwire.Receipt{
		Kind:  signalwire.KindComment,
		Bytes: len(c.Body),
		Hash:  contentHash(signalwire.KindComment, c.Body),
	}
	if rej := b.checkKind(signalwire.KindComment); rej != nil {
		return partial, rej
	}
	if rej := signalwire.CheckFields(signalwire.Field{Name: "body", Value: c.Body}); rej != nil {
		return partial, rej
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.receipt(partial.Kind, partial.Bytes, partial.Hash)
	b.comment = &storedComment{content: c, receipt: r}
	return r, nil
}

// AcceptPRIntent validates p and replaces any PR intent already buffered.
func (b *Buffer) AcceptPRIntent(p signalwire.PRIntent) (signalwire.Receipt, *signalwire.Reject) {
	partial := signalwire.Receipt{
		Kind:  signalwire.KindPRIntent,
		Bytes: len(p.Title) + len(p.Body),
		Hash:  contentHash(signalwire.KindPRIntent, p.Title, p.Body),
	}
	if rej := b.checkKind(signalwire.KindPRIntent); rej != nil {
		return partial, rej
	}
	if rej := signalwire.CheckFields(
		signalwire.Field{Name: "title", Value: p.Title},
		signalwire.Field{Name: "body", Value: p.Body},
	); rej != nil {
		return partial, rej
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.receipt(partial.Kind, partial.Bytes, partial.Hash)
	b.prIntent = &storedPRIntent{content: p, receipt: r}
	return r, nil
}

// AcceptIssueIntent validates i and appends it. A byte-identical repeat is
// accepted idempotently: it returns the stored receipt unchanged, sequence
// included, and does not grow the list, so a Box that retries a signal it
// never saw the receipt for is not punished with a duplicate issue -- nor
// with a cap it did not actually consume -- and Status() never lists a
// sequence the repeat's caller was told about but the buffer does not have.
func (b *Buffer) AcceptIssueIntent(i signalwire.IssueIntent) (signalwire.Receipt, *signalwire.Reject) {
	// Filing is best-effort: a reject here loses the finding outright, while
	// the relay path (settle/dedup.go's splitDedupTerms/buildDedupMarker)
	// just drops a blank term and files normally. The two carriers must not
	// disagree about whether a blank term is fatal, so drop whitespace-only
	// terms up front -- before Bytes/Hash and before i.ValidateTypeRequired()
	// -- and carry only the survivors into the stored intent. A non-blank
	// term keeps every check it has today (UTF-8, oversize).
	i = i.PruneBlankDedupTerms()

	total := len(i.Title) + len(i.Body) + len(i.Type) + len(i.Class) + len(i.Concurrence) + len(i.Patch)
	for _, term := range i.DedupTerms {
		total += len(term)
	}
	partial := signalwire.Receipt{
		Kind:  signalwire.KindIssueIntent,
		Bytes: total,
		Hash:  contentHash(signalwire.KindIssueIntent, append([]string{i.Title, i.Body, i.Type, i.Class, i.Concurrence, i.Patch}, i.DedupTerms...)...),
	}
	if rej := b.checkKind(signalwire.KindIssueIntent); rej != nil {
		return partial, rej
	}
	if rej := i.ValidateTypeRequired(); rej != nil {
		return partial, rej
	}
	if _, ok := doctor.FindingTypeLabels[i.Type]; !ok {
		return partial, &signalwire.Reject{
			Status: "invalid_type",
			Reason: "type is not one of the supported issue-intent types",
			Code:   http.StatusBadRequest,
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	h := partial.Hash
	if idx := slices.IndexFunc(b.intents, func(s storedIssueIntent) bool { return s.receipt.Hash == h }); idx >= 0 {
		return b.intents[idx].receipt, nil
	}
	if len(b.intents) >= b.maxIntents {
		return partial, &signalwire.Reject{
			Status: "intent_cap",
			Reason: fmt.Sprintf("this dispatch accepts at most %d issue intents", b.maxIntents),
			Code:   http.StatusTooManyRequests,
		}
	}
	r := b.receipt(signalwire.KindIssueIntent, partial.Bytes, h)
	b.intents = append(b.intents, storedIssueIntent{content: i, receipt: r})
	return r, nil
}

// Comment returns the buffered comment, if any.
func (b *Buffer) Comment() (signalwire.Comment, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.comment == nil {
		return signalwire.Comment{}, false
	}
	return b.comment.content, true
}

// PRIntent returns the buffered PR intent, if any.
func (b *Buffer) PRIntent() (signalwire.PRIntent, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.prIntent == nil {
		return signalwire.PRIntent{}, false
	}
	return b.prIntent.content, true
}

// IssueIntents returns the buffered issue intents in accept order.
func (b *Buffer) IssueIntents() []signalwire.IssueIntent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]signalwire.IssueIntent, 0, len(b.intents))
	for _, s := range b.intents {
		out = append(out, s.content)
	}
	return out
}

// Status reports the kinds and hashes accepted so far.
func (b *Buffer) Status() signalwire.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	var st signalwire.Status
	if b.comment != nil {
		r := b.comment.receipt
		st.Comment = &r
	}
	if b.prIntent != nil {
		r := b.prIntent.receipt
		st.PRIntent = &r
	}
	for _, s := range b.intents {
		st.IssueIntents = append(st.IssueIntents, s.receipt)
	}
	return st
}

// receipt stamps the next buffer-wide sequence number. Callers hold b.mu.
func (b *Buffer) receipt(k signalwire.Kind, bytes int, h string) signalwire.Receipt {
	b.seq++
	return signalwire.Receipt{Kind: k, Bytes: bytes, Hash: h, Sequence: b.seq}
}

func (b *Buffer) checkKind(k signalwire.Kind) *signalwire.Reject {
	if slices.Contains(b.consumes, k) {
		return nil
	}
	return &signalwire.Reject{
		Status: "kind_not_consumed",
		Reason: "this dispatch does not consume " + string(k) + " signals",
		Code:   http.StatusConflict,
	}
}

// contentHash content-hashes a signal, domain-separated by kind and
// length-framed per field, so neither two kinds nor two ways of splitting the
// same bytes across fields can collide.
func contentHash(k signalwire.Kind, values ...string) string {
	h := sha256.New()
	writeFramed(h, string(k))
	for _, v := range values {
		writeFramed(h, v)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func writeFramed(h io.Writer, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = h.Write(n[:])
	_, _ = h.Write([]byte(s))
}
