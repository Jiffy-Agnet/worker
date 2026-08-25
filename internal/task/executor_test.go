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
