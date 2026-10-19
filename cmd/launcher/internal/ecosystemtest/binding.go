package ecosystemtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/ecosystem"
)

// bindingOutput is the binding stage's golden. Parts the row does not render
// are omitted.
type bindingOutput struct {
	// NoRoutePrefix is set instead of every other part when the manifest
	// carries no route prefix, which production binding warns about and skips.
	NoRoutePrefix bool                  `json:"noRoutePrefix,omitempty"`
	EnvExports    []ecosystem.EnvExport `json:"envExports,omitempty"`
	Warnings      []string              `json:"warnings,omitempty"`
	HomeConfig    *homeConfigOutput     `json:"homeConfig,omitempty"`
	InTree        *inTreeOutput         `json:"inTree,omitempty"`
}

type homeConfigOutput struct {
	HomeEnvVar string `json:"homeEnvVar"`
	ConfigPath string `json:"configPath"`
	Content    string `json:"content"`
}

type inTreeOutput struct {
	ConfigPath string `json:"configPath"`
	Outcome    string `json:"outcome"`
	Content    string `json:"content,omitempty"`
}

// env is the case's env.json, the prior environment EnvExports reads, and
// whether the case ships one; a case without one has an empty environment.
func (c *replay) env() (env map[string]string, shipped bool, err error) {
	raw, err := os.ReadFile(filepath.Join(c.dir, "env.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false, fmt.Errorf("env.json: %w", err)
	}
	return env, true, nil
}

// binding calls the row's renderers directly: bindregistry's BindHomes and
// ApplyInTree need a Gate, which only a live Forwarder and manifest env build.
// Like BindHomes, every renderer gets the first route's prefix.
func (c *replay) binding() ([]byte, error) {
	routes, err := c.manifestRoutes()
	if err != nil {
		return nil, err
	}
	if len(routes) == 0 || routes[0].Prefix == "" {
		return json.Marshal(bindingOutput{NoRoutePrefix: true})
	}
	port, prefix := bindregistry.ForwarderPort, routes[0].Prefix
	rewrites, inTreeRoutes, collisionWarnings := bindregistry.InTreeRoutes(routes, port)

	out := bindingOutput{Warnings: collisionWarnings}
	env, shipped, err := c.env()
	if err != nil {
		return nil, err
	}
	if shipped && c.row.EnvExports == nil {
		return nil, fmt.Errorf("env.json is shipped but row %q has no EnvExports to read it", c.row.Name)
	}
	if c.row.EnvExports != nil {
		var warnings []string
		out.EnvExports, warnings = c.row.EnvExports(port, prefix, func(k string) string { return env[k] }, routes)
		out.Warnings = append(out.Warnings, warnings...)
	}
	if hc := c.row.HomeConfig; hc != nil {
		home := &homeConfigOutput{HomeEnvVar: hc.HomeEnvVar, ConfigPath: hc.ConfigPath}
		if c.row.RepoAwareHomeConfig != nil {
			raw, err := os.ReadFile(filepath.Join(c.repo(), c.row.InTreeConfigPath))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			var exports []ecosystem.EnvExport
			var warnings []string
			home.Content, exports, warnings = c.row.RepoAwareHomeConfig(port, prefix, inTreeRoutes, string(raw))
			out.EnvExports = append(out.EnvExports, exports...)
			out.Warnings = append(out.Warnings, warnings...)
		} else {
			home.Content = hc.Render(port, prefix, routes)
		}
		out.HomeConfig = home
	}
	if len(bindregistry.InTreeBindingsOf(c.rows())) > 0 {
		if out.InTree, err = c.inTreeRewrite(rewrites); err != nil {
			return nil, err
		}
	}
	return json.Marshal(out)
}

// inTreeRewrite runs ApplyInTreeBinding, the in-tree rewrite's only exported
// seam, over a throwaway git copy of repo/: it rewrites tracked files alone.
func (c *replay) inTreeRewrite(rewrites []bindregistry.HostRewrite) (*inTreeOutput, error) {
	if len(rewrites) == 0 {
		return nil, nil
	}

	work, err := os.MkdirTemp("", "ecosystemtest-intree-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if err := os.CopyFS(work, os.DirFS(c.repo())); err != nil {
		return nil, err
	}
	// gc.auto=0 and maintenance.auto=false keep a detached gc from racing the
	// RemoveAll above.
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "corpus"},
	} {
		cmd := exec.Command("git", append([]string{"-C", work, "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %v: %w: %s", args, err, out)
		}
	}

	outcome, err := bindregistry.ApplyInTreeBinding(work, c.row, rewrites)
	if err != nil {
		return nil, err
	}
	res := &inTreeOutput{ConfigPath: c.row.InTreeConfigPath, Outcome: outcome.String()}
	content, err := os.ReadFile(filepath.Join(work, c.row.InTreeConfigPath))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	res.Content = string(content)
	return res, nil
}
