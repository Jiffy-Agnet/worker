package sandboxdetect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectMatchesRule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	rules := []Rule{
		{Pattern: "Gemfile", Key: "ruby"},
		{Pattern: "Cargo.toml", Key: "rust"},
	}
	if got := Select(dir, rules); got != "rust" {
		t.Errorf("Select = %q, want %q", got, "rust")
	}
}

func TestSelectReturnsEmptyWhenNoRuleMatches(t *testing.T) {
	dir := t.TempDir()
	rules := []Rule{{Pattern: "Gemfile", Key: "ruby"}}
	if got := Select(dir, rules); got != "" {
		t.Errorf("Select = %q, want empty", got)
	}
}

func TestSelectHonorsRuleOrder(t *testing.T) {
	dir := t.TempDir()
	// Both markers present; the first matching rule (in the given
	// order) should win.
	for _, name := range []string{"Gemfile", "Cargo.toml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rules := []Rule{
		{Pattern: "Cargo.toml", Key: "rust"},
		{Pattern: "Gemfile", Key: "ruby"},
	}
	if got := Select(dir, rules); got != "rust" {
		t.Errorf("Select = %q, want %q (first matching rule)", got, "rust")
	}
}

func TestImageFallsBackWhenKeyEmpty(t *testing.T) {
	got := Image("", map[string]string{"rust": "jiffy-sandbox-rust:1.0.0"}, "jiffy-sandbox:1.2.3")
	if got != "jiffy-sandbox:1.2.3" {
		t.Errorf("Image = %q, want fallback", got)
	}
}

func TestImageFallsBackWhenKeyUnmapped(t *testing.T) {
	got := Image("dotnet", map[string]string{"rust": "jiffy-sandbox-rust:1.0.0"}, "jiffy-sandbox:1.2.3")
	if got != "jiffy-sandbox:1.2.3" {
		t.Errorf("Image = %q, want fallback", got)
	}
}

func TestImageUsesMappedValue(t *testing.T) {
	got := Image("rust", map[string]string{"rust": "jiffy-sandbox-rust:1.0.0"}, "jiffy-sandbox:1.2.3")
	if got != "jiffy-sandbox-rust:1.0.0" {
		t.Errorf("Image = %q, want mapped image", got)
	}
}
