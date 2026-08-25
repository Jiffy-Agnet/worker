// Package sandboxdetect selects which sandbox image to use for a task,
// based on marker files in the cloned repository — e.g. a Gemfile means
// Ruby, a Cargo.toml means Rust. Rules and the image each key maps to
// are entirely configuration-driven (see config.Config), so the
// community can add support for a new language purely by adding a rule
// and an image mapping — no changes to Worker's own code, and no change
// to the task payload.
//
// Detection only looks at the repository root, not subdirectories: for
// a monorepo mixing languages, this is a best-effort heuristic, not a
// guarantee. When nothing matches (or the matched key has no image
// mapped), the caller's generic default is used instead.
package sandboxdetect

import "path/filepath"

// Rule maps a marker-file glob pattern (relative to the repo root) to a
// sandbox key, e.g. {Pattern: "Cargo.toml", Key: "rust"}.
type Rule struct {
	Pattern string
	Key     string
}

// Select returns the sandbox key for the first rule (in order) whose
// pattern matches a file in repoDir, or "" if none match.
func Select(repoDir string, rules []Rule) string {
	for _, rule := range rules {
		matches, err := filepath.Glob(filepath.Join(repoDir, rule.Pattern))
		if err == nil && len(matches) > 0 {
			return rule.Key
		}
	}
	return ""
}

// Image resolves a sandbox key to an image reference via imageMap,
// falling back to fallback (the generic default) when key is empty or
// has no entry in imageMap.
func Image(key string, imageMap map[string]string, fallback string) string {
	if key == "" {
		return fallback
	}
	if img, ok := imageMap[key]; ok {
		return img
	}
	return fallback
}
