package gitrepo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCacheKey(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://github.com/Jiffy-Agnet/worker.git", "Jiffy-Agnet_worker"},
		{"https://github.com/Jiffy-Agnet/worker", "Jiffy-Agnet_worker"},
	}
	for _, c := range cases {
		if got := cacheKey(c.url); got != c.want {
			t.Errorf("cacheKey(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestCacheKeyFallsBackToHashForUnparseableURL(t *testing.T) {
	// "%zz" is an invalid percent-escape, which net/url.Parse rejects
	// outright — this exercises the sha256 fallback path.
	got := cacheKey("https://example.com/%zzbad")
	if len(got) != 16 {
		t.Errorf("cacheKey fallback = %q, want 16 hex chars", got)
	}
}

func TestRepoHost(t *testing.T) {
	if got := repoHost("https://github.com/owner/repo.git"); got != "github.com" {
		t.Errorf("repoHost = %q, want github.com", got)
	}
}

func TestAuthArgsEmptyWithoutToken(t *testing.T) {
	if got := authArgs(EnsureOptions{RepoURL: "https://github.com/owner/repo.git", Username: "x-access-token"}); got != nil {
		t.Errorf("authArgs with no token = %v, want nil", got)
	}
}

func TestBasicAuthHeader(t *testing.T) {
	got := basicAuthHeader("alice", "secret")
	if got == "" || got == "Authorization: basic " {
		t.Errorf("basicAuthHeader produced an empty/incomplete header: %q", got)
	}
}

func TestIsMirror(t *testing.T) {
	dir := t.TempDir()
	if isMirror(dir) {
		t.Error("an empty dir should not look like a mirror")
	}
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isMirror(dir) {
		t.Error("a dir with a HEAD file should look like a mirror")
	}
}
