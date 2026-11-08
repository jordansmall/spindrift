package inputdoc

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrConfigInvalid marks an error the launcher reports with exitcode.ConfigInvalid.
var ErrConfigInvalid = errors.New("config-invalid")

// configInvalidError keeps its own message while matching ErrConfigInvalid.
type configInvalidError struct{ msg string }

func (e configInvalidError) Error() string { return e.msg }
func (e configInvalidError) Unwrap() error { return ErrConfigInvalid }

// AmbientKnobError rejects every non-secret schema knob present in env (ADR
// 0020: the knob env var is gone, the document and flags carry the value).
// Secrets and non-knob plumbing are not SchemaFlags rows, so they pass.
// The error matches ErrConfigInvalid.
func AmbientKnobError(env map[string]string) error {
	var lines []string
	for _, e := range SchemaFlags {
		v, ok := env[e.Env]
		if !ok || (v == "" && !e.EmptyIsSetting()) {
			continue
		}
		// A launcherIgnores knob's flag is inert (see FlagEntry), so its
		// settings path is the only real migration target;
		// nix/checks/schema-drift.nix pins flakeOption = true on every such
		// knob, which lib/renderers.nix turns into a settings path (#3698).
		equiv := e.SettingsPath
		if !e.LauncherIgnores {
			equiv = "--" + e.Flag
			if e.SettingsPath != "" {
				equiv += " or " + e.SettingsPath
			}
		}
		if v == "" {
			v = `""`
		}
		lines = append(lines, fmt.Sprintf("%s=%s set in environment — knob env vars are not read; use %s", e.Env, v, equiv))
	}
	if len(lines) == 0 {
		return nil
	}
	return configInvalidError{strings.Join(lines, "\n")}
}

// Resolved is the Launcher input resolved once from args, document and
// environment: the typed knobs, the document's artifacts, and the args no
// schema flag consumed.
type Resolved struct {
	Knobs     Knobs
	Artifacts Artifacts
	// Args is every token Resolve did not consume as a schema flag, in order.
	// The verb table and the secret --*-file/--*-cmd flags need I/O and stay
	// with the caller's parser.
	Args []string

	src knobResolver
}

// Resolve applies flag > document settings > schema default to every schema
// knob. It is a pure function of its inputs: env is read only for the ambient
// check, secrets and plumbing. An ambient knob returns AmbientKnobError, which
// matches ErrConfigInvalid via errors.Is; flag-parse errors do not.
// Secret knobs (Knobs.GhToken and the like) come from env only, so a caller
// must resolve the --*-file/--*-cmd secret flags left in Args into env before
// calling Resolve.
func Resolve(args []string, doc *Document, env map[string]string) (Resolved, error) {
	if err := AmbientKnobError(env); err != nil {
		return Resolved{}, err
	}
	explicit, rest, err := scanSchemaFlags(args)
	if err != nil {
		return Resolved{}, err
	}
	var settings map[string]string
	var artifacts Artifacts
	if doc != nil {
		settings, artifacts = doc.Settings, doc.Artifacts
	}
	src := knobResolver{explicit: explicit, settings: settings, env: env}
	return Resolved{Knobs: LoadKnobs(src), Artifacts: artifacts, Args: rest, src: src}, nil
}

// BoxEnv resolves one BOX_ENV_VARS forwarding name: a schema knob through the
// same tiers as Knobs, anything else (secrets, plumbing) from a non-empty env
// value, else the document.
func (r Resolved) BoxEnv(name string) string {
	if _, ok := SchemaFlag(name); ok {
		return r.src.String(name)
	}
	if v := r.src.env[name]; v != "" {
		return v
	}
	return r.src.settings[name]
}

