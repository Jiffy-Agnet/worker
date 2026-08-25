package task

import (
	"os"
	"testing"
)

func TestWriteTaskFiles(t *testing.T) {
	e := &Executor{}
	d := Descriptor{
		IssueID:      "99",
		TaskText:     "do the thing",
		SystemPrompt: "you are a helpful agent",
	}

	taskFile, promptFile, err := e.writeTaskFiles(d)
	if err != nil {
		t.Fatalf("writeTaskFiles: %v", err)
	}
	defer os.Remove(taskFile)
	defer os.Remove(promptFile)

	if want := "/tmp/jiffy-task-99.md"; taskFile != want {
		t.Errorf("taskFile = %q, want %q", taskFile, want)
	}
	if want := "/tmp/jiffy-prompt-99.md"; promptFile != want {
		t.Errorf("promptFile = %q, want %q", promptFile, want)
	}

	gotTask, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("read task file: %v", err)
	}
	if string(gotTask) != d.TaskText {
		t.Errorf("task file content = %q, want %q", gotTask, d.TaskText)
	}

	gotPrompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("read prompt file: %v", err)
	}
	if string(gotPrompt) != d.SystemPrompt {
		t.Errorf("prompt file content = %q, want %q", gotPrompt, d.SystemPrompt)
	}
}

func TestWriteTaskFilesNamesIncludeIssueID(t *testing.T) {
	e := &Executor{}
	taskFile, promptFile, err := e.writeTaskFiles(Descriptor{IssueID: "abc123"})
	if err != nil {
		t.Fatalf("writeTaskFiles: %v", err)
	}
	defer os.Remove(taskFile)
	defer os.Remove(promptFile)

	if want := "/tmp/jiffy-task-abc123.md"; taskFile != want {
		t.Errorf("taskFile = %q, want %q", taskFile, want)
	}
	if want := "/tmp/jiffy-prompt-abc123.md"; promptFile != want {
		t.Errorf("promptFile = %q, want %q", promptFile, want)
	}
}

func TestMergeSandboxEnv(t *testing.T) {
	base := map[string]string{"FOO": "bar"}
	got := mergeSandboxEnv(base, "develop", "sekret-token")

	if got["FOO"] != "bar" {
		t.Errorf(`env["FOO"] = %q, want "bar"`, got["FOO"])
	}
	if got["JIFFY_ACTIVE_BRANCH"] != "develop" {
		t.Errorf(`env["JIFFY_ACTIVE_BRANCH"] = %q, want "develop"`, got["JIFFY_ACTIVE_BRANCH"])
	}
	if got["JIFFY_GIT_CREDENTIAL"] != "sekret-token" {
		t.Errorf(`env["JIFFY_GIT_CREDENTIAL"] = %q, want "sekret-token"`, got["JIFFY_GIT_CREDENTIAL"])
	}
	if _, mutated := base["JIFFY_ACTIVE_BRANCH"]; mutated {
		t.Error("mergeSandboxEnv mutated the caller's base map")
	}
}
