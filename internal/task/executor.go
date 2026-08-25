// Package task implements the task execution flow described in
// docs/adr/ADR-distributed-stateless-workers.md, "Task execution flow".
package task

import (
	"context"
	"encoding/json"
	"fmt"
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

	if err := e.checkoutAndConfigure(ctx, repoDir, d); err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("branch/env setup: %w", err))
	}

	taskFile, promptFile, err := e.writeTaskFiles(d)
	if err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("write /tmp files: %w", err))
	}

	if err := e.runPreSetupScript(ctx, repoDir, d); err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("pre-setup script: %w", err))
	}

	result, err := sandbox.Run(ctx, sandbox.RunOptions{
		Image:      d.SandboxImage,
		RepoDir:    repoDir,
		Env:        d.SandboxEnv,
		TaskFile:   taskFile,
		PromptFile: promptFile,
	})
	if err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("sandbox run: %w", err))
	}

	return callback.Report(ctx, d.CallbackURL, result)
}

// Step 1: if the repo is already cloned locally, `git pull` it; otherwise
// clone fresh using the short-lived credential from the descriptor. See
// internal/gitrepo for the implementation.
func (e *Executor) cloneOrUpdate(ctx context.Context, d Descriptor) (string, error) {
	return gitrepo.Ensure(ctx, gitrepo.EnsureOptions{
		RepoURL:    d.RepoURL,
		Credential: d.Credential,
		CacheDir:   e.cfg.RepoCacheDir,
	})
}

// Step 2: check out the Active Branch and configure the Sandbox's
// environment variables for this run.
func (e *Executor) checkoutAndConfigure(ctx context.Context, repoDir string, d Descriptor) error {
	// TODO: `git checkout d.ActiveBranch`; resolve d.SandboxEnv values.
	return nil
}

// Step 3: write the task and system prompt to /tmp, named with the Issue
// ID, so the code agent reads them from disk instead of CLI args/stdin —
// removing any length limit on task content.
func (e *Executor) writeTaskFiles(d Descriptor) (taskFile, promptFile string, err error) {
	taskFile = filepath.Join("/tmp", fmt.Sprintf("jiffy-task-%s.md", d.IssueID))
	promptFile = filepath.Join("/tmp", fmt.Sprintf("jiffy-prompt-%s.md", d.IssueID))
	// TODO: os.WriteFile(taskFile, []byte(d.TaskText), 0o600); same for the prompt.
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
