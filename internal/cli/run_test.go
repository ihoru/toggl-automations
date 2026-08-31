package cli

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ihoru/toggl-automations/internal/rewrite"
	"github.com/ihoru/toggl-automations/internal/toggl"
)

type fakeEngine struct {
	request rewrite.Request
	result  rewrite.Result
	err     error
}

func (engine *fakeEngine) Run(_ context.Context, request rewrite.Request) (rewrite.Result, error) {
	engine.request = request
	return engine.result, engine.err
}

func TestRunSearchPrintsOnlyLatestTenEntries(t *testing.T) {
	t.Parallel()

	entries := make([]rewrite.Entry, 12)
	for i := range entries {
		entries[i] = rewrite.Entry{
			ID:          int64(i + 1),
			Start:       time.Date(2026, time.August, 31-i, 10, 0, 0, 0, time.UTC),
			Stop:        time.Date(2026, time.August, 31-i, 11, 0, 0, 0, time.UTC),
			Duration:    time.Hour,
			Description: "X",
			ProjectID:   42,
			ProjectName: "Y",
		}
	}
	engine := &fakeEngine{result: rewrite.Result{
		Ready:         true,
		SourceProject: toggl.Project{ID: 42, Name: "Y", WorkspaceID: 7},
		Timezone:      "UTC",
		Matches:       entries,
	}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"entries", "rewrite", "--description", "X", "--project", "Y"},
		&stdout,
		&stderr,
		func(string) string { return "token" },
		func(token string) Engine {
			if token != "token" {
				t.Fatalf("token = %q", token)
			}
			return engine
		},
	)
	if status != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	if !containsOutput(stdout.String(), "Matches: 12") || !containsOutput(stdout.String(), "Latest entries shown: 10") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if engine.request.HasChanges() || engine.request.Apply {
		t.Fatalf("request = %#v", engine.request)
	}
}

func TestRunParsesPreviewReplacementFlags(t *testing.T) {
	t.Parallel()

	engine := &fakeEngine{result: rewrite.Result{
		Ready:         true,
		SourceProject: toggl.Project{ID: 42, Name: "Y", WorkspaceID: 7},
		Timezone:      "UTC",
	}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := Run(
		context.Background(),
		[]string{
			"entries", "rewrite",
			"--description", "X",
			"--project", "id:42",
			"--new-description", "Z",
			"--new-project", "J",
		},
		&stdout,
		&stderr,
		func(string) string { return "token" },
		func(string) Engine { return engine },
	)
	if status != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	if engine.request.NewDescription == nil || *engine.request.NewDescription != "Z" {
		t.Fatalf("new description = %#v", engine.request.NewDescription)
	}
	if engine.request.NewProject == nil || *engine.request.NewProject != "J" {
		t.Fatalf("new project = %#v", engine.request.NewProject)
	}
	if engine.request.Apply {
		t.Fatal("preview unexpectedly enabled apply")
	}
}

func TestRunRejectsApplyWithoutReplacementBeforeCreatingEngine(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	created := false
	status := Run(
		context.Background(),
		[]string{"entries", "rewrite", "--description", "X", "--project", "Y", "--apply"},
		&bytes.Buffer{},
		&stderr,
		func(string) string { return "token" },
		func(string) Engine {
			created = true
			return &fakeEngine{}
		},
	)
	if status != 2 || created || !containsOutput(stderr.String(), "--apply requires") {
		t.Fatalf("status=%d created=%v stderr=%q", status, created, stderr.String())
	}
}

func TestRunRequiresToken(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"entries", "rewrite", "--description", "X", "--project", "Y"},
		&bytes.Buffer{},
		&stderr,
		func(string) string { return "" },
		func(string) Engine { return &fakeEngine{} },
	)
	if status != 2 || !containsOutput(stderr.String(), "TOGGL_API_TOKEN is not set") {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
}

func TestRunPrintsPartialFailureSummary(t *testing.T) {
	t.Parallel()

	newDescription := "Z"
	engine := &fakeEngine{
		result: rewrite.Result{
			Ready:         true,
			SourceProject: toggl.Project{ID: 42, Name: "Y", WorkspaceID: 7},
			Timezone:      "UTC",
			Applied:       true,
			Succeeded:     []int64{1},
			Failures:      []rewrite.Failure{{ID: 2, Message: "locked"}},
		},
		err: &rewrite.PartialFailureError{Count: 1},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"entries", "rewrite", "--description", "X", "--project", "Y", "--new-description", newDescription, "--apply"},
		&stdout,
		&stderr,
		func(string) string { return "token" },
		func(string) Engine { return engine },
	)
	if status != 1 || !containsOutput(stdout.String(), "Failed: 1") || !containsOutput(stdout.String(), "2: locked") {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func containsOutput(output, value string) bool {
	return bytes.Contains([]byte(output), []byte(value))
}

func ExampleRun_search() {
	engine := &fakeEngine{result: rewrite.Result{
		Ready:         true,
		SourceProject: toggl.Project{ID: 42, Name: "Y", WorkspaceID: 7},
		Timezone:      "UTC",
	}}
	var output bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"entries", "rewrite", "--description", "X", "--project", "Y"},
		&output,
		&bytes.Buffer{},
		func(string) string { return "token" },
		func(string) Engine { return engine },
	)
	fmt.Println(status)
	// Output: 0
}
