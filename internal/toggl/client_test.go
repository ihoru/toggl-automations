package toggl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestClientProjectsUsesBasicAuthAndAcceptsWrappedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v9/me/projects" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("include_archived") != "true" {
			t.Fatal("include_archived was not set")
		}
		username, password, ok := request.BasicAuth()
		if !ok || username != "secret-token" || password != "api_token" {
			t.Fatalf("unexpected basic auth: %q %q %v", username, password, ok)
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"items":[{"id":42,"name":"Build","workspace_id":7,"active":true,"can_track_time":true}]}`)
	}))
	defer server.Close()

	client := NewClientWithConfig("secret-token", Config{
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
		MaxAttempts: 1,
	})
	projects, err := client.Projects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ID != 42 || projects[0].Workspace() != 7 {
		t.Fatalf("projects = %#v", projects)
	}
}

func TestClientSearchEntriesSendsFiltersAndParsesCursor(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/reports/api/v3/workspace/7/search/time_entries" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var search SearchRequest
		if err := json.NewDecoder(request.Body).Decode(&search); err != nil {
			t.Fatal(err)
		}
		if search.Description != "X" || !reflect.DeepEqual(search.ProjectIDs, []int64{42}) || !reflect.DeepEqual(search.UserIDs, []int64{3}) {
			t.Fatalf("search = %#v", search)
		}
		writer.Header().Set("X-Next-Row-Number", "50")
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `[{"description":"X","project_id":42,"user_id":3,"time_entries":[{"id":99,"start":"2026-08-01T10:00:00Z","stop":"2026-08-01T11:00:00Z","seconds":3600}]}]`)
	}))
	defer server.Close()

	client := NewClientWithConfig("token", Config{BaseURL: server.URL, HTTPClient: server.Client(), MaxAttempts: 1})
	page, err := client.SearchEntries(context.Background(), 7, SearchRequest{
		Description: "X",
		ProjectIDs:  []int64{42},
		UserIDs:     []int64{3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || len(page.Rows[0].TimeEntries) != 1 || page.Rows[0].TimeEntries[0].ID != 99 {
		t.Fatalf("page = %#v", page)
	}
	if page.NextRow == nil || *page.NextRow != 50 {
		t.Fatalf("next row = %#v", page.NextRow)
	}
}

func TestClientTimeEntriesSendsRFC3339RangeAndNormalizesLegacyIDs(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v9/me/time_entries" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if got := request.URL.Query().Get("start_date"); got != "2026-08-30T12:00:00Z" {
			t.Fatalf("start_date = %q", got)
		}
		if got := request.URL.Query().Get("end_date"); got != "2026-09-01T12:00:00Z" {
			t.Fatalf("end_date = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"data":[{"id":99,"wid":7,"pid":42,"start":"2026-08-31T10:00:00Z","stop":"2026-08-31T11:00:00Z","duration":3600,"description":"Build"}]}`)
	}))
	defer server.Close()

	client := NewClientWithConfig("token", Config{BaseURL: server.URL, HTTPClient: server.Client(), MaxAttempts: 1})
	entries, err := client.TimeEntries(
		context.Background(),
		time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC),
		time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != 99 || entries[0].WorkspaceID != 7 || entries[0].ProjectID != 42 {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestClientBulkPatchUsesJSONPatchAndParsesPartialResult(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch || request.URL.Path != "/api/v9/workspaces/7/time_entries/11,12" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var operations []PatchOperation
		if err := json.NewDecoder(request.Body).Decode(&operations); err != nil {
			t.Fatal(err)
		}
		if len(operations) != 2 || operations[0].Path != "/description" || operations[1].Path != "/project_id" {
			t.Fatalf("operations = %#v", operations)
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"success":[11],"failure":[{"id":12,"message":"locked"}]}`)
	}))
	defer server.Close()

	client := NewClientWithConfig("token", Config{BaseURL: server.URL, HTTPClient: server.Client(), MaxAttempts: 1})
	result, err := client.BulkPatchTimeEntries(context.Background(), 7, []int64{11, 12}, []PatchOperation{
		{Op: "replace", Path: "/description", Value: "Z"},
		{Op: "replace", Path: "/project_id", Value: int64(55)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Success, []int64{11}) || len(result.Failure) != 1 || result.Failure[0].ID != 12 {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientRetriesRateLimitWithoutLeakingToken(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(writer, "try again")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"id":3,"timezone":"UTC","created_at":"2026-01-01T00:00:00Z"}`)
	}))
	defer server.Close()

	client := NewClientWithConfig("private-token", Config{
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
		MaxAttempts: 2,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})
	user, err := client.Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || user.ID != 3 {
		t.Fatalf("attempts = %d, user = %#v", attempts, user)
	}
}

func TestClientRedactsTokenFromAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		io.WriteString(writer, "bad token private-token")
	}))
	defer server.Close()

	client := NewClientWithConfig("private-token", Config{BaseURL: server.URL, HTTPClient: server.Client(), MaxAttempts: 1})
	_, err := client.Me(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got == "" || contains(got, "private-token") {
		t.Fatalf("error leaked token: %q", got)
	}
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
