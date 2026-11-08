package settle

// Settlement is what one Settle yields (CONTEXT.md): the lifecycle state it
// drove the Dispatch to and its Settle reason (the embedded Verdict), the PR it
// worked if any, and a note. The zero Settlement means the settle reached no
// decision (abandoned mid-settle, reclaimed, or an unclaimed issue left
// alone); its state is forge.Dispatchable and callers fold nothing for it.
type Settlement struct {
	Verdict
	PRURL, Note string
}
