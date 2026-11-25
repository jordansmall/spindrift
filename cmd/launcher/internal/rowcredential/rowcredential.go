// Package rowcredential resolves a backend row's credential: the one
// implementation the Launcher and the Daemon share (#7503). It is value-free
// like secretcmd; each caller supplies its runner and decides what an unset
// credential means and whether to pin the result.
package rowcredential

import (
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/secretcmd"
)

// ErrUnset marks a row this run reads whose credential stays unset. The Daemon
// refuses it; the Launcher tolerates it, since doctor must run without one.
var ErrUnset = errors.New("credential is unset")

// unsetError carries the message without an ErrUnset suffix.
type unsetError string

func (e unsetError) Error() string        { return string(e) }
func (e unsetError) Is(target error) bool { return target == ErrUnset }

// Recipe is the command a credential was resolved from; a refresh re-runs it.
type Recipe struct{ Env, Cmd string }

// Resolution is one run's inputs to resolving row credentials.
type Resolution struct {
	// Forge and Tracker are this run's CODE_FORGE and ISSUE_TRACKER.
	Forge, Tracker string
	// Template is SECRET_CMD, "" for none.
	Template string
	Run      func(cmd string) (string, error)
	// Applied reports an env whose `<env>_CMD`/--*-cmd/--*-file forms the caller
	// already applied this run (the Launcher's parseFlags, for every core
	// secret), so Apply takes its value as final and never runs its `_CMD`
	// again. nil: none.
	Applied func(env string) bool
}

// reads reports whether this run reads through row: it is the CODE_FORGE or
// ISSUE_TRACKER backend and that backend reads through its Connector.
func reads(row backendrows.Row, forge, tracker string) bool {
	return (row.Name == forge && backendkit.ForgeReadsThroughConnector(row.Name)) ||
		(row.Name == tracker && backendkit.TrackerReadsThroughConnector(row.Name))
}

// Apply fills the credential env of each row this run reads, from `_CMD`
// (which wins over a plain value) or else the SECRET_CMD template, and returns
// the recipes that sourced them. For an Applied env a set value is final and
// only the template may run. A command failure returns at once. An unset
// row does not: the rest still resolve and the error joins every unset row
// under ErrUnset. It does not Pin; the caller decides.
func (r Resolution) Apply(rows []backendrows.Row) ([]Recipe, error) {
	var recipes []Recipe
	var unset []error
	seen := map[string]bool{}
	for _, row := range rows {
		env := row.CredentialEnv
		if !reads(row, r.Forge, r.Tracker) || seen[env] {
			continue
		}
		seen[env] = true
		var cmd string
		if r.Applied != nil && r.Applied(env) {
			// A set value is final; only an unset one may take the template.
			if !secretcmd.IsSet(env) && r.Template != "" {
				cmd = secretcmd.Template(r.Template, env)
			}
		} else {
			cmd = secretcmd.Command(env, r.Template)
		}
		if cmd != "" {
			if err := secretcmd.Export(env, cmd, r.Run); err != nil {
				return recipes, fmt.Errorf("the %s Connector row's credential comes from %s_CMD or the SECRET_CMD template: %w", row.Name, env, err)
			}
			recipes = append(recipes, Recipe{env, cmd})
			continue
		}
		if secretcmd.IsSet(env) {
			continue
		}
		msg := fmt.Sprintf("the %s Connector row is read through its Connector this run but its credential is unset: set %s, %s_CMD, or SECRET_CMD", row.Name, env, env)
		if row.CredentialKnob != "" {
			msg += fmt.Sprintf(" (knob %s)", row.CredentialKnob)
		}
		unset = append(unset, unsetError(msg))
	}
	return recipes, errors.Join(unset...)
}

// Refresh re-runs each recipe through r.Run. A failed re-run keeps the previous
// value and is passed to warn.
func (r Resolution) Refresh(recipes []Recipe, warn func(env string, err error)) {
	for _, rc := range recipes {
		if err := secretcmd.Export(rc.Env, rc.Cmd, r.Run); err != nil {
			warn(rc.Env, err)
		}
	}
}
