package connectorhost

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// The generated protocol description (lib/connector-protocol) is an agreement
// target, not a source: core's hand-written constants stay literal because the
// SDK's parity tests evaluate them. These tests fail when either side moves.

func TestProtocolVersionAgrees(t *testing.T) {
	if connectorprotocol.Version != ProtocolVersion {
		t.Errorf("connectorprotocol.Version = %q, ProtocolVersion = %q", connectorprotocol.Version, ProtocolVersion)
	}
}

func TestProtocolClassesAgree(t *testing.T) {
	want := []string{
		string(Transient), string(RateLimited), string(NotFound),
		string(Auth), string(NotMergeable), string(Permanent),
	}
	if !slices.Equal(connectorprotocol.Classes, want) {
		t.Errorf("connectorprotocol.Classes = %v, class.go declares %v", connectorprotocol.Classes, want)
	}
}

func TestProtocolLimitsAgree(t *testing.T) {
	secs := func(d time.Duration) int { return int(d / time.Second) }
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"MaxHTTPResponse", connectorprotocol.MaxHTTPResponse, maxHTTPResponse},
		{"MaxHTTPURL", connectorprotocol.MaxHTTPURL, maxHTTPURL},
		{"MaxHeaderName", connectorprotocol.MaxHeaderName, maxHeaderName},
		{"MaxHTTPRequests", connectorprotocol.MaxHTTPRequests, maxHTTPRequests},
		{"MaxHTTPTimeSeconds", connectorprotocol.MaxHTTPTimeSeconds, secs(maxHTTPTime)},
		{"MaxHTTPRequestSeconds", connectorprotocol.MaxHTTPRequestSeconds, secs(httpRequestTimeout)},
		{"MaxRequest", connectorprotocol.MaxRequest, maxRequest},
		{"MaxOutput", connectorprotocol.MaxOutput, maxOutput},
		{"MemoryCapBytes", connectorprotocol.MemoryCapBytes, memoryCapBytes},
		{"MemoryPerRequestByte", connectorprotocol.MemoryPerRequestByte, memoryPerRequestByte},
		{"OutputPerRequestByte", connectorprotocol.OutputPerRequestByte, outputPerRequestByte},
		{"CallBudgetSeconds", connectorprotocol.CallBudgetSeconds, secs(callBudget)},
		{"InitBudgetSeconds", connectorprotocol.InitBudgetSeconds, secs(initBudget)},
		{"BudgetPerMiBSeconds", connectorprotocol.BudgetPerMiBSeconds, secs(budgetPerMiB)},
		{"MaxResponseCredit", connectorprotocol.MaxResponseCredit, maxResponseCredit},
		{"MaxTableEntries", connectorprotocol.MaxTableEntries, maxTableEntries},
		{"MaxFrameSlots", connectorprotocol.MaxFrameSlots, maxFrameSlots},
		{"MaxModuleLocals", connectorprotocol.MaxModuleLocals, maxModuleLocals},
		{"MaxErrorText", connectorprotocol.MaxErrorText, maxErrorText},
	} {
		if c.got != c.want {
			t.Errorf("connectorprotocol.%s = %d, core's constant is %d", c.name, c.got, c.want)
		}
	}
}

func TestProtocolEventKindsAgree(t *testing.T) {
	canonical := []string{
		string(driverkit.KindStart), string(driverkit.KindText), string(driverkit.KindReasoning),
		string(driverkit.KindToolCall), string(driverkit.KindToolResult), string(driverkit.KindUsage),
		string(driverkit.KindModel), string(driverkit.KindError), string(driverkit.KindUnparsed),
		string(driverkit.KindResult), string(driverkit.KindHostOp),
	}
	if !slices.Equal(connectorprotocol.EventKinds, canonical) {
		t.Errorf("connectorprotocol.EventKinds = %v, driverkit declares %v", connectorprotocol.EventKinds, canonical)
	}
	var guest []string
	for _, k := range guestKinds {
		guest = append(guest, string(k))
	}
	if !slices.Equal(connectorprotocol.GuestEventKinds, guest) {
		t.Errorf("connectorprotocol.GuestEventKinds = %v, driver.go's guestKinds is %v", connectorprotocol.GuestEventKinds, guest)
	}
}

// driverNames returns every string literal in driver.go that names a driver
// method or capability ("driver" or "driver.<x>"), and the capabilities the
// Strategy builder checks through has().
func driverNames(t *testing.T) (all, checked []string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "driver.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`^driver(\.[A-Za-z]+)?$`)
	lit := func(n ast.Node) (string, bool) {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(bl.Value)
		return s, err == nil && name.MatchString(s)
	}
	// has(decodeBatchMethod) names a capability through a const, so resolve the
	// file's string consts.
	consts := map[string]string{}
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				if s, ok := lit(vs.Values[0]); ok {
					consts[vs.Names[0].Name] = s
				}
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if s, ok := lit(n); ok && !slices.Contains(all, s) {
			all = append(all, s)
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "has" && len(call.Args) == 1 {
				s, ok := lit(call.Args[0])
				if id, isID := call.Args[0].(*ast.Ident); isID {
					s, ok = consts[id.Name]
				}
				if ok {
					checked = append(checked, s)
				}
			}
		}
		return true
	})
	slices.Sort(all)
	return all, checked
}

// TestProtocolDriverAxisAgrees pins the description's driver methods and
// capabilities to the names driver.go calls and checks.
func TestProtocolDriverAxisAgrees(t *testing.T) {
	var want []string
	driverCaps := map[string]bool{}
	for name, axis := range connectorprotocol.Capabilities {
		if axis == connectorprotocol.AxisDriver {
			want = append(want, name)
			driverCaps[name] = true
		}
	}
	for name, m := range connectorprotocol.Methods {
		if m.Axis != connectorprotocol.AxisDriver {
			continue
		}
		if !slices.Contains(want, name) {
			want = append(want, name)
		}
		if m.Capability != "" && !driverCaps[m.Capability] {
			t.Errorf("method %s names capability %q, which is not a driver capability", name, m.Capability)
		}
	}
	slices.Sort(want)
	all, checked := driverNames(t)
	if !slices.Equal(all, want) {
		t.Errorf("driver.go names %v, the description declares %v", all, want)
	}
	for _, c := range checked {
		if !driverCaps[c] {
			t.Errorf("driver.go checks capability %q, which the description does not declare", c)
		}
	}
	// A method gated on a capability core checks must have that check.
	for name, m := range connectorprotocol.Methods {
		if m.Axis == connectorprotocol.AxisDriver && m.Capability != "" && m.Capability != "driver" &&
			!slices.Contains(checked, m.Capability) {
			t.Errorf("method %s needs capability %q, which driver.go never checks", name, m.Capability)
		}
	}
}
