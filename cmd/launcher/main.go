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
	"strconv"
	"strings"
	"sync"
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

func atoi(s string, def int) int {
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
		maxJobs:         atoi(os.Getenv("MAX_JOBS"), 0),

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

// queryIssues returns open issues with the configured label, oldest first.
func queryIssues(c config) ([]issue, error) {
	cmd := exec.Command("gh", "issue", "list",
		"--repo", c.repoSlug,
		"--state", "open",
		"--label", c.label,
		"--limit", "100",
		"--json", "number,title",
		"--jq", "sort_by(.number) | .[] | [.number, .title] | @tsv",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	var issues []issue
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		issues = append(issues, issue{number: parts[0], title: parts[1]})
	}
	return issues, nil
}

// swapLabel transitions an issue between lifecycle labels. Best-effort.
func swapLabel(c config, num, add, remove string) {
	cmd := exec.Command("gh", "issue", "edit", num,
		"--repo", c.repoSlug,
		"--add-label", add,
		"--remove-label", remove,
	)
	if err := cmd.Run(); err != nil {
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
func outcomeLine(logPath string) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	var last string
	scanner := bufio.NewScanner(f)
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

func queryPRState(pr string) string {
	cmd := exec.Command("gh", "pr", "view", pr, "--json", "state", "--jq", ".state")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func queryIssueLabels(c config, num string) []string {
	cmd := exec.Command("gh", "issue", "view", num,
		"--repo", c.repoSlug,
		"--json", "labels",
		"--jq", ".labels[].name",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var labels []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			labels = append(labels, l)
		}
	}
	return labels
}

func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

func printOutcomeReport(c config, pwd string, issues []issue) {
	fmt.Println("==> outcome report")
	for _, iss := range issues {
		logPath := filepath.Join(pwd, "logs", "issue-"+iss.number+".log")
		line := outcomeLine(logPath)
		if line == "" {
			fmt.Printf("    #%s  status=missing  note=no SPINDRIFT_OUTCOME in log\n", iss.number)
			continue
		}
		pr := field(line, "pr")
		status := field(line, "status")
		note := noteField(line)

		switch status {
		case "blocked":
			fmt.Printf("    #%s  pr=%s  status=%s  !! %s\n", iss.number, pr, status, note)
		case "merged":
			prState := queryPRState(pr)
			issueLabels := queryIssueLabels(c, iss.number)
			if prState == "MERGED" && containsLabel(issueLabels, c.completeLabel) {
				fmt.Printf("    #%s  pr=%s  status=verified-merged\n", iss.number, pr)
			} else {
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
				fmt.Printf("    #%s  pr=%s  status=failed  !! %s\n", iss.number, pr, reason)
				swapLabel(c, iss.number, c.failedLabel, c.inProgressLabel)
			}
		default:
			fmt.Printf("    #%s  pr=%s  status=%s\n", iss.number, pr, status)
		}
	}
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

	fmt.Printf("==> querying open '%s' issues in %s\n", c.label, c.repoSlug)
	issues, err := queryIssues(c)
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		fmt.Printf("no open '%s' issues — nothing to do.\n", c.label)
		return nil
	}

	// Apply MAX_JOBS cap (single-wave: no blocker detection).
	if c.maxJobs > 0 && len(issues) > c.maxJobs {
		issues = issues[:c.maxJobs]
		fmt.Printf("==> draining %d unblocked issue(s) (MAX_JOBS=%d)\n", len(issues), c.maxJobs)
	} else {
		fmt.Printf("==> %d issue(s); launching up to %d container(s) at a time\n", len(issues), c.maxParallel)
	}

	if err := os.MkdirAll(filepath.Join(pwd, "logs"), 0o755); err != nil {
		return err
	}

	// Claim the lifecycle label synchronously before goroutine launch so a
	// concurrent `run` invocation skips the issue immediately.
	sem := make(chan struct{}, c.maxParallel)
	var wg sync.WaitGroup
	for _, iss := range issues {
		swapLabel(c, iss.number, c.inProgressLabel, c.label)
		wg.Add(1)
		iss := iss
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := runOne(c, pwd, iss); err != nil {
				fmt.Printf("    !! #%s FAILED (logs/issue-%s.log)\n", iss.number, iss.number)
				swapLabel(c, iss.number, c.failedLabel, c.inProgressLabel)
			} else {
				fmt.Printf("    <- #%s done  (logs/issue-%s.log)\n", iss.number, iss.number)
			}
		}()
	}
	wg.Wait()

	printOutcomeReport(c, pwd, issues)
	fmt.Printf("==> all agents finished — branches pushed and PRs opened on %s.\n", c.repoSlug)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}
