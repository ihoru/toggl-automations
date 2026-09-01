package entrylist

import (
	"context"
	"testing"
	"time"

	"github.com/ihoru/toggl-automations/internal/toggl"
)

type fakeAPI struct {
	start time.Time
	end   time.Time
}

func (api *fakeAPI) Me(context.Context) (toggl.User, error) {
	return toggl.User{ID: 3, Timezone: "UTC"}, nil
}

func (api *fakeAPI) Projects(context.Context) ([]toggl.Project, error) {
	return []toggl.Project{{ID: 42, Name: "Build", ClientName: "PSA"}}, nil
}

func (api *fakeAPI) TimeEntries(_ context.Context, start, end time.Time) ([]toggl.TimeEntry, error) {
	api.start = start
	api.end = end
	stop := "2026-09-01T09:05:30Z"
	return []toggl.TimeEntry{
		{
			ID:          2,
			ProjectID:   0,
			Start:       "2026-09-01T11:15:00Z",
			Stop:        nil,
			Duration:    -1,
			Description: "Running",
		},
		{
			ID:          1,
			ProjectID:   42,
			Start:       "2026-09-01T08:00:00Z",
			Stop:        &stop,
			Duration:    3930,
			Description: "Older",
		},
	}, nil
}

func TestServiceListsOldestFirstAndComputesRunningDuration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	api := &fakeAPI{}
	entries, err := NewServiceWithNow(api, func() time.Time { return now }).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !api.start.Equal(now.Add(-48*time.Hour)) || !api.end.Equal(now) {
		t.Fatalf("range = %s to %s", api.start, api.end)
	}
	if len(entries) != 2 || entries[0].ID != 1 || entries[1].ID != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].ClientName != "PSA" || entries[0].ProjectName != "Build" || entries[0].Duration != 65*time.Minute+30*time.Second {
		t.Fatalf("completed entry = %#v", entries[0])
	}
	if !entries[1].Running || entries[1].ClientName != "-" || entries[1].ProjectName != "-" || entries[1].Duration != 45*time.Minute {
		t.Fatalf("running entry = %#v", entries[1])
	}
}
