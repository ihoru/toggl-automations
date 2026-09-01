package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/ihoru/toggl-automations/internal/credentials"
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

type fakeCredentials struct {
	token      string
	source     credentials.Source
	loadErr    error
	saveErr    error
	deleteErr  error
	deleted    bool
	savedToken string
}

func (store *fakeCredentials) Load() (string, credentials.Source, error) {
	if store.loadErr != nil {
		return "", "", store.loadErr
	}
	if store.token == "" {
		return "", "", credentials.ErrNotFound
	}
	return store.token, store.source, nil
}

func (store *fakeCredentials) Save(token string) (credentials.Source, error) {
	if store.saveErr != nil {
		return "", store.saveErr
	}
	store.token = token
	store.savedToken = token
	if store.source == "" {
		store.source = credentials.SourceKeyring
	}
	return store.source, nil
}

func (store *fakeCredentials) Delete() (bool, error) {
	if store.deleteErr != nil {
		return false, store.deleteErr
	}
	removed := store.token != ""
	store.token = ""
	store.deleted = true
	return removed, nil
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
		Dependencies{Getenv: func(string) string { return "token" }, Factory: func(token string) Engine {
			if token != "token" {
				t.Fatalf("token = %q", token)
			}
			return engine
		}},
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

func TestRunHelpListsCommands(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"--help"},
		&stdout,
		&stderr,
		Dependencies{},
	)
	if status != 0 || stderr.Len() != 0 {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	for _, expected := range []string{"auth login", "auth status", "auth logout", "entries rewrite", "--help"} {
		if !containsOutput(stdout.String(), expected) {
			t.Fatalf("help does not contain %q: %q", expected, stdout.String())
		}
	}
}

func TestRunRewriteHelpListsOptions(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"entries", "rewrite", "--help"},
		&stdout,
		&stderr,
		Dependencies{},
	)
	if status != 0 || stderr.Len() != 0 {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
	for _, expected := range []string{"--description", "--project", "--new-description", "--new-project", "--apply"} {
		if !containsOutput(stdout.String(), expected) {
			t.Fatalf("help does not contain %q: %q", expected, stdout.String())
		}
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
		Dependencies{Getenv: func(string) string { return "token" }, Factory: func(string) Engine { return engine }},
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
		Dependencies{Getenv: func(string) string { return "token" }, Factory: func(string) Engine {
			created = true
			return &fakeEngine{}
		}},
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
		Dependencies{Factory: func(string) Engine { return &fakeEngine{} }},
	)
	if status != 2 || !containsOutput(stderr.String(), "auth login") {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
}

func TestRunUsesStoredToken(t *testing.T) {
	t.Parallel()

	store := &fakeCredentials{token: "stored-token", source: credentials.SourceKeyring}
	engine := &fakeEngine{result: rewrite.Result{
		Ready:         true,
		SourceProject: toggl.Project{ID: 42, Name: "Y", WorkspaceID: 7},
		Timezone:      "UTC",
	}}
	var stderr bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"entries", "rewrite", "--description", "X", "--project", "Y"},
		&bytes.Buffer{},
		&stderr,
		Dependencies{Credentials: store, Factory: func(token string) Engine {
			if token != "stored-token" {
				t.Fatalf("token=%q", token)
			}
			return engine
		}},
	)
	if status != 0 {
		t.Fatalf("status=%d stderr=%q", status, stderr.String())
	}
}

func TestRunAuthLoginSavesSecretWithoutPrintingIt(t *testing.T) {
	t.Parallel()

	store := &fakeCredentials{}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"auth", "login"},
		&stdout,
		&stderr,
		Dependencies{
			Credentials: store,
			ReadSecret: func(io.Writer) (string, error) {
				return "0123456789abcdef", nil
			},
		},
	)
	if status != 0 || store.savedToken != "0123456789abcdef" {
		t.Fatalf("status=%d saved=%q stderr=%q", status, store.savedToken, stderr.String())
	}
	if containsOutput(stdout.String(), store.savedToken) || !containsOutput(stdout.String(), "system keyring") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunAuthLoginCanImportEnvironment(t *testing.T) {
	t.Parallel()

	store := &fakeCredentials{source: credentials.SourceFile}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"auth", "login", "--from-env"},
		&stdout,
		&stderr,
		Dependencies{
			Credentials: store,
			Getenv: func(name string) string {
				if name == "TOGGL_API_TOKEN" {
					return "environment-token"
				}
				return ""
			},
		},
	)
	if status != 0 || store.savedToken != "environment-token" {
		t.Fatalf("status=%d saved=%q stderr=%q", status, store.savedToken, stderr.String())
	}
}

func TestRunAuthStatusMasksTokenAndReportsSource(t *testing.T) {
	t.Parallel()

	store := &fakeCredentials{token: "0123456789abcdef", source: credentials.SourceFile}
	var stdout bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"auth", "status"},
		&stdout,
		&bytes.Buffer{},
		Dependencies{Credentials: store},
	)
	if status != 0 || !containsOutput(stdout.String(), "secure config file, ...cdef") {
		t.Fatalf("status=%d stdout=%q", status, stdout.String())
	}
	if containsOutput(stdout.String(), store.token) {
		t.Fatalf("full token leaked: %q", stdout.String())
	}
}

func TestRunAuthLogoutDeletesStoredTokenAndWarnsAboutEnvironment(t *testing.T) {
	t.Parallel()

	store := &fakeCredentials{token: "stored-token"}
	var stdout bytes.Buffer
	status := Run(
		context.Background(),
		[]string{"auth", "logout"},
		&stdout,
		&bytes.Buffer{},
		Dependencies{
			Credentials: store,
			Getenv:      func(string) string { return "environment-token" },
		},
	)
	if status != 0 || !store.deleted || !containsOutput(stdout.String(), "still set") {
		t.Fatalf("status=%d deleted=%v stdout=%q", status, store.deleted, stdout.String())
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
		Dependencies{Getenv: func(string) string { return "token" }, Factory: func(string) Engine { return engine }},
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
		Dependencies{Getenv: func(string) string { return "token" }, Factory: func(string) Engine { return engine }},
	)
	fmt.Println(status)
	// Output: 0
}
