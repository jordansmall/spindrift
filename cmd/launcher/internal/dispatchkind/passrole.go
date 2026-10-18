package dispatchkind

// The attribution roles a pass_start op maps to. These constants are the
// single source of truth; driverkit aliases them.
const (
	ImplementorRole = "implementor"
	ReviewerRole    = "reviewer"
)

// passRoles builds the table every Descriptor's PassRoles carries, one copy
// per row so a write through one kind's map cannot leak into another. The keys
// are passmachine's PassKind.String() literals, not its constants, because
// dispatchkind is a leaf (passmachine -> outcome -> dispatchkind would cycle).
func passRoles() map[string]string {
	return map[string]string{
		"review":       ReviewerRole,
		"delta-review": ReviewerRole,
		"implement":    ImplementorRole,
		"fix":          ImplementorRole,
		"land":         ImplementorRole,
	}
}
