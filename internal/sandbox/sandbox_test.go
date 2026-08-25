package sandbox

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestResultFilePath(t *testing.T) {
	got := resultFilePath("42")
	want := "/tmp/jiffy-result-42.json"
	if got != want {
		t.Errorf("resultFilePath(42) = %q, want %q", got, want)
	}
}

func TestRunFailsClearlyWhenTaskFileMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.md")

	_, err := Run(context.Background(), RunOptions{
		Image:      "irrelevant:latest",
		IssueID:    "1",
		TaskFile:   missing,
		PromptFile: missing,
	})
	if err == nil {
		t.Fatal("expected an error for a missing task file, got nil")
	}
	if !strings.Contains(err.Error(), "task file not found") {
		t.Errorf("error = %q, want it to mention the missing task file", err.Error())
	}
}

func TestRunRequiresImageAndIssueID(t *testing.T) {
	if _, err := Run(context.Background(), RunOptions{IssueID: "1"}); err == nil {
		t.Error("expected an error when Image is empty")
	}
	if _, err := Run(context.Background(), RunOptions{Image: "x:latest"}); err == nil {
		t.Error("expected an error when IssueID is empty")
	}
}
