// Package gitrepo implements Steps 1 and 2 of the Task execution flow (see
// docs/adr/ADR-distributed-stateless-workers.md):
//
//   - Ensure: if a repository is already cloned locally from a previous
//     run, fetch its latest refs; otherwise clone it fresh.
//   - Checkout: put the working tree into the exact state needed for the
//     task's Active Branch, regardless of whatever branch a previous
//     task left checked out in this cached clone.
//
// The short-lived credential passed in per task is never written to
// .git/config or embedded in the remote URL. It is supplied as a
// per-command HTTP Authorization header override, so nothing sensitive
// persists in the on-disk cache between tasks.
package gitrepo

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsureOptions describes a single clone-or-update request.
type EnsureOptions struct {
	// RepoURL is the repository's HTTPS clone URL, e.g.
	// "https://github.com/owner/repo.git".
	RepoURL string
	// Credential is a short-lived, narrowly-scoped access token (e.g. a
	// GitHub App installation token). Never persisted to disk.
	Credential string
	// CacheDir is the base directory under which repositories are cached
	// between tasks. Optional — defaults to a subdirectory of os.TempDir().
	CacheDir string
}

// Ensure makes sure a local clone of RepoURL exists on disk with up to
// date remote-tracking refs, and returns its path. A clone already
// present in the cache from a previous task is reused; otherwise a fresh
// clone is made.
//
// This intentionally does not touch the working tree or current branch:
// the cache is keyed only by repo URL, so whatever branch a previous task
// left checked out here has nothing to do with this task's Active
// Branch. Use Checkout for that.
func Ensure(ctx context.Context, opts EnsureOptions) (string, error) {
	if opts.RepoURL == "" {
		return "", fmt.Errorf("gitrepo: repo URL is required")
	}

	cacheDir := opts.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "jiffy-worker-repos")
	}

	dir := filepath.Join(cacheDir, cacheKey(opts.RepoURL))

	if isGitRepo(dir) {
		if err := fetchAll(ctx, dir, opts); err != nil {
			return "", fmt.Errorf("gitrepo: fetch existing clone: %w", err)
		}
		return dir, nil
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("gitrepo: prepare cache dir: %w", err)
	}
	if err := clone(ctx, dir, opts); err != nil {
		return "", fmt.Errorf("gitrepo: clone: %w", err)
	}
	return dir, nil
}

// Checkout fetches the given branch and resets dir's working tree to
// exactly match its remote state (origin/branch), discarding any local
// commits, modifications, or untracked files left over from a previous
// task that reused this cached clone. The branch is expected to already
// exist on the remote — Worker does not invent a new branch or guess a
// base for one; creating a new branch for fresh work is the code agent's
// own job, same as commit/push/PR creation.
func Checkout(ctx context.Context, dir, repoURL, credential, branch string) error {
	if branch == "" {
		return fmt.Errorf("gitrepo: active branch is required")
	}

	opts := EnsureOptions{RepoURL: repoURL, Credential: credential}

	fetchArgs := append(authArgs(opts), "-C", dir, "fetch", "origin", branch)
	if err := run(ctx, fetchArgs...); err != nil {
		return fmt.Errorf("gitrepo: fetch %s: %w", branch, err)
	}

	// -B creates the branch locally if it's new to this cache, or resets
	// it if a previous task already had it; --force discards local
	// modifications to tracked files that would otherwise block the
	// switch.
	if err := run(ctx, "-C", dir, "checkout", "-B", branch, "origin/"+branch, "--force"); err != nil {
		return fmt.Errorf("gitrepo: checkout %s: %w", branch, err)
	}

	// checkout --force doesn't touch untracked/ignored files (e.g. build
	// output from a previous task's run) — remove those too so every
	// task starts from a genuinely clean tree.
	if err := run(ctx, "-C", dir, "clean", "-fdx"); err != nil {
		return fmt.Errorf("gitrepo: clean working tree: %w", err)
	}
	return nil
}

func isGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

func clone(ctx context.Context, dir string, opts EnsureOptions) error {
	args := append(authArgs(opts), "clone", opts.RepoURL, dir)
	return run(ctx, args...)
}

func fetchAll(ctx context.Context, dir string, opts EnsureOptions) error {
	args := append(authArgs(opts), "-C", dir, "fetch", "origin")
	return run(ctx, args...)
}

// authArgs injects the credential as a per-command HTTP Authorization
// header override instead of embedding it in the remote URL or writing it
// into .git/config, so it never persists on disk between tasks.
func authArgs(opts EnsureOptions) []string {
	if opts.Credential == "" {
		return nil
	}
	host := repoHost(opts.RepoURL)
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + opts.Credential))
	return []string{
		"-c", fmt.Sprintf("http.https://%s/.extraHeader=Authorization: basic %s", host, basic),
	}
}

func repoHost(repoURL string) string {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		return "github.com"
	}
	return u.Host
}

// cacheKey derives a filesystem-safe, stable directory name from a repo
// URL so repeated tasks against the same repository reuse the same clone.
func cacheKey(repoURL string) string {
	trimmed := strings.TrimSuffix(repoURL, ".git")
	if u, err := url.Parse(trimmed); err == nil && u.Path != "" {
		safe := strings.Trim(strings.ReplaceAll(u.Path, "/", "_"), "_")
		if safe != "" {
			return safe
		}
	}
	sum := sha256.Sum256([]byte(repoURL))
	return hex.EncodeToString(sum[:])[:16]
}

func run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, string(out))
	}
	return nil
}
