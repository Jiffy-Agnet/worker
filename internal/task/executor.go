// Package task implements the task execution flow described in
// docs/adr/ADR-distributed-stateless-workers.md, "Task execution flow".
package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Jiffy-Agnet/worker/internal/callback"
	"github.com/Jiffy-Agnet/worker/internal/config"
	"github.com/Jiffy-Agnet/worker/internal/gitrepo"
	"github.com/Jiffy-Agnet/worker/internal/sandbox"

	"github.com/hibiken/asynq"
)

// TypeExecute is the asynq task type used for the local queue after a
// message has been bridged in from the shared Redis Stream.
const TypeExecute = "jiffy:execute"

// Descriptor is the JSON contract published by the producer on the shared
// Redis Stream. It is intentionally independent of both Celery's and
// asynq's own message formats — see the ADR, "Dispatch protocol".
type Descriptor struct {
	SchemaVersion  int               `json:"schema_version"`
	IssueID        string            `json:"issue_id"`
	RepoURL        string            `json:"repo_url"`
	Credential     string            `json:"credential"` // short-lived, narrowly-scoped
	ActiveBranch   string            `json:"active_branch"`
	TaskText       string            `json:"task_text"`
	SystemPrompt   string            `json:"system_prompt"`
	SandboxImage   string            `json:"sandbox_image"`
	SandboxEnv     map[string]string `json:"sandbox_env"`
	ProjectLockKey string            `json:"project_lock_key,omitempty"` // see ADR item 6
	CallbackURL    string            `json:"callback_url"`
}

type Executor struct {
	cfg *config.Config
}

func NewExecutor(cfg *config.Config) *Executor {
	return &Executor{cfg: cfg}
}

// HandleAsynqTask implements the 5-step flow from the ADR's "Task execution
// flow" section. Each step below is a stub to be filled in.
func (e *Executor) HandleAsynqTask(ctx context.Context, t *asynq.Task) error {
	var d Descriptor
	if err := json.Unmarshal(t.Payload(), &d); err != nil {
		return fmt.Errorf("unmarshal descriptor: %w", err)
	}

	repoDir, err := e.cloneOrUpdate(ctx, d)
	if err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("clone/update: %w", err))
	}

	sandboxEnv, err := e.checkoutAndConfigure(ctx, repoDir, d)
	if err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("branch/env setup: %w", err))
	}

	taskFile, promptFile, err := e.writeTaskFiles(d)
	if err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("write /tmp files: %w", err))
	}
	// These carry the task/prompt content for exactly this one run; clean
	// them up once we're done regardless of outcome, so a busy Worker
	// doesn't accumulate them under /tmp over time.
	defer os.Remove(taskFile)
	defer os.Remove(promptFile)

	if err := e.runPreSetupScript(ctx, repoDir, d); err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("pre-setup script: %w", err))
	}

	result, err := sandbox.Run(ctx, sandbox.RunOptions{
		Image:       d.SandboxImage,
		RepoDir:     repoDir,
		Env:         sandboxEnv,
		TaskFile:    taskFile,
		PromptFile:  promptFile,
		IssueID:     d.IssueID,
		MemoryLimit: e.cfg.SandboxMemoryLimit,
		CPULimit:    e.cfg.SandboxCPULimit,
	})
	if err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("sandbox run: %w", err))
	}

	return callback.Report(ctx, d.CallbackURL, result)
}

// Step 1: if the repo is already cloned locally, fetch its latest refs;
// otherwise clone fresh using the short-lived credential from the
// descriptor. Does not touch the working tree/branch — see Step 2
// (checkoutAndConfigure) for that. See internal/gitrepo for the
// implementation.
func (e *Executor) cloneOrUpdate(ctx context.Context, d Descriptor) (string, error) {
	return gitrepo.Ensure(ctx, gitrepo.EnsureOptions{
		RepoURL:    d.RepoURL,
		Credential: d.Credential,
		CacheDir:   e.cfg.RepoCacheDir,
	})
}

// Step 2: check out the Active Branch and configure the Sandbox's
// environment variables for this run.
//
// The Active Branch is expected to already exist on the remote (e.g.
// "develop", or a branch from an earlier phase) — see internal/gitrepo,
// which resets the working tree to match it exactly rather than guessing
// a base for a brand-new branch. Creating a new branch for fresh work is
// the code agent's own job, same as commit/push/PR creation.
func (e *Executor) checkoutAndConfigure(ctx context.Context, repoDir string, d Descriptor) (map[string]string, error) {
	if err := gitrepo.Checkout(ctx, repoDir, d.RepoURL, d.Credential, d.ActiveBranch); err != nil {
		return nil, err
	}
	return mergeSandboxEnv(d.SandboxEnv, d.ActiveBranch), nil
}

// mergeSandboxEnv layers well-known Worker-provided variables on top of
// the descriptor's own SandboxEnv, without mutating the caller's map, so
// the agent inside the sandbox knows which branch it's working from
// (e.g. to base a new branch on, or to push back to).
func mergeSandboxEnv(base map[string]string, activeBranch string) map[string]string {
	env := make(map[string]string, len(base)+1)
	for k, v := range base {
		env[k] = v
	}
	env["JIFFY_ACTIVE_BRANCH"] = activeBranch
	return env
}

// Step 3: write the task and system prompt to /tmp, named with the Issue
// ID, so the code agent reads them from disk instead of CLI args/stdin —
// removing any length limit on task content.
func (e *Executor) writeTaskFiles(d Descriptor) (taskFile, promptFile string, err error) {
	taskFile = filepath.Join("/tmp", fmt.Sprintf("jiffy-task-%s.md", d.IssueID))
	promptFile = filepath.Join("/tmp", fmt.Sprintf("jiffy-prompt-%s.md", d.IssueID))

	if err := os.WriteFile(taskFile, []byte(d.TaskText), 0o600); err != nil {
		return "", "", fmt.Errorf("write task file: %w", err)
	}
	if err := os.WriteFile(promptFile, []byte(d.SystemPrompt), 0o600); err != nil {
		// Don't leave a half-written pair behind.
		_ = os.Remove(taskFile)
		return "", "", fmt.Errorf("write prompt file: %w", err)
	}
	return taskFile, promptFile, nil
}

// Step 4: run the project's pre-setup/entrypoint script inside the
// sandbox, if one exists, before the agent starts.
func (e *Executor) runPreSetupScript(ctx context.Context, repoDir string, d Descriptor) error {
	// TODO: look for a conventional path (e.g. .jiffy/pre-setup.sh) inside
	// repoDir and run it inside the sandbox if present.
	return nil
}

func (e *Executor) reportFailure(ctx context.Context, d Descriptor, cause error) error {
	return callback.ReportFailure(ctx, d.CallbackURL, cause)
}
