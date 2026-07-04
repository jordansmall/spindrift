// Package main: spindrift launcher — orchestrates open issues into disposable
// containers. Config is baked into env vars by the nix wrapper (imagePreamble,
// runDefaultsPreamble, etc.); harness.env overrides those at runtime. The
// binary contains no baked store paths of its own beyond what nix injects.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

type config struct {
	// OCI image config (baked by nix wrapper; empty for bwrap)
	imageArchive    string
	imageTag        string
	imageDrv        string
	nixBuilderImage string
	nixVolume       string
	flakeImageAttr  string

	// bwrap agent closure paths (bwrap only)
	agentFiles    string
	agentEnv      string
	bakedPrefetch string

	// Runtime: podman | docker | bwrap
	runtime string

	// image is the runtime image reference; defaults to imageTag
	image string

	// Run defaults (overrideable via env / harness.env)
	repoSlug        string
	label           string
	baseBranch      string
	maxParallel     int
	branchPrefix    string
	inProgressLabel string
	failedLabel     string
	completeLabel   string
	model           string
	scoutModel      string
	reviewModel     string
	maxJobs         int

	// Dependency-wave knobs
	depsPollSecs int
	depsWaitSecs int

	// Merge gate polling knobs
	mergePollInterval int
	mergePollTimeout  int

	// Secrets / identity
	ghToken          string
	claudeOAuthToken string
	anthropicAPIKey  string
	gitUserName      string
	gitUserEmail     string

	// Optional prompt override
	spindriftPromptDir string
}

type issue struct {
	number string
	title  string
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// atoi parses a positive integer; zero and negatives fall back to def.
// Use this for values where zero would cause a bug (e.g. semaphore capacity).
func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

// atoiNonneg parses a non-negative integer; negatives fall back to def.
// Use this for values where zero is valid (e.g. timeouts, poll intervals).
func atoiNonneg(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return n
	}
	return def
}

func loadConfig() config {
	imageTag := getenv("IMAGE_TAG", "spindrift:latest")
	image := os.Getenv("IMAGE")
	if image == "" {
		image = imageTag
	}
	return config{
		imageArchive:    os.Getenv("IMAGE_ARCHIVE"),
		imageTag:        imageTag,
		imageDrv:        os.Getenv("IMAGE_DRV"),
		nixBuilderImage: os.Getenv("NIX_BUILDER_IMAGE"),
		nixVolume:       getenv("NIX_VOLUME", "spindrift-nix"),
		flakeImageAttr:  os.Getenv("FLAKE_IMAGE_ATTR"),
		agentFiles:      os.Getenv("AGENT_FILES"),
		agentEnv:        os.Getenv("AGENT_ENV"),
		bakedPrefetch:   os.Getenv("BAKED_PREFETCH"),
		runtime:         os.Getenv("RUNTIME"),
		image:           image,

		repoSlug:        os.Getenv("REPO_SLUG"),
		label:           getenv("LABEL", "ready-for-agent"),
		baseBranch:      getenv("BASE_BRANCH", "main"),
		maxParallel:     atoi(getenv("MAX_PARALLEL", "3"), 3),
		branchPrefix:    getenv("BRANCH_PREFIX", "agent/issue-"),
		inProgressLabel: getenv("IN_PROGRESS_LABEL", "agent-in-progress"),
		failedLabel:     getenv("FAILED_LABEL", "agent-failed"),
		completeLabel:   getenv("COMPLETE_LABEL", "agent-complete"),
		model:           getenv("MODEL", "claude-opus-4-8"),
		scoutModel:      os.Getenv("SCOUT_MODEL"),
		reviewModel:     os.Getenv("REVIEW_MODEL"),
		maxJobs:         atoiNonneg(os.Getenv("MAX_JOBS"), 0),

		depsPollSecs: atoiNonneg(getenv("DEPS_POLL_SECS", "30"), 30),
		depsWaitSecs: atoiNonneg(getenv("DEPS_WAIT_SECS", "7200"), 7200),

		mergePollInterval: atoiNonneg(getenv("MERGE_POLL_INTERVAL", "30"), 30),
		mergePollTimeout:  atoiNonneg(getenv("MERGE_POLL_TIMEOUT", "1800"), 1800),

		ghToken:          os.Getenv("GH_TOKEN"),
		claudeOAuthToken: os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"),
		anthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		gitUserName:      os.Getenv("GIT_USER_NAME"),
		gitUserEmail:     os.Getenv("GIT_USER_EMAIL"),

		spindriftPromptDir: os.Getenv("SPINDRIFT_PROMPT_DIR"),
	}
}

