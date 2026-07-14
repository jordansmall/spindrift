package freshness

// FakeCall records one Eval invocation.
type FakeCall struct {
	Pwd, Rev, Attr string
}

// Fake is an in-memory Evaluator for unit tests — no nix round-trip.
type Fake struct {
	// OutPath is returned by Eval when Err is nil.
	OutPath string
	// Err, if non-nil, is returned by Eval instead of OutPath.
	Err error
	// Calls records the (pwd, rev, attr) tuples passed to Eval, in order.
	Calls []FakeCall
}

// Eval records the call and returns OutPath, or Err if set.
func (f *Fake) Eval(pwd, rev, attr string) (string, error) {
	f.Calls = append(f.Calls, FakeCall{pwd, rev, attr})
	if f.Err != nil {
		return "", f.Err
	}
	return f.OutPath, nil
}
