package rewrite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ihoru/toggl-automations/internal/toggl"
)

type fakeAPI struct {
	user         toggl.User
	projects     []toggl.Project
	current      *toggl.CurrentTimeEntry
	searchFn     func(toggl.SearchRequest) (toggl.SearchPage, error)
	patchFn      func([]int64, []toggl.PatchOperation) (toggl.PatchResult, error)
	searches     []toggl.SearchRequest
	patchBatches [][]int64
}

func (api *fakeAPI) Me(context.Context) (toggl.User, error) {
	return api.user, nil
}

func (api *fakeAPI) Projects(context.Context) ([]toggl.Project, error) {
	return api.projects, nil
}

func (api *fakeAPI) CurrentTimeEntry(context.Context) (*toggl.CurrentTimeEntry, error) {
	return api.current, nil
}

func (api *fakeAPI) SearchEntries(
	_ context.Context,
	_ int64,
	request toggl.SearchRequest,
) (toggl.SearchPage, error) {
	api.searches = append(api.searches, request)
	if api.searchFn == nil {
		return toggl.SearchPage{}, nil
	}
	return api.searchFn(request)
}

func (api *fakeAPI) BulkPatchTimeEntries(
	_ context.Context,
	_ int64,
	ids []int64,
	operations []toggl.PatchOperation,
) (toggl.PatchResult, error) {
	batch := append([]int64(nil), ids...)
	api.patchBatches = append(api.patchBatches, batch)
	if api.patchFn == nil {
		return toggl.PatchResult{Success: batch}, nil
	}
	return api.patchFn(ids, operations)
}