func validate(c config) error {
	if c.repoSlug == "" {
		return fmt.Errorf("set REPO_SLUG=owner/repo (the target GitHub repository)")
	}
	if c.gitUserName == "" {
		return fmt.Errorf("set GIT_USER_NAME, or configure git user.name on the host")
	}
	if c.gitUserEmail == "" {
		return fmt.Errorf("set GIT_USER_EMAIL, or configure git user.email on the host")
	}
	if c.ghToken == "" {
		return fmt.Errorf("set GH_TOKEN (fine-grained PAT scoped to the single target repo: Issues RW, Contents RW, Pull requests RW, Metadata R)")
	}
	if c.claudeOAuthToken == "" && c.anthropicAPIKey == "" {
		return fmt.Errorf("set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY")
	}
	if c.runtime == "" {
		return fmt.Errorf("RUNTIME is not set")
	}
	if _, err := exec.LookPath(c.runtime); err != nil {
		return fmt.Errorf("%s not found on PATH.", c.runtime)
	}
	return nil
}

func loadImage(runtime, archive, imageTag string) error {
	fmt.Printf("==> loading spindrift image from %s\n", archive)
	load := exec.Command(runtime, "load", "-i", archive)
	load.Stdout = os.Stdout
	load.Stderr = os.Stderr
	if err := load.Run(); err != nil {
		return fmt.Errorf("load failed: %w", err)
	}
	tag := exec.Command(runtime, "tag", "spindrift:latest", imageTag)
	tag.Stdout = os.Stdout
	tag.Stderr = os.Stderr
	if err := tag.Run(); err != nil {
		return fmt.Errorf("tag failed: %w", err)
	}
	fmt.Printf("==> done: spindrift:latest + %s\n", imageTag)
	return nil
}

func buildInContainer(c config, pwd string) error {
	tar := filepath.Join(pwd, ".spindrift-image.tar")
	pathfile := ".spindrift-image-path"
	fmt.Printf("==> no host Linux builder; building the image inside a %s container\n", c.nixBuilderImage)
	fmt.Printf("    (reusing the '%s' volume for /nix so rebuilds are incremental)\n", c.nixVolume)

	shCmd := fmt.Sprintf(
		"nix --extra-experimental-features 'nix-command flakes' build '%s' --print-out-paths --no-link >%s && cp \"$(cat %s)\" .spindrift-image.tar",
		c.flakeImageAttr, pathfile, pathfile,
	)
	build := exec.Command(c.runtime, "run", "--rm",
		"-v", c.nixVolume+":/nix",
		"-v", pwd+":/workspace",
		"-w", "/workspace",
		c.nixBuilderImage,
		"sh", "-euc", shCmd,
	)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "==> container build failed — see the %s output above.\n", c.runtime)
		_ = os.Remove(tar)
		_ = os.Remove(filepath.Join(pwd, pathfile))
		return fmt.Errorf("container build failed")
	}
	if err := loadImage(c.runtime, tar, c.imageTag); err != nil {
		return err
	}
	_ = os.Remove(tar)
	_ = os.Remove(filepath.Join(pwd, pathfile))
	return nil
}

