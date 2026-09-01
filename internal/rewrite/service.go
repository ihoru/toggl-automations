package rewrite

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ihoru/toggl-automations/internal/toggl"
)

type API interface {
	Me(context.Context) (toggl.User, error)
	Projects(context.Context) ([]toggl.Project, error)
	CurrentTimeEntry(context.Context) (*toggl.CurrentTimeEntry, error)
	SearchEntries(context.Context, int64, toggl.SearchRequest) (toggl.SearchPage, error)
	BulkPatchTimeEntries(context.Context, int64, []int64, []toggl.PatchOperation) (toggl.PatchResult, error)
}

type Request struct {
	Description    string
	Project        string
	NewDescription *string
	NewProject     *string
	Apply          bool
}

func (r Request) HasChanges() bool {
	return r.NewDescription != nil || r.NewProject != nil
}

type Entry struct {
	ID          int64
	Start       time.Time
	Stop        time.Time
	Duration    time.Duration
	Description string
	ProjectID   int64
	ProjectName string
}

type Change struct {
	Entry
	NewDescription string
	NewProjectID   int64
	NewProjectName string
}

type Failure struct {
	ID      int64
	Message string
}

type Result struct {
	Ready           bool
	SourceProject   toggl.Project
	TargetProject   *toggl.Project
	Timezone        string
	TimezoneWarning string
	Matches         []Entry
	Changes         []Change
	SkippedRunning  int
	Applied         bool
	Succeeded       []int64
	Failures        []Failure
}

type PartialFailureError struct {
	Count int
}

func (e *PartialFailureError) Error() string {
	return fmt.Sprintf("%d time entries failed to update", e.Count)
}

type Service struct {
	api API
	now func() time.Time
}

func NewService(api API) *Service {
	return &Service{api: api, now: time.Now}
}

func NewServiceWithClock(api API, now func() time.Time) *Service {
	return &Service{api: api, now: now}
}

func (s *Service) Run(ctx context.Context, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}

	user, err := s.api.Me(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("load current Toggl user: %w", err)
	}
	if user.ID == 0 {
		return Result{}, errors.New("Toggl returned a current user without an ID")
	}

	projects, err := s.api.Projects(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("load Toggl projects: %w", err)
	}
	source, err := resolveSourceProject(request.Project, projects)
	if err != nil {
		return Result{}, err
	}

	var target *toggl.Project
	if request.NewProject != nil {
		resolved, resolveErr := resolveTargetProject(*request.NewProject, source.Workspace(), projects)
		if resolveErr != nil {
			return Result{}, resolveErr
		}
		target = &resolved
	}

	location, timezoneName, timezoneWarning := userLocation(user.Timezone)
	result := Result{
		SourceProject:   source,
		TargetProject:   target,
		Timezone:        timezoneName,
		TimezoneWarning: timezoneWarning,
	}

	current, err := s.api.CurrentTimeEntry(ctx)
	if err != nil {
		return result, fmt.Errorf("load current Toggl time entry: %w", err)
	}
	currentID := int64(0)
	if current != nil {
		currentID = current.ID
	}

	rows, err := s.searchHistory(ctx, user, source, request.Description, location)
	if err != nil {
		return result, err
	}

	seen := make(map[int64]struct{})
	for _, row := range rows {
		if row.Description != request.Description || row.ProjectID != source.ID {
			continue
		}
		for _, reportEntry := range row.TimeEntries {
			if _, duplicate := seen[reportEntry.ID]; duplicate {
				continue
			}
			seen[reportEntry.ID] = struct{}{}

			entryUserID := reportEntry.UserID
			if entryUserID == 0 {
				entryUserID = row.UserID
			}
			if entryUserID != 0 && entryUserID != user.ID {
				continue
			}

			if reportEntry.ID == currentID || reportEntry.Stop == nil || *reportEntry.Stop == "" {
				result.SkippedRunning++
				continue
			}

			entry, entryErr := makeEntry(reportEntry, row.Description, source, location)
			if entryErr != nil {
				return result, entryErr
			}
			result.Matches = append(result.Matches, entry)
		}
	}

	sort.Slice(result.Matches, func(i, j int) bool {
		if result.Matches[i].Start.Equal(result.Matches[j].Start) {
			return result.Matches[i].ID > result.Matches[j].ID
		}
		return result.Matches[i].Start.After(result.Matches[j].Start)
	})

	result.Changes = buildChanges(result.Matches, request, source, target)
	result.Ready = true
	if !request.Apply {
		return result, nil
	}

	result.Applied = true
	operations := patchOperations(request, target)
	for start := 0; start < len(result.Changes); start += 100 {
		end := min(start+100, len(result.Changes))
		batch := result.Changes[start:end]
		ids := make([]int64, len(batch))
		for i, change := range batch {
			ids[i] = change.ID
		}

		patchResult, patchErr := s.api.BulkPatchTimeEntries(ctx, source.Workspace(), ids, operations)
		if patchErr != nil {
			return result, fmt.Errorf("bulk update Toggl time entries: %w", patchErr)
		}
		result.recordPatchResult(ids, patchResult)
	}

	if len(result.Failures) != 0 {
		return result, &PartialFailureError{Count: len(result.Failures)}
	}
	return result, nil
}

