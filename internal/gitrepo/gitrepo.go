// Package gitrepo implements Step 1 of the Task execution flow (see
// docs/adr/ADR-distributed-stateless-workers.md).
//
// Two-layer design:
//   - Ensure keeps one shared *mirror* clone per repository URL on disk,
//     updated via `git fetch`. It has no working tree of its own — it
//     exists purely so later per-task clones are fast and don't need to
//     re-download the repository's full history every time. Nothing
//     sensitive is persisted here: the credential is passed as a
//     per-command HTTP Authorization header override, never written to
//     this shared, long-lived cache's own .git/config.
//   - NewWorkingCopy clones a fresh, fully independent working directory
//     for a single task, sourced from that local mirror (no network
//     needed). Concurrent tasks on the same repository never share a
//     working directory this way, so one task's checkout, changes, or
//     failure can never touch another's. Because this working copy is
//     single-task and removed once the task finishes, the credential
//     *is* configured directly into its .git/config here — so the code
//     agent can run plain `git push`/`git pull` without ever needing to
//     know the token itself.
//
// Switching to (or creating) a branch, and everything after that —
// commits, push, PR creation — is the code agent's own job inside the
// sandbox, driven by the task content and the repository's own
// AGENTS.md.
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
	// Username and Token are the short-lived credential for this task.
	// Never persisted to this shared cache's .git/config.
	Username string
	Token    string
	// CacheDir is the base directory under which the shared mirror is
	// cached between tasks. Optional — defaults to a subdirectory of
	// os.TempDir().
	CacheDir string
}

// Ensure makes sure a local *mirror* clone of RepoURL exists on disk,
// up to date with all remote refs, and returns its path.
func Ensure(ctx context.Context, opts EnsureOptions) (string, error) {
	if opts.RepoURL == "" {
		return "", fmt.Errorf("gitrepo: repo URL is required")
	}

	cacheDir := opts.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "jiffy-worker-repos")
	}

	dir := filepath.Join(cacheDir, cacheKey(opts.RepoURL))

	if isMirror(dir) {
		if err := fetchAll(ctx, dir, opts); err != nil {
			return "", fmt.Errorf("gitrepo: fetch existing mirror: %w", err)
		}
		return dir, nil
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("gitrepo: prepare cache dir: %w", err)
	}
	if err := cloneMirror(ctx, dir, opts); err != nil {
		return "", fmt.Errorf("gitrepo: mirror clone: %w", err)
	}
	return dir, nil
}

// NewWorkingCopy creates a fresh, isolated local clone at dir, sourced
// from the shared mirror cache rather than the network. origin is then
// repointed at the real repoURL, and — since this working copy is
// single-task and gets deleted afterward — the push/pull credential is
// configured directly into its .git/config, so the code agent can run
// plain git commands without ever handling the token itself. The caller
// owns dir and should remove it once the task is done.
func NewWorkingCopy(ctx context.Context, cacheDir, repoURL, username, token, dir string) error {
	if err := run(ctx, "clone", cacheDir, dir); err != nil {
		return fmt.Errorf("gitrepo: create working copy: %w", err)
	}
	if err := run(ctx, "-C", dir, "remote", "set-url", "origin", repoURL); err != nil {
		return fmt.Errorf("gitrepo: point working copy at real remote: %w", err)
	}
	if token != "" {
		host := repoHost(repoURL)
		key := fmt.Sprintf("http.https://%s/.extraHeader", host)
		if err := run(ctx, "-C", dir, "config", key, basicAuthHeader(username, token)); err != nil {
			return fmt.Errorf("gitrepo: configure push credential: %w", err)
		}
	}
	return nil
}

// isMirror reports whether dir looks like a bare/mirror repository (a
// HEAD file directly at its root, rather than a .git subdirectory).
func isMirror(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "HEAD"))
	return err == nil && !info.IsDir()
}

func cloneMirror(ctx context.Context, dir string, opts EnsureOptions) error {
	args := append(authArgs(opts), "clone", "--mirror", opts.RepoURL, dir)
	return run(ctx, args...)
}

func fetchAll(ctx context.Context, dir string, opts EnsureOptions) error {
	args := append(authArgs(opts), "-C", dir, "fetch", "origin")
	return run(ctx, args...)
}

// authArgs injects the credential as a per-command HTTP Authorization
// header override instead of embedding it in the remote URL or writing it
// into .git/config, so it never persists on disk in the shared mirror
// cache between tasks.
func authArgs(opts EnsureOptions) []string {
	if opts.Token == "" {
		return nil
	}
	host := repoHost(opts.RepoURL)
	return []string{
		"-c", fmt.Sprintf("http.https://%s/.extraHeader=%s", host, basicAuthHeader(opts.Username, opts.Token)),
	}
}

func basicAuthHeader(username, token string) string {
	basic := base64.StdEncoding.EncodeToString([]byte(username + ":" + token))
	return fmt.Sprintf("Authorization: basic %s", basic)
}

func repoHost(repoURL string) string {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		return "github.com"
	}
	return u.Host
}

// cacheKey derives a filesystem-safe, stable directory name from a repo
// URL so repeated tasks against the same repository reuse the same
// mirror cache.
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