// ensureImage checks that the OCI image is present and builds it if not.
func ensureImage(c config, pwd string) error {
	exists := exec.Command(c.runtime, "image", "exists", c.image)
	if err := exists.Run(); err == nil {
		return nil
	}
	fmt.Printf("==> image '%s' not found — building first\n", c.image)

	// 1. Try host build (nix build <drv>^* --no-link).
	nixBuild := exec.Command("nix", "build", c.imageDrv+"^*", "--no-link")
	nixBuild.Stdout = os.Stdout
	nixBuild.Stderr = os.Stderr
	if err := nixBuild.Run(); err == nil {
		fmt.Println("==> realised image derivation on the host")
		return loadImage(c.runtime, c.imageArchive, c.imageTag)
	}

	// 2. Fall back to ephemeral nix container if the runtime is available.
	if _, err := exec.LookPath(c.runtime); err == nil {
		return buildInContainer(c, pwd)
	}

	// 3. Neither path is possible.
	fmt.Fprintf(os.Stderr, `==> cannot build the spindrift image.

The image is a Linux (OCI) derivation, and this host can neither realise it
directly nor fall back to a container build:

  * No Linux builder: 'nix build' could not realise the image. On macOS, enable
    nix-darwin's 'nix.linux-builder.enable = true;', or point nix at a remote
    Linux builder via 'nix.buildMachines' / '--builders'.

  * No container runtime: '%s' was not found on PATH. Install it (or set
    'runtime = "docker"' in your mkHarness call) so 'build' can build the image
    inside an ephemeral Nix container.

Run 'build' from your Consumer flake's directory.
`, c.runtime)
	return fmt.Errorf("cannot build image: no Linux builder and no container runtime")
}

// swapLabel is a best-effort label transition that logs but does not propagate
// errors, matching the original behaviour.
func swapLabel(fc forge.Client, num, add, remove string) {
	if err := fc.SwapLabel(num, add, remove); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not set label '%s' (remove '%s')\n", num, add, remove)
	}
}

