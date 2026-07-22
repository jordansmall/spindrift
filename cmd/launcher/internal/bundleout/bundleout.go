// Package bundleout is the harness-owned code-out step for CODE_FORGE=local
// (ADR 0033, issue #1808): bundling the base..branch commit range into the
// outbox, in place of the Agent's own `git bundle create` prompt
// instruction. driver-exec's `bundle-out` verb is a thin CLI wrapper around
// Run; the localloop composed test calls Run directly as the same real
// producer, so both consumers share one implementation instead of the
// prompt↔Go string coupling this replaces.
package bundleout

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/seambundle"
)

// Config is everything Run needs to bundle one seam's code-out.
type Config struct {
	// Repo is the path to the git repository holding both Base and Branch.
	Repo string
	// Base is the base ref, e.g. "main" or "origin/main".
	Base string
	// Branch is the agent branch name, e.g. "agent/issue-42".
	Branch string
	// OutboxDir is the directory Run writes the bundle into.
	OutboxDir string
	// Issue is the issue number, carried into a corrective outcome line.
	Issue string
	// PriorOutcomeLine is the Agent's own SPINDRIFT_OUTCOME line, verbatim,
	// or "" if it never emitted one. Only its parsed status matters: a
	// status=ready claim against an empty range is the contradiction Run
	// corrects.
	PriorOutcomeLine string
}

// Run bundles Base..Branch from Repo into OutboxDir/seambundle.FileName.
// An empty range after the Agent's own claimed status=ready is a
// contradiction the Box can't leave standing: no bundle is written, and a
// corrective status=blocked SPINDRIFT_OUTCOME line is printed to w instead,
// picked up by the launcher's last-line-wins log scan (outcome.LastInLog)
// with no launcher changes. An empty range after any other claimed status is
// already consistent — nothing is written.
func Run(cfg Config, w io.Writer) error {
	// Defense in depth, matching forge/local's relayBundle: Base and Branch
	// are harness-controlled today (BASE_BRANCH/BRANCH), but both interpolate
	// directly into a `base..branch` range spec, so guard them the same way
	// regardless.
	if err := validateRef(cfg.Base); err != nil {
		return err
	}
	if err := validateRef(cfg.Branch); err != nil {
		return err
	}

	count, err := commitCount(cfg.Repo, cfg.Base, cfg.Branch)
	if err != nil {
		return err
	}
	if count > 0 {
		return createBundle(cfg.Repo, cfg.Base, cfg.Branch, cfg.OutboxDir)
	}

	prior, err := outcome.Parse(cfg.PriorOutcomeLine)
	if err == nil && prior.Status == "ready" {
		corrective := outcome.Outcome{
			Issue:   cfg.Issue,
			Landing: "none",
			Status:  "blocked",
			Note:    fmt.Sprintf("agent reported ready but no commits exist on %s", cfg.Branch),
		}
		if _, err := fmt.Fprintln(w, corrective.Line()); err != nil {
			return err
		}
	}
	return nil
}

func validateRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("bundleout: invalid ref %q", ref)
	}
	return nil
}

func commitCount(repo, base, branch string) (int, error) {
	cmd := exec.Command("git", "-C", repo, "rev-list", "--count", base+".."+branch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Output() (stdout only) rather than CombinedOutput(): a git warning on
	// stderr would otherwise merge into the count text and break Atoi below.
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("bundleout: rev-list --count %s..%s: %w: %s", base, branch, err, stderr.String())
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("bundleout: parse rev-list output %q: %w", out, err)
	}
	return n, nil
}

func createBundle(repo, base, branch, outboxDir string) error {
	if err := os.MkdirAll(outboxDir, 0o755); err != nil {
		return fmt.Errorf("bundleout: create outbox dir %s: %w", outboxDir, err)
	}
	bundlePath := filepath.Join(outboxDir, seambundle.FileName)
	rangeSpec := base + ".." + branch
	if out, err := exec.Command("git", "-C", repo, "bundle", "create", bundlePath, rangeSpec).CombinedOutput(); err != nil {
		return fmt.Errorf("bundleout: bundle create %s: %w: %s", bundlePath, err, out)
	}
	return nil
}
