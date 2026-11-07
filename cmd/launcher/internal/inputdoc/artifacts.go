package inputdoc

import (
	"encoding/json"
	"fmt"
	"maps"
)

// Value is one artifact as the document carries it: a string whose presence
// is tracked apart from its content, because the launcher's trust-gate
// fallbacks test whether a key was rendered, not what it says.
type Value struct {
	s   string
	set bool
}

// ValueOf returns a set Value holding s.
func ValueOf(s string) Value { return Value{s: s, set: true} }

// String returns the value, or "" when the key was absent.
func (v Value) String() string { return v.s }

// Lookup returns the value and whether the document carried the key.
func (v Value) Lookup() (string, bool) { return v.s, v.set }

// IsZero reports an absent key; encoding/json's omitzero uses it to leave
// unset keys out of a marshalled document.
func (v Value) IsZero() bool { return !v.set }

func (v *Value) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &v.s); err != nil {
		return err
	}
	v.set = true
	return nil
}

func (v Value) MarshalJSON() ([]byte, error) { return json.Marshal(v.s) }

// Flag is a Value Nix renders as the literal "true" or "false".
type Flag struct{ Value }

// FlagOf returns a set Flag holding b.
func FlagOf(b bool) Flag {
	if b {
		return Flag{ValueOf("true")}
	}
	return Flag{ValueOf("false")}
}

// Bool reports whether the key is the literal "true"; an absent key is false.
func (f Flag) Bool() bool { return f.s == "true" }

// LookupBool is Bool plus whether the document carried the key.
func (f Flag) LookupBool() (bool, bool) { return f.Bool(), f.set }

// UnmarshalJSON decodes the shared part, then the one family part the
// document's RUNNER_KIND names. A key some family carries but neither the
// shared part nor RUNNER_KIND's family does is an error, never a silent drop:
// it would otherwise read as empty. Keys no family carries are skipped, so a
// newer document still loads.
func (a *Artifacts) UnmarshalJSON(b []byte) error {
	*a = Artifacts{}
	if err := json.Unmarshal(b, &a.Shared); err != nil {
		return err
	}
	kind := a.Shared.RunnerKind.String()
	if err := rejectForeignKeys(b, kind); err != nil {
		return err
	}
	part := a.newFamilyPart(kind)
	if part == nil {
		return nil
	}
	return json.Unmarshal(b, part)
}

func rejectForeignKeys(b []byte, kind string) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	carried := map[string]bool{}
	for _, k := range SharedArtifactKeys {
		carried[k] = true
	}
	for _, k := range FamilyArtifactKeys[kind] {
		carried[k] = true
	}
	for _, keys := range FamilyArtifactKeys {
		for _, k := range keys {
			if _, ok := raw[k]; ok && !carried[k] {
				return fmt.Errorf("artifact %q is not carried by RUNNER_KIND %q", k, kind)
			}
		}
	}
	return nil
}

// MarshalJSON merges the shared part and the set family part into the flat
// object the document carries.
func (a Artifacts) MarshalJSON() ([]byte, error) {
	merged := map[string]json.RawMessage{}
	parts := []any{a.Shared}
	if p := a.familyPart(); p != nil {
		parts = append(parts, p)
	}
	for _, p := range parts {
		b, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		maps.Copy(merged, m)
	}
	return json.Marshal(merged)
}