// scanSchemaFlags lifts the schema half of cmd/launcher's parseFlags into a
// pure pass: it returns the explicit flag values keyed by knob env (a bool is
// "1" or "" for off) and every other token in order. "--" ends scanning.
func scanSchemaFlags(args []string) (explicit map[string]string, rest []string, err error) {
	byFlag := map[string]FlagEntry{}
	byBool := map[string]FlagEntry{}
	for _, e := range SchemaFlags {
		m := byFlag
		if e.Kind == "bool" {
			m = byBool
		}
		for _, name := range []string{e.Flag, e.Alias, e.DeprecatedAlias} {
			if name != "" {
				m["--"+name] = e
			}
		}
	}
	// The next token is the secret flag's path or command, never a schema flag.
	secretFlag := map[string]bool{}
	for _, s := range SecretKnobs {
		for _, name := range []string{s.FileFlag, s.CmdFlag} {
			if name != "" {
				secretFlag["--"+name] = true
			}
		}
	}

	explicit = map[string]string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, value, hasEquals := strings.Cut(arg, "=")
		if e, ok := byBool[name]; ok {
			on := !hasEquals
			if hasEquals && value != "" {
				b, perr := strconv.ParseBool(value)
				if perr != nil {
					return nil, nil, fmt.Errorf("flag %s: invalid boolean value %q", name, value)
				}
				on = b
			}
			explicit[e.Env] = ""
			if on {
				explicit[e.Env] = "1"
			}
			continue
		}
		if e, ok := byFlag[arg]; ok {
			i++
			if i >= len(args) {
				return nil, nil, fmt.Errorf("flag %s requires a value", arg)
			}
			explicit[e.Env] = args[i]
			continue
		}
		rest = append(rest, arg)
		if secretFlag[arg] && i+1 < len(args) {
			i++
			rest = append(rest, args[i])
		}
	}
	return explicit, rest, nil
}

// knobResolver is the KnobSource over the flag, document and schema-default
// tiers; env serves secrets, and plumbing names via Resolved.BoxEnv.
type knobResolver struct {
	explicit map[string]string
	settings map[string]string
	env      map[string]string
}

// flag returns the explicit flag value for key when it counts as set.
func (k knobResolver) flag(key string) (string, bool) {
	v, ok := k.explicit[key]
	e, _ := SchemaFlag(key)
	return v, ok && (v != "" || e.EmptyIsSetting())
}

// fallback is the document value when it carries key (even empty: an empty
// bool is off and an empty emptyDisables knob is its disabled state), else
// the schema default.
func (k knobResolver) fallback(key string) string {
	if v, ok := k.settings[key]; ok {
		return v
	}
	return SchemaDefault(key)
}

func (k knobResolver) String(key string) string {
	if v, ok := k.flag(key); ok {
		return v
	}
	return k.fallback(key)
}

func (k knobResolver) PositiveInt(key string) int {
	if n, err := strconv.Atoi(k.explicit[key]); err == nil && n > 0 {
		return n
	}
	n, _ := strconv.Atoi(k.fallback(key))
	return n
}

func (k knobResolver) NonnegInt(key string) int {
	if n, err := strconv.Atoi(k.explicit[key]); err == nil && n >= 0 {
		return n
	}
	n, _ := strconv.Atoi(k.fallback(key))
	return n
}

func (k knobResolver) NonnegFloat(key string) float64 {
	// Must match promptassembly.ParseNonnegBudgetUSD, which inputdoc cannot
	// import (leaf package, narrow vendored filesets); infinities are rejected
	// per issue #6029.
	if n, err := strconv.ParseFloat(k.explicit[key], 64); err == nil && n >= 0 && !math.IsInf(n, 0) {
		return n
	}
	n, _ := strconv.ParseFloat(k.fallback(key), 64)
	return n
}

func (k knobResolver) Secret(key string) string { return k.env[key] }

// HostGitConfig answers only the flag/document/default tiers: the host git
// config fallback shells out, so the caller layers it on an empty result.
func (k knobResolver) HostGitConfig(key string) string { return k.String(key) }
