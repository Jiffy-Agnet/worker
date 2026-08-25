// Package sandbox manages the sandbox container lifecycle. The image is
// always pulled from a registry, never built locally — see the ADR,
// "Sandbox image: pre-built, multi-arch, registry-only".
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// containerWorkspace is the fixed path inside the sandbox where the
// cloned repository is mounted. The code agent finds the project's own
// AGENTS.md here directly — Worker never copies it anywhere separately.
const containerWorkspace = "/workspace"

// RunOptions describes a single sandbox execution.
type RunOptions struct {
	Image    string
	RepoDir  string
	TaskFile string
	IssueID  string

	// MemoryLimit and CPULimit are passed straight to `docker run` as
	// --memory and --cpus. Leave empty to use the Docker daemon's
	// defaults (no limit) — running more than one sandbox concurrently
	// without these set is exactly what risks OOM-killing the host.
	MemoryLimit string
	CPULimit    string
}

// Result is what gets sent back to the producer as a callback. The
// sandbox's own entrypoint is expected to write this as JSON to the
// result file path passed in via JIFFY_RESULT_FILE.
type Result struct {
	Success bool   `json:"success"`
	Report  string `json:"report"`
	PRURL   string `json:"pr_url"`
}

// Run pulls the pre-built registry image — never building it locally —
// and runs the code agent inside it.
//
// The task and result files are bind-mounted individually at the same
// absolute path inside the container as on the host, rather than
// mounting the whole host /tmp: with more than one sandbox running
// concurrently on the same Worker, a shared /tmp mount would let one
// task's container see (and touch) another task's files, which breaks
// the isolated-execution guarantee. Mounting each file by its own path
// keeps every container's view limited to its own task.
func Run(ctx context.Context, opts RunOptions) (Result, error) {
	if opts.Image == "" {
		return Result{}, fmt.Errorf("sandbox: image is required")
	}
	if opts.IssueID == "" {
		return Result{}, fmt.Errorf("sandbox: issue ID is required")
	}

	// A bind-mount source path that doesn't exist yet gets silently
	// turned into an empty directory by Docker, which then fails much
	// later in a confusing way — check up front and fail clearly instead.
	if _, err := os.Stat(opts.TaskFile); err != nil {
		return Result{}, fmt.Errorf("sandbox: task file not found at %s: %w", opts.TaskFile, err)
	}

	if err := pullImage(ctx, opts.Image); err != nil {
		return Result{}, fmt.Errorf("sandbox: pull image: %w", err)
	}

	resultFile := resultFilePath(opts.IssueID)
	if err := os.WriteFile(resultFile, nil, 0o600); err != nil {
		return Result{}, fmt.Errorf("sandbox: prepare result file: %w", err)
	}
	defer os.Remove(resultFile)

	if err := runContainer(ctx, opts, resultFile); err != nil {
		return Result{}, fmt.Errorf("sandbox: run container: %w", err)
	}

	return readResult(resultFile)
}

func pullImage(ctx context.Context, image string) error {
	// Never falls back to building locally — if the image is missing for
	// this host's architecture, this fails clearly instead of silently
	// building a possibly-inconsistent image (see the ADR, item 4).
	return run(ctx, "docker", "pull", image)
}

func runContainer(ctx context.Context, opts RunOptions, resultFile string) error {
	args := []string{
		"run", "--rm",
		"-v", fmt.Sprintf("%s:%s", opts.RepoDir, containerWorkspace),
		"-v", fmt.Sprintf("%s:%s:ro", opts.TaskFile, opts.TaskFile),
		"-v", fmt.Sprintf("%s:%s", resultFile, resultFile),
		"-e", fmt.Sprintf("JIFFY_REPO_DIR=%s", containerWorkspace),
		"-e", fmt.Sprintf("JIFFY_TASK_FILE=%s", opts.TaskFile),
		"-e", fmt.Sprintf("JIFFY_RESULT_FILE=%s", resultFile),
	}

	if opts.MemoryLimit != "" {
		args = append(args, "--memory", opts.MemoryLimit)
	}
	if opts.CPULimit != "" {
		args = append(args, "--cpus", opts.CPULimit)
	}

	args = append(args, opts.Image)
	return run(ctx, "docker", args...)
}

func resultFilePath(issueID string) string {
	return fmt.Sprintf("/tmp/jiffy-result-%s.json", issueID)
}

func readResult(resultFile string) (Result, error) {
	data, err := os.ReadFile(resultFile)
	if err != nil {
		return Result{}, fmt.Errorf("read result file: %w", err)
	}
	if len(data) == 0 {
		return Result{}, fmt.Errorf("sandbox: container exited without writing a result to %s", resultFile)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return Result{}, fmt.Errorf("parse result file: %w", err)
	}
	return result, nil
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out.String())
	}
	return nil
}