// runOneOCI fans out a single issue into a podman/docker container.
func runOneOCI(c config, iss issue, logFile *os.File) error {
	// Reap any stale container from a prior interrupted run.
	reap := exec.Command(c.runtime, "rm", "-f", "agent-issue-"+iss.number)
	_ = reap.Run()

	args := []string{"run", "--rm",
		"--name", "agent-issue-" + iss.number,
		"-e", "GH_TOKEN",
	}
	if c.claudeOAuthToken != "" {
		args = append(args, "-e", "CLAUDE_CODE_OAUTH_TOKEN")
	}
	if c.anthropicAPIKey != "" {
		args = append(args, "-e", "ANTHROPIC_API_KEY")
	}
	args = append(args,
		"-e", "GIT_USER_NAME="+c.gitUserName,
		"-e", "GIT_USER_EMAIL="+c.gitUserEmail,
		"-e", "REPO_SLUG="+c.repoSlug,
		"-e", "ISSUE_NUMBER="+iss.number,
		"-e", "ISSUE_TITLE="+iss.title,
		"-e", "BASE_BRANCH="+c.baseBranch,
		"-e", "BRANCH_PREFIX="+c.branchPrefix,
		"-e", "MODEL="+c.model,
		"-e", "SCOUT_MODEL="+c.scoutModel,
		"-e", "REVIEW_MODEL="+c.reviewModel,
		"-e", "IN_PROGRESS_LABEL="+c.inProgressLabel,
		"-e", "COMPLETE_LABEL="+c.completeLabel,
	)
	if c.spindriftPromptDir != "" {
		if info, err := os.Stat(c.spindriftPromptDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", c.spindriftPromptDir)
			args = append(args, "-v", c.spindriftPromptDir+":/agent/prompts:ro")
		}
	}
	args = append(args, c.image, "/agent/entrypoint.sh")

	cmd := exec.Command(c.runtime, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}

// runOneBwrap fans out a single issue into a bubblewrap sandbox.
func runOneBwrap(c config, iss issue, logFile *os.File) error {
	etcDir, err := os.MkdirTemp("", "spindrift-etc-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(etcDir)

	passwd := "root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1000:agent:/home/agent:/bin/bash\n"
	group := "root:x:0:\nagent:x:1000:\n"
	if err := os.WriteFile(filepath.Join(etcDir, "passwd"), []byte(passwd), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(etcDir, "group"), []byte(group), 0o644); err != nil {
		return err
	}

	args := []string{
		"--ro-bind", "/nix/store", "/nix/store",
		"--tmpfs", "/tmp",
		"--tmpfs", "/work",
		"--tmpfs", "/home/agent",
		"--proc", "/proc",
		"--dev", "/dev",
		"--dir", "/etc",
		"--ro-bind", filepath.Join(etcDir, "passwd"), "/etc/passwd",
		"--ro-bind", filepath.Join(etcDir, "group"), "/etc/group",
	}
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
	}
	args = append(args, "--ro-bind", c.agentFiles+"/agent", "/agent")
	if c.spindriftPromptDir != "" {
		if info, err := os.Stat(c.spindriftPromptDir); err == nil && info.IsDir() {
			fmt.Printf("==> SPINDRIFT_PROMPT_DIR set; mounting %s over the baked prompt\n", c.spindriftPromptDir)
			args = append(args, "--ro-bind", c.spindriftPromptDir, "/agent/prompts")
		}
	}
	args = append(args,
		"--clearenv",
		"--setenv", "HOME", "/home/agent",
		"--setenv", "PATH", c.agentEnv+"/bin",
		"--setenv", "SSL_CERT_FILE", c.agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GIT_SSL_CAINFO", c.agentEnv+"/etc/ssl/certs/ca-bundle.crt",
		"--setenv", "GH_TOKEN", c.ghToken,
	)
	if c.claudeOAuthToken != "" {
		args = append(args, "--setenv", "CLAUDE_CODE_OAUTH_TOKEN", c.claudeOAuthToken)
	}
	if c.anthropicAPIKey != "" {
		args = append(args, "--setenv", "ANTHROPIC_API_KEY", c.anthropicAPIKey)
	}
	args = append(args,
		"--setenv", "GIT_USER_NAME", c.gitUserName,
		"--setenv", "GIT_USER_EMAIL", c.gitUserEmail,
		"--setenv", "REPO_SLUG", c.repoSlug,
		"--setenv", "ISSUE_NUMBER", iss.number,
		"--setenv", "ISSUE_TITLE", iss.title,
		"--setenv", "BASE_BRANCH", c.baseBranch,
		"--setenv", "BRANCH_PREFIX", c.branchPrefix,
		"--setenv", "MODEL", c.model,
		"--setenv", "SCOUT_MODEL", c.scoutModel,
		"--setenv", "REVIEW_MODEL", c.reviewModel,
		"--setenv", "IN_PROGRESS_LABEL", c.inProgressLabel,
		"--setenv", "COMPLETE_LABEL", c.completeLabel,
		"--setenv", "PREFETCH", c.bakedPrefetch,
		"--unshare-user", "--uid", "1000", "--gid", "1000",
		"--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--", "/agent/entrypoint.sh",
	)

	cmd := exec.Command("bwrap", args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}

// runOne dispatches one issue into a container and logs its output.
func runOne(c config, pwd string, iss issue) error {
	logPath := filepath.Join(pwd, "logs", "issue-"+iss.number+".log")
	fmt.Printf("    -> #%s: %s\n", iss.number, iss.title)

	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	defer logFile.Close()

	if c.runtime == "bwrap" {
		return runOneBwrap(c, iss, logFile)
	}
	return runOneOCI(c, iss, logFile)
}

// outcomeLine returns the last SPINDRIFT_OUTCOME line from the issue log.
// Uses a 4 MiB scanner buffer so that large tool-output lines (JSON, file
// reads) before the outcome line do not silently truncate the scan.
func outcomeLine(logPath string) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	var last string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "SPINDRIFT_OUTCOME ") {
			last = line
		}
	}
	return last
}

// field extracts the value of `key=<value>` from a space-delimited outcome line.
func field(line, key string) string {
	prefix := key + "="
	for _, tok := range strings.Fields(line) {
		if strings.HasPrefix(tok, prefix) {
			return tok[len(prefix):]
		}
	}
	return ""
}

// noteField extracts everything after `note=` (may contain spaces).
func noteField(line string) string {
	if idx := strings.Index(line, "note="); idx >= 0 {
		return line[idx+5:]
	}
	return ""
}

