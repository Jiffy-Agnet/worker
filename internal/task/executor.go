// Package task implements the task execution flow described in
// docs/adr/ADR-distributed-stateless-workers.md, "Task execution flow".
package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// Redis Stream. This mirrors the producer's actual payload exactly —
// there is no separate active-branch, sandbox-image, or system-prompt
// field: branch handling is entirely the code agent's job (see the
// repo's own AGENTS.md for its instructions), and the sandbox image is a
// Worker-level default (see config.Config.SandboxImage).
type Descriptor struct {
	Repo     RepoInfo     `json:"repo"`
	Issue    IssueInfo    `json:"issue"`
	Callback CallbackInfo `json:"callback"`
}

// RepoInfo carries the repository location and a short-lived credential
// scoped to this task. Never persisted to the shared mirror cache (see
// internal/gitrepo) — only into this task's own ephemeral working copy.
type RepoInfo struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Username string `json:"username"`
}

// IssueInfo is the Issue's own text plus its full comment thread. The
// code agent's task content is composed directly from this.
type IssueInfo struct {
	Text            string      `json:"text"`
	Turns           []IssueTurn `json:"turns"`
	ExternalIssueID string      `json:"external_issue_id"`
}

// IssueTurn is a single comment in the Issue's thread.
type IssueTurn struct {
	Role      string    `json:"role"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// CallbackInfo is where and how to report the result.
type CallbackInfo struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

type Executor struct {
	cfg      *config.Config
	callback *callback.Client
}

func NewExecutor(cfg *config.Config) *Executor {
	return &Executor{cfg: cfg, callback: callback.NewClient(cfg)}
}

// HandleAsynqTask implements the Task execution flow from the ADR.
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

	taskFile, err := e.writeTaskFile(d)
	if err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("write task file: %w", err))
	}
	defer os.Remove(taskFile)

	if err := e.runPreSetupScript(ctx, repoDir, d); err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("pre-setup script: %w", err))
	}

	result, err := sandbox.Run(ctx, sandbox.RunOptions{
		Image:           e.cfg.SandboxImage,
		RepoDir:         repoDir,
		TaskFile:        taskFile,
		IssueID:         d.Issue.ExternalIssueID,
		MemoryLimit:     e.cfg.SandboxMemoryLimit,
		MemorySwapLimit: e.cfg.SandboxMemorySwapLimit,
		CPULimit:        e.cfg.SandboxCPULimit,
		Cleanup:         e.cfg.SandboxCleanup,
		ContainerTTL:    e.cfg.SandboxContainerTTL,
		ExtraEnv:        e.cfg.SandboxExtraEnv,
	})
	if err != nil {
		return e.reportFailure(ctx, d, fmt.Errorf("sandbox run: %w", err))
	}

	return e.callback.Report(ctx, d.Callback.URL, d.Callback.Secret, result)
}

// Step 1: make sure a shared, up-to-date mirror of the repo exists
// locally (fetching it if already cached), then clone a fresh, isolated
// working copy for this task alone from that local mirror, with the
// push/pull credential configured directly into it. Concurrent tasks on
// the same repository never share a working directory this way — one
// task's checkout, changes, or failure can never touch another's. See
// internal/gitrepo for the implementation.
func (e *Executor) cloneOrUpdate(ctx context.Context, d Descriptor) (string, error) {
	cacheDir, err := gitrepo.Ensure(ctx, gitrepo.EnsureOptions{
		RepoURL:  d.Repo.URL,
		Username: d.Repo.Username,
		Token:    d.Repo.Token,
		CacheDir: e.cfg.RepoCacheDir,
	})
	if err != nil {
		return "", err
	}

	workDir, err := os.MkdirTemp("", "jiffy-worker-task-*")
	if err != nil {
		return "", fmt.Errorf("prepare task working copy: %w", err)
	}
	if err := gitrepo.NewWorkingCopy(ctx, cacheDir, d.Repo.URL, d.Repo.Username, d.Repo.Token, workDir); err != nil {
		_ = os.RemoveAll(workDir)
		return "", err
	}
	return workDir, nil
}

// Step 2: compose the task content from the Issue's text and comment
// thread, and write it to /tmp named with the external Issue ID, so the
// code agent reads it from disk instead of CLI args/stdin — removing any
// length limit on task content.
//
// There's no separate system-prompt file: project-specific agent
// instructions live in the repository's own AGENTS.md, which the agent
// reads directly from the mounted working copy at /workspace.
func (e *Executor) writeTaskFile(d Descriptor) (string, error) {
	taskFile := filepath.Join("/tmp", fmt.Sprintf("jiffy-task-%s.md", d.Issue.ExternalIssueID))
	content := composeTaskText(d.Issue)
	if err := os.WriteFile(taskFile, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write task file: %w", err)
	}
	return taskFile, nil
}

// composeTaskText renders the Issue's own text followed by its comment
// thread, in chronological order, as a single markdown document.
func composeTaskText(issue IssueInfo) string {
	var b strings.Builder
	b.WriteString(issue.Text)
	if len(issue.Turns) > 0 {
		b.WriteString("\n\n## Conversation\n")
		for _, turn := range issue.Turns {
			fmt.Fprintf(&b, "\n### %s (%s) — %s\n%s\n",
				turn.Author, turn.Role, turn.CreatedAt.Format(time.RFC3339), turn.Body)
		}
	}
	return b.String()
}

// Step 3: run the project's pre-setup/entrypoint script inside the
// sandbox, if one exists, before the agent starts.
func (e *Executor) runPreSetupScript(ctx context.Context, repoDir string, d Descriptor) error {
	// TODO: look for a conventional path (e.g. .jiffy/pre-setup.sh) inside
	// repoDir and run it inside the sandbox if present.
	return nil
}

func (e *Executor) reportFailure(ctx context.Context, d Descriptor, cause error) error {
	return e.callback.ReportFailure(ctx, d.Callback.URL, d.Callback.Secret, cause)
}
