package entrylist

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ihoru/toggl-automations/internal/toggl"
)

const Window = 48 * time.Hour

type API interface {
	Me(context.Context) (toggl.User, error)
	Projects(context.Context) ([]toggl.Project, error)
	TimeEntries(context.Context, time.Time, time.Time) ([]toggl.TimeEntry, error)
}

type Entry struct {
	ID          int64
	Start       time.Time
	Stop        time.Time
	Running     bool
	ProjectName string
	Duration    time.Duration
	Description string
}

type Service struct {
	api API
	now func() time.Time
}

func NewService(api API) *Service {
	return NewServiceWithNow(api, time.Now)
}

func NewServiceWithNow(api API, now func() time.Time) *Service {
	return &Service{api: api, now: now}
}

func (service *Service) List(ctx context.Context) ([]Entry, error) {
	user, err := service.api.Me(ctx)
	if err != nil {
		return nil, fmt.Errorf("load Toggl profile: %w", err)
	}
	location := time.Local
	if user.Timezone != "" {
		location, err = time.LoadLocation(user.Timezone)
		if err != nil {
			return nil, fmt.Errorf("load Toggl timezone %q: %w", user.Timezone, err)
		}
	}

	now := service.now()
	rawEntries, err := service.api.TimeEntries(ctx, now.Add(-Window), now)
	if err != nil {
		return nil, fmt.Errorf("load Toggl time entries: %w", err)
	}
	if len(rawEntries) >= 1000 {
		return nil, errors.New("Toggl returned its 1000-entry limit; the 48-hour result may be incomplete")
	}
	projects, err := service.api.Projects(ctx)
	if err != nil {
		return nil, fmt.Errorf("load Toggl projects: %w", err)
	}
	projectNames := make(map[int64]string, len(projects))
	for _, project := range projects {
		projectNames[project.ID] = project.Name
	}

	entries := make([]Entry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		start, parseErr := time.Parse(time.RFC3339Nano, raw.Start)
		if parseErr != nil {
			return nil, fmt.Errorf("parse start time for entry %d: %w", raw.ID, parseErr)
		}

		entry := Entry{
			ID:          raw.ID,
			Start:       start.In(location),
			ProjectName: projectName(raw.ProjectID, projectNames),
			Description: raw.Description,
		}
		if raw.Stop == nil || *raw.Stop == "" {
			entry.Running = true
			entry.Duration = now.Sub(start)
		} else {
			stop, stopErr := time.Parse(time.RFC3339Nano, *raw.Stop)
			if stopErr != nil {
				return nil, fmt.Errorf("parse stop time for entry %d: %w", raw.ID, stopErr)
			}
			entry.Stop = stop.In(location)
			entry.Duration = time.Duration(raw.Duration) * time.Second
			if entry.Duration <= 0 {
				entry.Duration = stop.Sub(start)
			}
		}
		if entry.Duration < 0 {
			entry.Duration = 0
		}
		entries = append(entries, entry)
	}

	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].Start.Equal(entries[right].Start) {
			return entries[left].ID < entries[right].ID
		}
		return entries[left].Start.Before(entries[right].Start)
	})
	return entries, nil
}

func projectName(projectID int64, names map[int64]string) string {
	if projectID == 0 {
		return "-"
	}
	if name := names[projectID]; name != "" {
		return name
	}
	return fmt.Sprintf("id:%d", projectID)
}