func validateRequest(request Request) error {
	if request.Description == "" {
		return errors.New("--description must not be empty")
	}
	if request.Project == "" {
		return errors.New("--project must not be empty")
	}
	if request.NewDescription != nil && *request.NewDescription == "" {
		return errors.New("--new-description must not be empty")
	}
	if request.NewProject != nil && *request.NewProject == "" {
		return errors.New("--new-project must not be empty")
	}
	if request.Apply && !request.HasChanges() {
		return errors.New("--apply requires --new-description, --new-project, or both")
	}
	return nil
}

func resolveSourceProject(selector string, projects []toggl.Project) (toggl.Project, error) {
	return resolveProject(selector, projects, 0, false)
}

func resolveTargetProject(selector string, workspaceID int64, projects []toggl.Project) (toggl.Project, error) {
	project, err := resolveProject(selector, projects, workspaceID, true)
	if err != nil {
		return toggl.Project{}, err
	}
	if !project.Active {
		return toggl.Project{}, fmt.Errorf("target project %q (id:%d) is archived", project.Name, project.ID)
	}
	return project, nil
}

func resolveProject(
	selector string,
	projects []toggl.Project,
	workspaceID int64,
	target bool,
) (toggl.Project, error) {
	id, isID, err := parseSelector(selector)
	if err != nil {
		return toggl.Project{}, err
	}

	matches := make([]toggl.Project, 0, 1)
	for _, project := range projects {
		if workspaceID != 0 && project.Workspace() != workspaceID {
			continue
		}
		if (isID && project.ID == id) || (!isID && project.Name == selector) {
			matches = append(matches, project)
		}
	}

	role := "source"
	if target {
		role = "target"
	}
	if len(matches) == 0 {
		return toggl.Project{}, fmt.Errorf("%s project %q was not found", role, selector)
	}
	if len(matches) > 1 {
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].Workspace() == matches[j].Workspace() {
				return matches[i].ID < matches[j].ID
			}
			return matches[i].Workspace() < matches[j].Workspace()
		})
		choices := make([]string, len(matches))
		for i, match := range matches {
			choices[i] = fmt.Sprintf("id:%d (workspace %d)", match.ID, match.Workspace())
		}
		return toggl.Project{}, fmt.Errorf(
			"%s project name %q is ambiguous; use one of: %s",
			role,
			selector,
			strings.Join(choices, ", "),
		)
	}
	return matches[0], nil
}

func parseSelector(selector string) (int64, bool, error) {
	if !strings.HasPrefix(selector, "id:") {
		return 0, false, nil
	}
	value := strings.TrimPrefix(selector, "id:")
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, false, fmt.Errorf("invalid project selector %q; expected id:<positive number>", selector)
	}
	return id, true, nil
}

func (s *Service) searchHistory(
	ctx context.Context,
	user toggl.User,
	project toggl.Project,
	description string,
	location *time.Location,
) ([]toggl.ReportRow, error) {
	created := accountCreationDate(user.CreatedAt, location)
	today := dateOnly(s.now().In(location))
	windows := dateWindows(created, today)
	rows := make([]toggl.ReportRow, 0)

	for _, window := range windows {
		var cursor *int64
		seenCursors := make(map[int64]struct{})
		for {
			page, err := s.api.SearchEntries(ctx, project.Workspace(), toggl.SearchRequest{
				StartDate:      window.start.Format(time.DateOnly),
				EndDate:        window.end.Format(time.DateOnly),
				Description:    description,
				ProjectIDs:     []int64{project.ID},
				UserIDs:        []int64{user.ID},
				Grouped:        false,
				PageSize:       50,
				OrderBy:        "date",
				OrderDirection: "DESC",
				FirstRowNumber: cursor,
			})
			if err != nil {
				return nil, fmt.Errorf(
					"search Toggl history %s through %s: %w",
					window.start.Format(time.DateOnly),
					window.end.Format(time.DateOnly),
					err,
				)
			}
			rows = append(rows, page.Rows...)
			if page.NextRow == nil {
				break
			}
			if _, duplicate := seenCursors[*page.NextRow]; duplicate {
				return nil, fmt.Errorf("Toggl Reports API repeated pagination cursor %d", *page.NextRow)
			}
			seenCursors[*page.NextRow] = struct{}{}
			next := *page.NextRow
			cursor = &next
		}
	}
	return rows, nil
}

