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
	"time"
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

	MemoryLimit     string
	MemorySwapLimit string
	CPULimit        string

	// Cleanup controls whether the container is removed on exit
	// (`docker run --rm`). Set false to leave it running for debugging.
	Cleanup bool

	// ContainerTTL is a hard backstop on how long this container may
	// exist, independent of Cleanup and of the task's own status inside
	// it. 0 disables the backstop. Task execution itself has no
	// separate time limit — this is the only thing bounding how long a
	// container can exist.
	ContainerTTL time.Duration

	// ExtraEnv is a list of "NAME=value" pairs forwarded into the
	// container as-is (e.g. LLM provider credentials) — see
	// config.Config.SandboxExtraEnv for how these are resolved.
	ExtraEnv []string
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
	name := containerName(opts.IssueID)

	// The TTL backstop is an independent watchdog, not tied to whether
	// the foreground `docker run` below ever returns: killing the
	// docker CLI client on context cancellation does not reliably stop
	// the container itself, so an explicit `docker rm -f` is the only
	// way to guarantee this fires "regardless of the task's status
	// inside it".
	if opts.ContainerTTL > 0 {
		go func() {
			time.Sleep(opts.ContainerTTL)
			_ = run(context.Background(), "docker", "rm", "-f", name)
		}()
	}

	args := []string{"run", "--name", name}
	if opts.Cleanup {
		args = append(args, "--rm")
	}
	args = append(args,
		"-v", fmt.Sprintf("%s:%s", opts.RepoDir, containerWorkspace),
		"-v", fmt.Sprintf("%s:%s:ro", opts.TaskFile, opts.TaskFile),
		"-v", fmt.Sprintf("%s:%s", resultFile, resultFile),
		"-e", fmt.Sprintf("JIFFY_REPO_DIR=%s", containerWorkspace),
		"-e", fmt.Sprintf("JIFFY_TASK_FILE=%s", opts.TaskFile),
		"-e", fmt.Sprintf("JIFFY_RESULT_FILE=%s", resultFile),
	)

	if opts.MemoryLimit != "" {
		args = append(args, "--memory", opts.MemoryLimit)
	}
	if opts.MemorySwapLimit != "" {
		args = append(args, "--memory-swap", opts.MemorySwapLimit)
	}
	if opts.CPULimit != "" {
		args = append(args, "--cpus", opts.CPULimit)
	}
	for _, kv := range opts.ExtraEnv {
		args = append(args, "-e", kv)
	}

	args = append(args, opts.Image)
	return run(ctx, "docker", args...)
}

// containerName derives a unique, Docker-safe container name so the TTL
// watchdog (and manual debugging when Cleanup is disabled) can reliably
// target this exact container, even if the external Issue ID contains
// characters Docker doesn't allow in names.
func containerName(issueID string) string {
	return fmt.Sprintf("jiffy-sandbox-%s-%d", sanitizeForDockerName(issueID), time.Now().UnixNano())
}

func sanitizeForDockerName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "task"
	}
	return b.String()
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