// mergeWhenGreen polls CheckState on the PR's head commit until the state
// reaches SUCCESS, a terminal failure, or mergePollTimeout seconds elapse. On
// SUCCESS it merges via rebase and swaps the issue to completeLabel; on any
// failure or timeout it swaps to failedLabel. Returns true when merged.
func mergeWhenGreen(c config, fc forge.Client, num, pr string) bool {
	pollIv := c.mergePollInterval
	deadline := c.mergePollTimeout
	// actualIv is used for elapsed tracking; floor to 1 so we don't
	// hot-spin. When pollIv is 0 (test mode) the sleep duration is also 0,
	// so elapsed still advances and the loop terminates.
	actualIv := pollIv
	if actualIv <= 0 {
		actualIv = 1
	}
	elapsed := 0

	for {
		state, _ := fc.CheckState(pr)

		switch state {
		case forge.StateSuccess:
			if err := fc.Merge(pr); err == nil {
				swapLabel(fc, num, c.completeLabel, c.inProgressLabel)
				return true
			}
			swapLabel(fc, num, c.failedLabel, c.inProgressLabel)
			return false
		case forge.StateFailure, forge.StateError:
			// Hard failure — refuse without further polling.
			swapLabel(fc, num, c.failedLabel, c.inProgressLabel)
			return false
		}

		// PENDING, EXPECTED, NONE (no checks yet), or unrecognised — keep
		// waiting until timeout.
		if elapsed >= deadline {
			break
		}
		// Sleep 0 when pollIv is 0 (test mode) so tests run without real
		// delays; actualIv still advances elapsed to prevent a tight loop.
		time.Sleep(time.Duration(pollIv) * time.Second)
		elapsed += actualIv
	}
	swapLabel(fc, num, c.failedLabel, c.inProgressLabel)
	return false
}

func verifyMerged(c config, fc forge.Client, num, pr string) {
	prState, _ := fc.PRState(pr)
	iss, _ := fc.Issue(num)
	if prState == "MERGED" && containsLabel(iss.Labels, c.completeLabel) {
		fmt.Printf("    #%s  pr=%s  status=verified-merged\n", num, pr)
		return
	}
	var reason string
	if prState != "MERGED" {
		if prState == "" {
			reason = "PR state is 'unknown', expected MERGED"
		} else {
			reason = fmt.Sprintf("PR state is '%s', expected MERGED", prState)
		}
	} else {
		reason = fmt.Sprintf("issue does not carry '%s'", c.completeLabel)
	}
	fmt.Printf("    #%s  pr=%s  status=failed  !! %s\n", num, pr, reason)
	swapLabel(fc, num, c.failedLabel, c.inProgressLabel)
}

func printOutcomeReport(c config, fc forge.Client, pwd string, issues []issue) {
	fmt.Println("==> outcome report")
	for _, iss := range issues {
		logPath := filepath.Join(pwd, "logs", "issue-"+iss.number+".log")
		line := outcomeLine(logPath)
		if line == "" {
			branch := c.branchPrefix + iss.number
			pr, isDraft, found, err := openPRForBranch(fc, branch)
			if err != nil || !found {
				fmt.Printf("    #%s  status=missing  note=no SPINDRIFT_OUTCOME in log\n", iss.number)
				continue
			}
			if isDraft {
				fmt.Printf("    #%s  pr=%s  status=blocked  note=draft PR on %s; no outcome line\n", iss.number, pr, branch)
				continue
			}
			fmt.Printf("    #%s  pr=%s  status=adopted  note=no outcome line; PR discovered on %s\n", iss.number, pr, branch)
			if mergeWhenGreen(c, fc, iss.number, pr) {
				verifyMerged(c, fc, iss.number, pr)
			} else {
				fmt.Printf("    #%s  pr=%s  status=failed  !! CI or merge failed\n", iss.number, pr)
			}
			continue
		}
		pr := field(line, "pr")
		status := field(line, "status")
		note := noteField(line)

		switch status {
		case "blocked":
			fmt.Printf("    #%s  pr=%s  status=%s  !! %s\n", iss.number, pr, status, note)
		case "ready":
			// Agent pushed a PR but left CI to the launcher — poll and merge.
			if mergeWhenGreen(c, fc, iss.number, pr) {
				verifyMerged(c, fc, iss.number, pr)
			} else {
				fmt.Printf("    #%s  pr=%s  status=failed  !! CI or merge failed\n", iss.number, pr)
			}
		case "merged":
			// Agent already merged (legacy path) — verify the GitHub state.
			verifyMerged(c, fc, iss.number, pr)
		default:
			fmt.Printf("    #%s  pr=%s  status=%s\n", iss.number, pr, status)
		}
	}
}