func TestServicePreviewFiltersExactlyAndSkipsRunningEntries(t *testing.T) {
	t.Parallel()

	stop := "2026-08-02T11:00:00Z"
	api := &fakeAPI{
		user: toggl.User{ID: 3, Timezone: "Asia/Tbilisi", CreatedAt: "2026-01-01T00:00:00Z"},
		projects: []toggl.Project{
			{ID: 42, Name: "Y", WorkspaceID: 7, Active: false},
			{ID: 55, Name: "J", WorkspaceID: 7, Active: true, CanTrackTime: true},
		},
		current: &toggl.CurrentTimeEntry{ID: 2},
	}
	api.searchFn = func(_ toggl.SearchRequest) (toggl.SearchPage, error) {
		return toggl.SearchPage{Rows: []toggl.ReportRow{
			{
				Description: "X",
				ProjectID:   42,
				UserID:      3,
				TimeEntries: []toggl.ReportTimeEntry{
					{ID: 1, Start: "2026-08-02T10:00:00Z", Stop: &stop, Seconds: 3600},
					{ID: 2, Start: "2026-08-03T10:00:00Z", Stop: &stop, Seconds: 3600},
					{ID: 3, Start: "2026-08-04T10:00:00Z", Stop: nil},
					{ID: 1, Start: "2026-08-02T10:00:00Z", Stop: &stop, Seconds: 3600},
				},
			},
			{Description: "x", ProjectID: 42, UserID: 3, TimeEntries: []toggl.ReportTimeEntry{{ID: 4, Start: "2026-08-02T10:00:00Z", Stop: &stop}}},
			{Description: "X", ProjectID: 99, UserID: 3, TimeEntries: []toggl.ReportTimeEntry{{ID: 5, Start: "2026-08-02T10:00:00Z", Stop: &stop}}},
			{Description: "X", ProjectID: 42, UserID: 9, TimeEntries: []toggl.ReportTimeEntry{{ID: 6, Start: "2026-08-02T10:00:00Z", Stop: &stop}}},
		}}, nil
	}

	newDescription := "Z"
	newProject := "J"
	service := NewServiceWithClock(api, func() time.Time {
		return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	})
	result, err := service.Run(context.Background(), Request{
		Description:    "X",
		Project:        "Y",
		NewDescription: &newDescription,
		NewProject:     &newProject,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].ID != 1 {
		t.Fatalf("matches = %#v", result.Matches)
	}
	if result.SkippedRunning != 2 {
		t.Fatalf("skipped running = %d", result.SkippedRunning)
	}
	if len(result.Changes) != 1 || result.Changes[0].NewDescription != "Z" || result.Changes[0].NewProjectID != 55 {
		t.Fatalf("changes = %#v", result.Changes)
	}
	if len(api.patchBatches) != 0 {
		t.Fatalf("preview made patch calls: %#v", api.patchBatches)
	}
	if result.Matches[0].Start.Location().String() != "Asia/Tbilisi" {
		t.Fatalf("location = %s", result.Matches[0].Start.Location())
	}
}

func TestServiceApplyChunksUpdatesAndReportsPartialFailures(t *testing.T) {
	t.Parallel()

	stop := "2026-08-02T11:00:00Z"
	reportEntries := make([]toggl.ReportTimeEntry, 205)
	for i := range reportEntries {
		reportEntries[i] = toggl.ReportTimeEntry{
			ID:      int64(i + 1),
			Start:   fmt.Sprintf("2026-08-02T10:%02d:00Z", i%60),
			Stop:    &stop,
			Seconds: 3600,
		}
	}
	api := &fakeAPI{
		user:     toggl.User{ID: 3, Timezone: "UTC", CreatedAt: "2026-01-01T00:00:00Z"},
		projects: []toggl.Project{{ID: 42, Name: "Y", WorkspaceID: 7, Active: true, CanTrackTime: true}},
	}
	api.searchFn = func(_ toggl.SearchRequest) (toggl.SearchPage, error) {
		return toggl.SearchPage{Rows: []toggl.ReportRow{{
			Description: "X",
			ProjectID:   42,
			UserID:      3,
			TimeEntries: reportEntries,
		}}}, nil
	}
	api.patchFn = func(ids []int64, operations []toggl.PatchOperation) (toggl.PatchResult, error) {
		if len(operations) != 1 || operations[0].Path != "/description" {
			t.Fatalf("operations = %#v", operations)
		}
		result := toggl.PatchResult{}
		for _, id := range ids {
			if id == 101 {
				result.Failure = append(result.Failure, toggl.PatchFailure{ID: id, Message: "locked"})
			} else {
				result.Success = append(result.Success, id)
			}
		}
		return result, nil
	}

	newDescription := "Z"
	service := NewServiceWithClock(api, func() time.Time {
		return time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	})
	result, err := service.Run(context.Background(), Request{
		Description:    "X",
		Project:        "id:42",
		NewDescription: &newDescription,
		Apply:          true,
	})
	var partial *PartialFailureError
	if !errors.As(err, &partial) || partial.Count != 1 {
		t.Fatalf("error = %#v", err)
	}
	if got := []int{len(api.patchBatches[0]), len(api.patchBatches[1]), len(api.patchBatches[2])}; !reflect.DeepEqual(got, []int{100, 100, 5}) {
		t.Fatalf("batch sizes = %#v", got)
	}
	if len(result.Succeeded) != 204 || len(result.Failures) != 1 || result.Failures[0].ID != 101 {
		t.Fatalf("result success=%d failures=%#v", len(result.Succeeded), result.Failures)
	}
}

func TestServiceRejectsAmbiguousProjectName(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{
		user: toggl.User{ID: 3, Timezone: "UTC", CreatedAt: "2026-01-01T00:00:00Z"},
		projects: []toggl.Project{
			{ID: 42, Name: "Y", WorkspaceID: 7},
			{ID: 43, Name: "Y", WorkspaceID: 8},
		},
	}
	_, err := NewService(api).Run(context.Background(), Request{Description: "X", Project: "Y"})
	if err == nil || !containsText(err.Error(), "id:42") || !containsText(err.Error(), "id:43") {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceRejectsRepeatedPaginationCursor(t *testing.T) {
	t.Parallel()

	next := int64(50)
	api := &fakeAPI{
		user:     toggl.User{ID: 3, Timezone: "UTC", CreatedAt: "2026-01-01T00:00:00Z"},
		projects: []toggl.Project{{ID: 42, Name: "Y", WorkspaceID: 7}},
		searchFn: func(toggl.SearchRequest) (toggl.SearchPage, error) {
			return toggl.SearchPage{NextRow: &next}, nil
		},
	}
	service := NewServiceWithClock(api, func() time.Time {
		return time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	})
	_, err := service.Run(context.Background(), Request{Description: "X", Project: "Y"})
	if err == nil || !containsText(err.Error(), "repeated pagination cursor") {
		t.Fatalf("error = %v", err)
	}
}

func TestDateWindowsCoverMultipleYearsWithBoundedRanges(t *testing.T) {
	t.Parallel()

	start := time.Date(2024, time.January, 10, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	windows := dateWindows(start, end)
	if len(windows) != 3 {
		t.Fatalf("windows = %#v", windows)
	}
	if !windows[0].start.Equal(start) || !windows[len(windows)-1].end.Equal(end) {
		t.Fatalf("windows do not cover requested range: %#v", windows)
	}
	for i, window := range windows {
		if window.end.Sub(window.start) > 366*24*time.Hour {
			t.Fatalf("window %d is too large: %#v", i, window)
		}
	}
}

func containsText(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
