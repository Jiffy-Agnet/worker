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
	// This is a fresh clone made just for this one task (see
	// cloneOrUpdate) — remove it once we're done regardless of outcome,
	// so it never lingers or gets confused with another task's copy.
	defer os.RemoveAll(repoDir)

	sandboxEnv := e.checkoutAndConfigure(d)

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

// Step 1: make sure a shared, up-to-date mirror of the repo exists
// locally (fetching it if already cached), then clone a fresh, isolated
// working copy for this task alone from that local mirror. Concurrent
// tasks on the same repository never share a working directory this way
// — one task's checkout, changes, or failure can never touch another's.
// See internal/gitrepo for the implementation.
func (e *Executor) cloneOrUpdate(ctx context.Context, d Descriptor) (string, error) {
	cacheDir, err := gitrepo.Ensure(ctx, gitrepo.EnsureOptions{
		RepoURL:    d.RepoURL,
		Credential: d.Credential,
		CacheDir:   e.cfg.RepoCacheDir,
	})
	if err != nil {
		return "", err
	}

	workDir, err := os.MkdirTemp("", "jiffy-worker-task-*")
	if err != nil {
		return "", fmt.Errorf("prepare task working copy: %w", err)
	}
	if err := gitrepo.NewWorkingCopy(ctx, cacheDir, d.RepoURL, workDir); err != nil {
		_ = os.RemoveAll(workDir)
		return "", err
	}
	return workDir, nil
}

// Step 2: configure the Sandbox's environment variables for this run.
//
// Switching to (or creating) the Active Branch — and everything after
// that, commit/push/PR creation — is the code agent's own job inside the
// sandbox, driven by the task and system prompt. Worker doesn't run git
// checkout itself; it just makes sure the agent has what it needs to do
// that: which branch to work with, and the credential to push with.
func (e *Executor) checkoutAndConfigure(d Descriptor) map[string]string {
	return mergeSandboxEnv(d.SandboxEnv, d.ActiveBranch, d.Credential)
}

// mergeSandboxEnv layers well-known Worker-provided variables on top of
// the descriptor's own SandboxEnv, without mutating the caller's map.
func mergeSandboxEnv(base map[string]string, activeBranch, credential string) map[string]string {
	env := make(map[string]string, len(base)+2)
	for k, v := range base {
		env[k] = v
	}
	env["JIFFY_ACTIVE_BRANCH"] = activeBranch
	env["JIFFY_GIT_CREDENTIAL"] = credential
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