// openPRForBranch wraps fc.OpenPRForBranch to unpack the PR struct for callers
// that need the URL and draft flag separately.
func openPRForBranch(fc forge.Client, branch string) (url string, isDraft bool, found bool, err error) {
	pr, ok, err := fc.OpenPRForBranch(branch)
	if err != nil || !ok {
		return "", false, false, err
	}
	return pr.URL, pr.IsDraft, true, nil
}

// Compiled once; shared by all parseBlockerRefs calls.
var (
	// Matches inline keyword patterns. The keyword must be followed by optional
	// whitespace and colon before any issue refs are scanned.
	blockKeyword = regexp.MustCompile(`(?i)(?:depends on|blocked by)\s*:?\s*`)
	// Matches "#NNN" issue references.
	issueRef = regexp.MustCompile(`#([0-9]+)`)
	// Matches "## Blocked by" (or similar) section headers.
	blockedByHeader = regexp.MustCompile(`(?i)^#+\s*blocked by\s*:?\s*$`)
	// Matches any markdown heading line (to end the "Blocked by" section).
	anyHeading = regexp.MustCompile(`^#+`)
	// Matches a bullet list item line.
	bulletItem = regexp.MustCompile(`^[ \t]*[-*][ \t]*`)
)

// parseBlockerRefs extracts all blocker issue numbers referenced in a body.
// Recognises two formats:
//   - Inline: "depends on #N" or "blocked by #N" anywhere in the body.
//     All issue refs after the keyword (to end of line) are captured.
//   - Section: a "## Blocked by" header followed by "- #N" list items.
//     All issue refs in each list item are captured.
//
// Fixes two bugs present in the bash parser: (1) header+list edges were
// silently dropped because the old single-line regex required the keyword and
// the ref on the same line; (2) only the first ref on a "blocked by #12 #13"
// line was captured.
func parseBlockerRefs(body string) []string {
	seen := map[string]bool{}
	var refs []string
	addRef := func(n string) {
		if !seen[n] {
			seen[n] = true
			refs = append(refs, n)
		}
	}

	inSection := false
	for _, rawLine := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(rawLine, "\r")

		// "## Blocked by" section header — enter the section.
		if blockedByHeader.MatchString(strings.TrimSpace(line)) {
			inSection = true
			continue
		}
		// Any other heading ends the section.
		if anyHeading.MatchString(line) {
			inSection = false
		}

		// List item inside the "Blocked by" section: extract all #N refs.
		if inSection && bulletItem.MatchString(line) {
			for _, m := range issueRef.FindAllStringSubmatch(line, -1) {
				addRef(m[1])
			}
		}

		// Inline keyword anywhere in the line: extract all #N refs that follow.
		remaining := line
		for {
			loc := blockKeyword.FindStringIndex(remaining)
			if loc == nil {
				break
			}
			after := remaining[loc[1]:]
			for _, m := range issueRef.FindAllStringSubmatch(after, -1) {
				addRef(m[1])
			}
			remaining = after
		}
	}
	return refs
}

// parseBlockers fetches each issue's body from GitHub via the forge seam and
// returns a map from issue number to the slice of issue numbers that must
// complete first.
func parseBlockers(fc forge.Client, issues []issue) (map[string][]string, error) {
	edges := map[string][]string{}
	for _, iss := range issues {
		fi, err := fc.Issue(iss.number)
		if err != nil {
			// Non-fatal: skip issues whose data cannot be fetched.
			continue
		}
		refs := parseBlockerRefs(fi.Body)
		if len(refs) > 0 {
			edges[iss.number] = refs
		}
	}
	return edges, nil
}

