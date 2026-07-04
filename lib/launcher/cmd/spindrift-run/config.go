package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Config struct {
	// Auth
	GHToken    string
	OAuthToken string
	APIKey     string

	// Git identity
	GitUserName  string
	GitUserEmail string

	// Required runtime config
	Repo    string
	Runtime string

	// OCI-specific (baked by nix preamble; empty for bwrap)
	ImageArchive    string
	ImageTag        string
	ImageDRV        string
	NixBuilderImage string
	NixVolume       string
	FlakeImageAttr  string

	// bwrap-specific (baked by nix preamble; empty for OCI)
	AgentFiles    string
	AgentEnv      string
	BakedPrefetch string

	// Dispatch config (set by nix runDefaultsPreamble)
	Label           string
	MaxParallel     int
	MaxJobs         int
	BaseBranch      string
	BranchPrefix    string
	InProgressLabel string
	FailedLabel     string
	CompleteLabel   string

	// Agent model config
	Model       string
	ScoutModel  string
	ReviewModel string

	// Dependency-wave config
	DepsWaitSecs int
	DepsPollSecs int

	// Prompt override (host-side bind-mount)
	PromptDir string
}

func loadConfig() (*Config, error) {
	// harness.env (highest priority) overrides the nix preamble env vars
	if err := loadHarnessEnv(); err != nil {
		return nil, err
	}

	cfg := &Config{
		GHToken:    os.Getenv("GH_TOKEN"),
		OAuthToken: os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"),
		APIKey:     os.Getenv("ANTHROPIC_API_KEY"),

		GitUserName:  os.Getenv("GIT_USER_NAME"),
		GitUserEmail: os.Getenv("GIT_USER_EMAIL"),

		Repo:    os.Getenv("REPO_SLUG"),
		Runtime: os.Getenv("RUNTIME"),

		ImageArchive:    getenvOr("IMAGE_ARCHIVE", ""),
		ImageTag:        getenvOr("IMAGE_TAG", "spindrift:latest"),
		ImageDRV:        os.Getenv("IMAGE_DRV"),
		NixBuilderImage: getenvOr("NIX_BUILDER_IMAGE", "docker.io/nixos/nix:latest"),
		NixVolume:       getenvOr("NIX_VOLUME", "spindrift-nix"),
		FlakeImageAttr:  os.Getenv("FLAKE_IMAGE_ATTR"),

		AgentFiles:    os.Getenv("AGENT_FILES"),
		AgentEnv:      os.Getenv("AGENT_ENV"),
		BakedPrefetch: os.Getenv("BAKED_PREFETCH"),

		Label:           os.Getenv("LABEL"),
		MaxParallel:     intenv("MAX_PARALLEL", 3),
		MaxJobs:         intenv("MAX_JOBS", 0),
		BaseBranch:      os.Getenv("BASE_BRANCH"),
		BranchPrefix:    os.Getenv("BRANCH_PREFIX"),
		InProgressLabel: os.Getenv("IN_PROGRESS_LABEL"),
		FailedLabel:     os.Getenv("FAILED_LABEL"),
		CompleteLabel:   os.Getenv("COMPLETE_LABEL"),

		Model:       os.Getenv("MODEL"),
		ScoutModel:  os.Getenv("SCOUT_MODEL"),
		ReviewModel: os.Getenv("REVIEW_MODEL"),

		DepsWaitSecs: intenv("DEPS_WAIT_SECS", 7200),
		DepsPollSecs: intenv("DEPS_POLL_SECS", 30),

		PromptDir: os.Getenv("SPINDRIFT_PROMPT_DIR"),
	}

	// Fall back to git config for commit identity when not set in env.
	if cfg.GitUserName == "" {
		if out, err := exec.Command("git", "config", "--get", "user.name").Output(); err == nil {
			cfg.GitUserName = strings.TrimSpace(string(out))
		}
	}
	if cfg.GitUserEmail == "" {
		if out, err := exec.Command("git", "config", "--get", "user.email").Output(); err == nil {
			cfg.GitUserEmail = strings.TrimSpace(string(out))
		}
	}

	if cfg.Repo == "" {
		return nil, fmt.Errorf("set REPO_SLUG=owner/repo (the target GitHub repository)")
	}
	if cfg.GitUserName == "" {
		return nil, fmt.Errorf("set GIT_USER_NAME, or configure git user.name on the host")
	}
	if cfg.GitUserEmail == "" {
		return nil, fmt.Errorf("set GIT_USER_EMAIL, or configure git user.email on the host")
	}
	if cfg.GHToken == "" {
		return nil, fmt.Errorf("set GH_TOKEN (fine-grained PAT scoped to the single target repo: Issues RW, Contents RW, Pull requests RW, Metadata R)")
	}
	if cfg.OAuthToken == "" && cfg.APIKey == "" {
		return nil, fmt.Errorf("set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY")
	}

	if cfg.Runtime != "" {
		if _, err := exec.LookPath(cfg.Runtime); err != nil {
			return nil, fmt.Errorf("%s not found on PATH.", cfg.Runtime)
		}
	}

	return cfg, nil
}

// loadHarnessEnv sources $PWD/harness.env (if present), overriding the
// process environment — matching `set -a; . harness.env; set +a` in bash.
func loadHarnessEnv() error {
	f, err := os.Open("harness.env")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("opening harness.env: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip optional "export " prefix
		line = strings.TrimPrefix(line, "export ")
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := line[:idx]
		val := unquote(line[idx+1:])
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("setenv %s: %w", key, err)
		}
	}
	return scanner.Err()
}

// unquote strips a single layer of surrounding single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func getenvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intenv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
