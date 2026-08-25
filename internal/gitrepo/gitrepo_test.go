package gitrepo

import "testing"

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

func TestAuthArgsEmptyWithoutCredential(t *testing.T) {
	if got := authArgs(EnsureOptions{RepoURL: "https://github.com/owner/repo.git"}); got != nil {
		t.Errorf("authArgs with no credential = %v, want nil", got)
	}
}