// detectCycle runs Kahn's algorithm on the in-batch portion of the dependency
// graph. Only edges where both endpoints appear in nums are considered; external
// blockers (not in the batch) are ignored. Returns a cycle-member issue number
// and true when a cycle exists; returns "" and false for an acyclic graph.
func detectCycle(edges map[string][]string, nums []string) (string, bool) {
	inBatch := make(map[string]bool, len(nums))
	for _, n := range nums {
		inBatch[n] = true
	}

	indegree := make(map[string]int, len(nums))
	adj := map[string][]string{}
	for _, n := range nums {
		indegree[n] = 0
	}
	for child, blockers := range edges {
		if !inBatch[child] {
			continue
		}
		for _, blocker := range blockers {
			if !inBatch[blocker] {
				continue
			}
			indegree[child]++
			adj[blocker] = append(adj[blocker], child)
		}
	}

	queue := make([]string, 0, len(nums))
	for _, n := range nums {
		if indegree[n] == 0 {
			queue = append(queue, n)
		}
	}
	done := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		done++
		for _, dep := range adj[node] {
			indegree[dep]--
			if indegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}
	if done < len(nums) {
		for _, n := range nums {
			if indegree[n] > 0 {
				return n, true
			}
		}
	}
	return "", false
}

func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// blockerReady returns true when dep carries completeLabel or is closed (a
// closed blocker without the label is treated as satisfied, with a log note).
func blockerReady(c config, fc forge.Client, dep string) bool {
	fi, err := fc.Issue(dep)
	if err != nil {
		return false
	}
	if containsLabel(fi.Labels, c.completeLabel) {
		return true
	}
	if fi.State == "CLOSED" {
		fmt.Printf("    .. blocker #%s is closed without '%s'; treating as satisfied\n", dep, c.completeLabel)
		return true
	}
	return false
}

// issueIsReady returns true when all of num's declared blockers are ready.
func issueIsReady(c config, fc forge.Client, num string, edges map[string][]string) bool {
	for _, dep := range edges[num] {
		if !blockerReady(c, fc, dep) {
			return false
		}
	}
	return true
}

// issueNums returns the number strings from a slice of issues.
func issueNums(issues []issue) []string {
	nums := make([]string, len(issues))
	for i, iss := range issues {
		nums[i] = iss.number
	}
	return nums
}

// fanOut dispatches a batch of issues in parallel (up to maxParallel at once),
// claiming the in-progress label before each goroutine launches.
func fanOut(c config, fc forge.Client, pwd string, batch []issue) {
	sem := make(chan struct{}, c.maxParallel)
	var wg sync.WaitGroup
	for _, iss := range batch {
		swapLabel(fc, iss.number, c.inProgressLabel, c.label)
		wg.Add(1)
		iss := iss
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := runOne(c, pwd, iss); err != nil {
				fmt.Printf("    !! #%s FAILED (logs/issue-%s.log)\n", iss.number, iss.number)
				swapLabel(fc, iss.number, c.failedLabel, c.inProgressLabel)
			} else {
				fmt.Printf("    <- #%s done  (logs/issue-%s.log)\n", iss.number, iss.number)
			}
		}()
	}
	wg.Wait()
}

