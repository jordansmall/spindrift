package dispatchrecord

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestReasonTable(t *testing.T) {
	realStates := map[string]bool{StateComplete: true, StateFailed: true, StateAmbiguous: true, StateRecoverable: true}
	seen := map[string]bool{}
	for _, r := range reasons {
		if seen[r.Name] {
			t.Errorf("duplicate reason %q", r.Name)
		}
		seen[r.Name] = true
		if r.Gloss == "" {
			t.Errorf("reason %q has no gloss", r.Name)
		}
		if (r.State == AnyState) != (r.Name == ReasonTransitionRefused) {
			t.Errorf("reason %q has state %q; only %q may be %q", r.Name, r.State, ReasonTransitionRefused, AnyState)
		}
		if r.State != AnyState && !realStates[r.State] {
			t.Errorf("reason %q has unknown state %q", r.Name, r.State)
		}
		if (r.LeavesPROpen || r.Landed) && r.State != StateComplete {
			t.Errorf("reason %q is %q but marks a landing fact", r.Name, r.State)
		}
		if r.Delivery != Delivers && r.State != StateComplete {
			t.Errorf("reason %q is %q but sets a delivery", r.Name, r.State)
		}
		if got := ReasonDelivery(r.Name); got != r.Delivery {
			t.Errorf("ReasonDelivery(%q) = %v, want %v", r.Name, got, r.Delivery)
		}
		if got := ReasonLeavesPROpen(r.Name); got != r.LeavesPROpen {
			t.Errorf("ReasonLeavesPROpen(%q) = %v, want %v", r.Name, got, r.LeavesPROpen)
		}
		if got := ReasonLanded(r.Name); got != r.Landed {
			t.Errorf("ReasonLanded(%q) = %v, want %v", r.Name, got, r.Landed)
		}
		// Stats' merged count and settle's merge-commit reads both ask
		// Landed, so a second landed reason would change them silently.
		if r.Landed != (r.Name == ReasonMerged) {
			t.Errorf("reason %q: Landed = %v; only %q lands", r.Name, r.Landed, ReasonMerged)
		}
	}
	if ReasonLeavesPROpen("no-such-reason") || ReasonLanded("no-such-reason") {
		t.Error("an unknown reason must answer false")
	}
	if ReasonDelivery("no-such-reason") != Delivers || ReasonDelivery("") != Delivers {
		t.Error("an unknown reason must deliver")
	}
	if ReasonDelivery(ReasonAlreadyResolved) != DeliversNothing || ReasonDelivery(ReasonMergeBlocked) != DeliversWithPR {
		t.Error("already-resolved delivers nothing; merge-blocked delivers only with a PR")
	}
}

var backticked = regexp.MustCompile("`([^`]+)`")

// stripParens drops text inside parentheses at any nesting depth.
func stripParens(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// docsReasons parses the settled-outcome reason list in docs/reference.md
// into reason -> state.
func docsReasons(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("../../../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "`reason` is the class of the decision:") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("docs/reference.md: the reason list intro is gone; update this parser")
	}
	var bullets []string
	for _, l := range lines[start+1:] {
		if strings.HasPrefix(l, "- ") {
			bullets = append(bullets, l[2:])
		} else if strings.HasPrefix(l, "  ") && len(bullets) > 0 {
			bullets[len(bullets)-1] += " " + strings.TrimSpace(l)
		} else if len(bullets) > 0 || strings.TrimSpace(l) != "" {
			break
		}
	}
	got := map[string]string{}
	perBullet := map[string]int{}
	for _, bl := range bullets {
		bl = stripParens(bl)
		var toks []string
		for _, m := range backticked.FindAllStringSubmatch(bl, -1) {
			toks = append(toks, m[1])
		}
		if len(toks) == 0 {
			t.Fatalf("docs bullet has no backticked token: %q", bl)
		}
		switch toks[0] {
		case StateComplete, StateFailed:
			for _, n := range toks[1:] {
				got[n] = toks[0]
				perBullet[toks[0]]++
			}
		case StateAmbiguous:
			for _, n := range toks {
				got[n] = n
				perBullet[StateAmbiguous]++
			}
		case ReasonTransitionRefused:
			got[toks[0]] = AnyState
			perBullet[ReasonTransitionRefused]++
		default:
			t.Fatalf("docs bullet starts with unrecognised token %q", toks[0])
		}
	}
	for _, k := range []string{StateComplete, StateFailed, StateAmbiguous, ReasonTransitionRefused} {
		if perBullet[k] == 0 {
			t.Fatalf("docs bullet %q parsed no reasons; the list was restructured", k)
		}
	}
	return got
}

func TestReasonsMatchDocs(t *testing.T) {
	want := docsReasons(t)
	have := map[string]string{}
	for _, r := range reasons {
		have[r.Name] = r.State
	}
	var diffs []string
	for n, s := range want {
		hs, ok := have[n]
		switch {
		case !ok:
			diffs = append(diffs, fmt.Sprintf("documented but not in table: %s (%s)", n, s))
		case hs != s:
			diffs = append(diffs, fmt.Sprintf("state mismatch for %s: docs %s, table %s", n, s, hs))
		}
	}
	for n, s := range have {
		if _, ok := want[n]; !ok {
			diffs = append(diffs, fmt.Sprintf("in table but not documented: %s (%s)", n, s))
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Errorf("docs/reference.md and the reasons table disagree:\n%s", strings.Join(diffs, "\n"))
	}
}

// Reads the Reason* consts from source, so a new one without a reasons row
// fails here instead of settling a reason no consumer can look up.
func TestReasonConstsHaveRows(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "reason.go", nil, 0)
	if err != nil {
		t.Fatalf("parse reason.go: %v", err)
	}
	rows := map[string]bool{}
	for _, r := range reasons {
		rows[r.Name] = true
	}
	n := 0
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Reason") {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				n++
				if v, _ := strconv.Unquote(lit.Value); !rows[v] {
					t.Errorf("%s = %s has no row in reasons", name.Name, lit.Value)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("found no Reason* consts; reason.go was restructured")
	}
}
