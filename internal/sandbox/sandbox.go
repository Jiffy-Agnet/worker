// Package sandbox manages the sandbox container lifecycle. The image is
// always pulled from a registry, never built locally — see the ADR,
// "Sandbox image: pre-built, multi-arch, registry-only".
package sandbox

import "context"

// RunOptions describes a single sandbox execution.
type RunOptions struct {
	Image      string
	RepoDir    string
	Env        map[string]string
	TaskFile   string
	PromptFile string
}

// Result is what gets sent back to the producer as a callback.
type Result struct {
	Success bool
	Report  string
	PRURL   string
}

// Run pulls the pre-built registry image and runs the code agent inside
// it, pointed at TaskFile and PromptFile instead of CLI args or stdin.
func Run(ctx context.Context, opts RunOptions) (Result, error) {
	// TODO: `docker pull opts.Image`; `docker run` mounting opts.RepoDir,
	// opts.TaskFile, opts.PromptFile, with opts.Env applied. If the sandbox
	// image is missing for this host's architecture, fail clearly rather
	// than falling back to a local build.
	return Result{}, nil
}
