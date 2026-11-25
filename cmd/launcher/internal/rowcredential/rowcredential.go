// Package rowcredential resolves a backend row's credential: the one
// implementation the Launcher and the Daemon share (#7503). It is value-free
// like secretcmd; each caller supplies its runner and decides what an unset
// credential means and whether to pin the result.
package rowcredential

import (
	"errors"
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/credsource"
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
	// Lookup is the plain value for an env: the Launcher input's document, then
	// the process environment. nil: os.Getenv. Sources never writes the process
	// environment.
	Lookup func(env string) string
}

// refreshFileKnobs pairs a credential env with the knob naming the file that
// rotates it, read through Lookup. Once readable, the file wins over the value
// or command that sourced the env.
var refreshFileKnobs = map[string]string{"GH_TOKEN": "GH_TOKEN_REFRESH_FILE"}

// refreshFile is the file rotating env, "" for none.
func (r Resolution) refreshFile(env string) string {
	if knob := refreshFileKnobs[env]; knob != "" {
		return r.lookup(knob)
	}
	return ""
}

// Reads reports whether this run reads through row: it is the CODE_FORGE or
// ISSUE_TRACKER backend and that backend reads through its Connector.
func Reads(row backendrows.Row, forge, tracker string) bool {
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
		if !Reads(row, r.Forge, r.Tracker) || seen[env] {
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

func (r Resolution) lookup(env string) string {
	if r.Lookup != nil {
		return r.Lookup(env)
	}
	return os.Getenv(env)
}

// Sourced is the credential sources Sources chose.
type Sourced struct {
	// Rows holds one entry per backend row, keyed by row name. Rows sharing a
	// credential env share one Secret.
	Rows map[string]credsource.Row
	// Commands are the `_CMD` sources, for the periodic Refresh.
	Commands []*credsource.Command
}

// Refresh re-runs every command source. A failed re-run keeps the previous
// value and is passed to warn by env name.
func (s Sourced) Refresh(warn func(env string, err error)) {
	for _, c := range s.Commands {
		if err := c.Refresh(); err != nil {
			warn(c.Env(), err)
		}
	}
}

// Sources chooses the credential source of every row without touching the
// process environment. Of the rows this run reads, `_CMD` (which wins over a
// plain value) or else the SECRET_CMD template becomes a command source, else
// a plain value a static one. For an Applied env a set value is final and only
// the template may run. A command failure returns at once. An unset row does
// not: the rest still resolve and the error joins every unset row under
// ErrUnset, the row keeping an empty source. A refresh file (see
// refreshFileKnobs) wraps the chosen source, so a row with one is never unset.
// Rows this run does not read keep a plain-value source. It does not Pin; the
// caller decides.
func (r Resolution) Sources(rows []backendrows.Row) (Sourced, error) {
	out := Sourced{Rows: map[string]credsource.Row{}}
	byEnv := map[string]credsource.Source{}
	var unset []error
	choose := func(row backendrows.Row, read bool) error {
		env := row.CredentialEnv
		if _, ok := byEnv[env]; ok {
			return nil
		}
		var src credsource.Source
		if read {
			var cmd string
			plain := r.lookup(env)
			if r.Applied != nil && r.Applied(env) {
				// A set value is final; only an unset one may take the template.
				if plain == "" && r.Template != "" {
					cmd = secretcmd.Template(r.Template, env)
				}
			} else if cmd = r.lookup(env + "_CMD"); cmd == "" && plain == "" && r.Template != "" {
				cmd = secretcmd.Template(r.Template, env)
			}
			switch {
			case cmd != "":
				c, err := credsource.NewCommand(env, cmd, r.Run)
				if err != nil {
					return fmt.Errorf("the %s Connector row's credential comes from %s_CMD or the SECRET_CMD template: %w", row.Name, env, err)
				}
				out.Commands = append(out.Commands, c)
				src = c
			case plain != "" || r.refreshFile(env) != "":
				src = credsource.Static(plain)
			default:
				msg := fmt.Sprintf("the %s Connector row is read through its Connector this run but its credential is unset: set %s, %s_CMD, or SECRET_CMD", row.Name, env, env)
				if row.CredentialKnob != "" {
					msg += fmt.Sprintf(" (knob %s)", row.CredentialKnob)
				}
				unset = append(unset, unsetError(msg))
				src = credsource.Static("")
			}
		} else {
			src = credsource.Static(r.lookup(env))
		}
		if path := r.refreshFile(env); path != "" {
			src = credsource.File(path, src)
		}
		byEnv[env] = src
		return nil
	}
	// Read rows choose first so a shared env takes the command a read row runs.
	for _, read := range []bool{true, false} {
		for _, row := range rows {
			if reads(row, r.Forge, r.Tracker) != read {
				continue
			}
			if err := choose(row, read); err != nil {
				return out, err
			}
		}
	}
	for _, row := range rows {
		cr := credsource.Row{Secret: byEnv[row.CredentialEnv]}
		if row.IdentityEnv != "" {
			cr.Identity = credsource.Static(r.lookup(row.IdentityEnv))
		}
		out.Rows[row.Name] = cr
	}
	return out, errors.Join(unset...)
}