type dateWindow struct {
	start time.Time
	end   time.Time
}

func dateWindows(start, end time.Time) []dateWindow {
	if start.After(end) {
		start = end
	}

	windows := make([]dateWindow, 0)
	for cursor := start; !cursor.After(end); {
		windowEnd := cursor.AddDate(1, 0, -1)
		if windowEnd.After(end) {
			windowEnd = end
		}
		if !windowEnd.After(cursor) {
			windowEnd = cursor.AddDate(0, 0, 1)
		}
		windows = append(windows, dateWindow{start: cursor, end: windowEnd})
		cursor = windowEnd.AddDate(0, 0, 1)
	}
	return windows
}

func accountCreationDate(value string, location *time.Location) time.Time {
	created, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Date(1970, time.January, 1, 0, 0, 0, 0, location)
	}
	return dateOnly(created.In(location))
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func userLocation(name string) (*time.Location, string, string) {
	if name == "" {
		return time.UTC, "UTC", "Toggl profile did not provide a timezone; using UTC"
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC, "UTC", fmt.Sprintf("cannot load Toggl timezone %q; using UTC", name)
	}
	return location, name, ""
}

func makeEntry(
	reportEntry toggl.ReportTimeEntry,
	description string,
	project toggl.Project,
	location *time.Location,
) (Entry, error) {
	start, err := parseTimestamp(reportEntry.Start, location)
	if err != nil {
		return Entry{}, fmt.Errorf("parse start time for entry %d: %w", reportEntry.ID, err)
	}
	stop, err := parseTimestamp(*reportEntry.Stop, location)
	if err != nil {
		return Entry{}, fmt.Errorf("parse stop time for entry %d: %w", reportEntry.ID, err)
	}

	durationSeconds := reportEntry.Seconds
	if durationSeconds == 0 {
		durationSeconds = reportEntry.Duration
	}
	duration := time.Duration(durationSeconds) * time.Second
	if duration <= 0 {
		duration = stop.Sub(start)
	}

	return Entry{
		ID:          reportEntry.ID,
		Start:       start.In(location),
		Stop:        stop.In(location),
		Duration:    duration,
		Description: description,
		ProjectID:   project.ID,
		ProjectName: project.Name,
	}, nil
}

func parseTimestamp(value string, location *time.Location) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	return time.ParseInLocation("2006-01-02T15:04:05", value, location)
}

func buildChanges(
	entries []Entry,
	request Request,
	source toggl.Project,
	target *toggl.Project,
) []Change {
	changes := make([]Change, 0, len(entries))
	for _, entry := range entries {
		newDescription := entry.Description
		if request.NewDescription != nil {
			newDescription = *request.NewDescription
		}
		newProjectID := source.ID
		newProjectName := source.Name
		if target != nil {
			newProjectID = target.ID
			newProjectName = target.Name
		}
		if newDescription == entry.Description && newProjectID == entry.ProjectID {
			continue
		}
		changes = append(changes, Change{
			Entry:          entry,
			NewDescription: newDescription,
			NewProjectID:   newProjectID,
			NewProjectName: newProjectName,
		})
	}
	return changes
}

func patchOperations(request Request, target *toggl.Project) []toggl.PatchOperation {
	operations := make([]toggl.PatchOperation, 0, 2)
	if request.NewDescription != nil {
		operations = append(operations, toggl.PatchOperation{
			Op:    "replace",
			Path:  "/description",
			Value: *request.NewDescription,
		})
	}
	if target != nil {
		operations = append(operations, toggl.PatchOperation{
			Op:    "replace",
			Path:  "/project_id",
			Value: target.ID,
		})
	}
	return operations
}

func (result *Result) recordPatchResult(requested []int64, patch toggl.PatchResult) {
	acknowledged := make(map[int64]struct{}, len(requested))
	for _, id := range patch.Success {
		result.Succeeded = append(result.Succeeded, id)
		acknowledged[id] = struct{}{}
	}
	for _, failure := range patch.Failure {
		result.Failures = append(result.Failures, Failure{ID: failure.ID, Message: failure.Message})
		acknowledged[failure.ID] = struct{}{}
	}
	for _, id := range requested {
		if _, ok := acknowledged[id]; !ok {
			result.Failures = append(result.Failures, Failure{
				ID:      id,
				Message: "Toggl did not acknowledge this time entry",
			})
		}
	}
}
