# The dogfood's tuned leaf values, defined exactly once and consumed by both
# flake.nix's `spindrift` module config and nix/fixtures.nix's direct
# mkHarness mirror, so the flakemodule-equivalence check verifies the two
# wiring paths rather than two hand-copied value sets (issue #459).
{ system, lib }:
let
  isLinux = builtins.match ".*-linux" system != null;
  rosterLib = import ../lib/roster.nix { inherit lib; };
in
{
  # `nix flake archive` warms flake inputs alongside the Go module cache, so
  # a subsequent in-box `nix flake check` doesn't hit the network cold
  # (ADR 0008's original suggestion, wired in by issue #470).
  prefetch = "go mod download || true\nnix flake archive || true";
  packages = p: [
    p.go
    p.nil
    p.bats
    p.shellcheck
  ];
  # Self-test mode (ADR 0018, issue #469): spindrift dogfoods its own writable
  # store so a Box working a spindrift issue can run real `nix flake check`
  # in-box (issue #470) instead of round-tripping CI for nix feedback.
  nixStoreWritable = true;
  # Bake the rest of nix/checks.nix's dependency closure so in-box
  # `nix flake check` doesn't cold-substitute it. `go`/`bats`/`shellcheck`
  # above and `bash`/`coreutils`/`git`/`gettext`/`jq`/`gnugrep`/`gnused` (baked
  # unconditionally by every mkHarness image) already cover most checks;
  # `nixfmt` and `mandoc` are the remaining gap (issue #470).
  extraClosures = p: [
    p.nixfmt
    p.mandoc
  ];
  # Source spindrift's own dogfood agent models/efforts from the explicit
  # default roster (issue #2388), configured by roster entry name (issue
  # #2426) rather than the legacy per-agent model knobs. The `filer` entry
  # below carries forward the Filer's (#393, landed 2026-07-09) tuned model,
  # so non-blocking review findings still become tracked
  # `agent-review-finding` issues instead of staying stuck in PR bodies. The
  # `reviewer` entry (issue #2427) puts the strongest available model behind
  # the orchestrator's code-owned review pass -- the highest-leverage read in
  # the loop, and already run at `high` effort below -- instead of letting it
  # inherit the coordinator's own model. The `scout` and `worker` entries
  # (issue #2428) provision those subagents at all: without a model, every
  # Driver drops them from the rendered agent config and neither is ever
  # provisioned. `defaultRoster` also ships this roster's fixed per-agent
  # efforts (issue #2386).
  roster = rosterLib.defaultRoster {
    models = {
      scout = "claude-haiku-4-5-20251001";
      reviewer = "claude-opus-5";
      filer = "claude-haiku-4-5-20251001";
      worker = "claude-sonnet-5";
    };
  };
  defaults = {
    mergeMode = "immediate";
    autoFormat = true;
    autoLint = true;
    # Dogfood the host-mediated read-only path (ADR 0034, #1916-#1919): the
    # Box makes no forge/tracker writes; the launcher relays branch, draft PR,
    # and comment writes host-side. github satisfies the capability gate.
    boxForgeAndIssueAccess = "read-only";
    # Drive the orchestrator's code-owned review pass (issue #2387's
    # REVIEW_EFFORT/--review-effort knob) at the same effort as the roster's
    # `reviewer` entry above.
    reviewEffort = "high";
    # Cap podman's container memory on darwin/windows, where podman runs in a
    # fixed-RAM VM (issue #712's original rationale); native Linux shares host
    # RAM with the container directly, so no cap is needed there (issue #2379).
    memoryLimit = if isLinux then "" else "5g";
  };
}
