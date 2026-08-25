package task

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestWriteTaskFile(t *testing.T) {
	e := &Executor{}
	d := Descriptor{
		Issue: IssueInfo{
			Text:            "do the thing",
			ExternalIssueID: "99",
			Turns: []IssueTurn{
				{
					Role:      "user",
					Author:    "lo0ser",
					Body:      "please hurry",
					CreatedAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
				},
			},
		},
	}

	taskFile, err := e.writeTaskFile(d)
	if err != nil {
		t.Fatalf("writeTaskFile: %v", err)
	}
	defer os.Remove(taskFile)

	if want := "/tmp/jiffy-task-99.md"; taskFile != want {
		t.Errorf("taskFile = %q, want %q", taskFile, want)
	}

	got, err := os.ReadFile(taskFile)
	if err != nil {
		t.Fatalf("read task file: %v", err)
	}
	if !strings.Contains(string(got), "do the thing") {
		t.Errorf("task file missing issue text: %q", got)
	}
	if !strings.Contains(string(got), "please hurry") {
		t.Errorf("task file missing turn body: %q", got)
	}
}

func TestComposeTaskTextWithoutTurns(t *testing.T) {
	got := composeTaskText(IssueInfo{Text: "just the text"})
	if got != "just the text" {
		t.Errorf("composeTaskText = %q, want %q", got, "just the text")
	}
}

func TestComposeTaskTextIncludesAllTurns(t *testing.T) {
	issue := IssueInfo{
		Text: "main text",
		Turns: []IssueTurn{
			{Role: "user", Author: "a", Body: "first turn"},
			{Role: "user", Author: "b", Body: "second turn"},
		},
	}
	got := composeTaskText(issue)
	for _, want := range []string{"main text", "first turn", "second turn"} {
		if !strings.Contains(got, want) {
			t.Errorf("composeTaskText missing %q in output: %q", want, got)
		}
	}
}