// dispatchWaves fans issues out in dependency order. Each wave dispatches the
// currently unblocked set; blocked issues are held and rechecked after
// depsPollSecs. The deadlock timer resets on any progress; if no issue becomes
// ready within depsWaitSecs the function returns an error rather than blocking
// forever. Dispatched issues leave the remaining set even when they fail.
func dispatchWaves(c config, fc forge.Client, pwd string, issues []issue, edges map[string][]string) error {
	remaining := make([]issue, len(issues))
	copy(remaining, issues)
	elapsed := 0

	for len(remaining) > 0 {
		var ready, held []issue
		for _, iss := range remaining {
			if issueIsReady(c, fc, iss.number, edges) {
				ready = append(ready, iss)
			} else {
				held = append(held, iss)
			}
		}

		if len(ready) == 0 {
			if elapsed >= c.depsWaitSecs {
				fmt.Fprintf(os.Stderr,
					"ERROR: dependency deadlock — blockers did not reach '%s' after %ds\n",
					c.completeLabel, c.depsWaitSecs)
				for _, iss := range remaining {
					fmt.Fprintf(os.Stderr, "    #%s %s\n", iss.number, iss.title)
				}
				return fmt.Errorf("dependency deadlock")
			}
			fmt.Printf("    .. all remaining issues blocked; retrying in %ds (%ds elapsed)\n",
				c.depsPollSecs, elapsed)
			time.Sleep(time.Duration(c.depsPollSecs) * time.Second)
			elapsed += c.depsPollSecs
			continue
		}

		// Progress: reset the deadlock timer.
		elapsed = 0
		fanOut(c, fc, pwd, ready)
		remaining = held
	}
	return nil
}

func run() error {
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}

	c := loadConfig()
	if err := validate(c); err != nil {
		return err
	}

	if c.runtime != "bwrap" {
		if err := ensureImage(c, pwd); err != nil {
			return err
		}
	}

	fc := forge.NewExecClient(c.repoSlug)

	fmt.Printf("==> querying open '%s' issues in %s\n", c.label, c.repoSlug)
	rawIssues, err := fc.ListIssues(c.label)
	if err != nil {
		return err
	}
	var issues []issue
	for _, fi := range rawIssues {
		issues = append(issues, issue{number: fi.Number, title: fi.Title})
	}
	if len(issues) == 0 {
		fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
		return nil
	}

	// Build the dependency graph for the batch.
	edges, err := parseBlockers(fc, issues)
	if err != nil {
		return err
	}
	hasEdges := len(edges) > 0

	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		return err
	}

	if c.maxJobs > 0 {
		// MAX_JOBS > 0: drain up to N currently-unblocked issues, then exit.
		// A blocked oldest issue is skipped so no slot is wasted on a
		// dependency that hasn't merged yet; it waits for the next invocation.
		if hasEdges {
			if node, cycle := detectCycle(edges, issueNums(issues)); cycle {
				return fmt.Errorf("ERROR: dependency cycle detected (issue #%s is in the cycle)", node)
			}
		}
		var selected []issue
		for _, iss := range issues {
			if issueIsReady(c, fc, iss.number, edges) {
				selected = append(selected, iss)
				if len(selected) >= c.maxJobs {
					break
				}
			} else {
				fmt.Printf("    ~~ #%s blocked (a blocker is not '%s'); skipping\n", iss.number, c.completeLabel)
			}
		}
		if len(selected) == 0 {
			fmt.Printf("no unblocked '%s' issues to drain — nothing to do.\n", c.label)
			return nil
		}
		fmt.Printf("==> draining %d unblocked issue(s) (MAX_JOBS=%d)\n", len(selected), c.maxJobs)
		fanOut(c, fc, pwd, selected)
		printOutcomeReport(c, fc, pwd, selected)
	} else if hasEdges {
		// MAX_JOBS = 0 with dependency edges: multi-wave dispatch.
		if node, cycle := detectCycle(edges, issueNums(issues)); cycle {
			return fmt.Errorf("ERROR: dependency cycle detected (issue #%s is in the cycle)", node)
		}
		fmt.Println("==> dependency edges found; dispatching in waves")
		if err := dispatchWaves(c, fc, pwd, issues, edges); err != nil {
			return err
		}
		printOutcomeReport(c, fc, pwd, issues)
	} else {
		// MAX_JOBS = 0, no declared edges: original single-wave fan-out.
		fmt.Printf("==> %d issue(s); launching up to %d container(s) at a time\n", len(issues), c.maxParallel)
		fanOut(c, fc, pwd, issues)
		printOutcomeReport(c, fc, pwd, issues)
	}

	fmt.Printf("==> all agents finished — branches pushed and PRs opened on %s.\n", c.repoSlug)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}
