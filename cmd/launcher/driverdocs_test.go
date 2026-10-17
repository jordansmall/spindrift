package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// The reference guide's per-Driver credential list is hand-written prose over
// the credential rules each Driver row declares in lib/drivers/<name>.nix
// (issue #5505). Parsing it here keeps the two from drifting.
func TestReferenceGuide_DriverCredentialListMatchesRules(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference.md"))
	if err != nil {
		t.Fatalf("read reference guide: %v", err)
	}
	const anchor = "The active Driver's credential is likewise the only one"
	text := string(raw)
	i := strings.Index(text, anchor)
	if i < 0 {
		t.Fatalf("anchor sentence %q not found in docs/reference.md", anchor)
	}
	bullets := parseTopLevelBullets(text[i:])
	bulletRe := regexp.MustCompile("^- `([a-z0-9-]+)`")

	byName := map[string]string{}
	for _, b := range bullets {
		m := bulletRe.FindStringSubmatch(b)
		if m == nil {
			t.Fatalf("bullet does not start with a backticked Driver name: %q", b)
		}
		byName[m[1]] = b
	}

	names := driver.Names()
	for name := range byName {
		if !slices.Contains(names, name) {
			t.Errorf("docs/reference.md credential list names Driver %q, which is not registered; remove the bullet", name)
		}
	}

	owner := map[string]string{}
	for _, name := range names {
		rules, _ := driver.CredentialRules(name)
		for _, r := range rules {
			for _, env := range r.Env {
				owner[env] = name
			}
		}
	}

	for _, name := range names {
		bullet, ok := byName[name]
		if !ok {
			t.Errorf("docs/reference.md credential list has no bullet for Driver %q; add one from lib/drivers/%s.nix", name, name)
			continue
		}
		rules, _ := driver.CredentialRules(name)
		for _, r := range rules {
			for _, env := range r.Env {
				if !strings.Contains(bullet, "`"+env+"`") {
					t.Errorf("docs/reference.md bullet for %q omits `%s`; update the bullet from lib/drivers/%s.nix", name, env, name)
				}
			}
			if r.ModelPrefix != "" && !strings.Contains(bullet, "`"+r.ModelPrefix) {
				t.Errorf("docs/reference.md bullet for %q omits model prefix `%s`; update the bullet from lib/drivers/%s.nix", name, r.ModelPrefix, name)
			}
		}
		for env, o := range owner {
			if o != name && strings.Contains(bullet, "`"+env+"`") {
				t.Errorf("docs/reference.md bullet for %q names `%s`, which belongs to Driver %q; update the bullet from lib/drivers/%s.nix", name, env, o, name)
			}
		}
	}
}

// parseTopLevelBullets returns the "- " bullets (with their indented
// continuation lines joined) that follow text's first line, up to
// the blank line that ends the list.
func parseTopLevelBullets(text string) []string {
	var bullets []string
	started := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "- "):
			started = true
			bullets = append(bullets, line)
		case started && strings.HasPrefix(line, " ") && len(bullets) > 0:
			bullets[len(bullets)-1] += " " + strings.TrimSpace(line)
		case started && strings.TrimSpace(line) == "":
			return bullets
		}
	}
	return bullets
}
