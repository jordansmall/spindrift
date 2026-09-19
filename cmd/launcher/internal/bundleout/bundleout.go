// Package bundleout is the harness-owned code-out step for CODE_FORGE=local
// (ADR 0033, issue #1808): it bundles the base..branch commit range into the
// outbox instead of asking the Agent to run `git bundle create` itself.
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
	Repo      string
	Base      string
	Branch    string
	OutboxDir string
	// Issue is only used in the corrective outcome line Run may print.
	Issue string
	// PriorOutcomeLine is the Agent's own SPINDRIFT_OUTCOME line, verbatim, or
	// "" if it never emitted one. Only its parsed status matters: a status=ready
	// claim against an empty range is the contradiction Run corrects.
	PriorOutcomeLine string
}

// Run bundles Base..Branch from Repo into OutboxDir/seambundle.FileName.
// An empty range after the Agent claimed status=ready is a contradiction: Run
// writes no bundle and prints a corrective status=blocked SPINDRIFT_OUTCOME
// line to w, which the launcher's last-line-wins log scan (outcome.Resolve)
// picks up. An empty range after any other status is already consistent.
func Run(cfg Config, w io.Writer) error {
	// Base and Branch interpolate into a `base..branch` range spec, so guard
	// them even though the harness controls both today.
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
	// Output(), not CombinedOutput(): a git warning on stderr would merge into
	// the count text and break Atoi below.
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
