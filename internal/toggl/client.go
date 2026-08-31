package toggl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL = "https://api.track.toggl.com"
	maxBodyBytes   = 4 << 20
)

type User struct {
	ID        int64  `json:"id"`
	Timezone  string `json:"timezone"`
	CreatedAt string `json:"created_at"`
}

type Project struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	WorkspaceID  int64  `json:"workspace_id"`
	LegacyWid    int64  `json:"wid"`
	Active       bool   `json:"active"`
	CanTrackTime bool   `json:"can_track_time"`
	ClientName   string `json:"client_name"`
}

func (p Project) Workspace() int64 {
	if p.WorkspaceID != 0 {
		return p.WorkspaceID
	}
	return p.LegacyWid
}

type CurrentTimeEntry struct {
	ID          int64   `json:"id"`
	Description string  `json:"description"`
	ProjectID   int64   `json:"project_id"`
	LegacyPID   int64   `json:"pid"`
	UserID      int64   `json:"user_id"`
	Duration    int64   `json:"duration"`
	Stop        *string `json:"stop"`
}

type ReportRow struct {
	Description string            `json:"description"`
	ProjectID   int64             `json:"project_id"`
	UserID      int64             `json:"user_id"`
	TimeEntries []ReportTimeEntry `json:"time_entries"`
}

type ReportTimeEntry struct {
	ID       int64   `json:"id"`
	Start    string  `json:"start"`
	Stop     *string `json:"stop"`
	Seconds  int64   `json:"seconds"`
	UserID   int64   `json:"user_id"`
	Duration int64   `json:"duration"`
}

type SearchRequest struct {
	StartDate      string  `json:"start_date"`
	EndDate        string  `json:"end_date"`
	Description    string  `json:"description"`
	ProjectIDs     []int64 `json:"project_ids"`
	UserIDs        []int64 `json:"user_ids"`
	Grouped        bool    `json:"grouped"`
	PageSize       int     `json:"page_size"`
	OrderBy        string  `json:"order_by"`
	OrderDirection string  `json:"order_dir"`
	FirstRowNumber *int64  `json:"first_row_number,omitempty"`
}

type SearchPage struct {
	Rows    []ReportRow
	NextRow *int64
}

type PatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

type PatchFailure struct {
	ID      int64  `json:"id"`
	Message string `json:"message"`
}

type PatchResult struct {
	Success []int64        `json:"success"`
	Failure []PatchFailure `json:"failure"`
}

type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("Toggl API %s %s returned HTTP %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("Toggl API %s %s returned HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func StatusCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

type Config struct {
	BaseURL     string
	HTTPClient  *http.Client
	MinInterval time.Duration
	MaxAttempts int
	Sleep       func(context.Context, time.Duration) error
	Now         func() time.Time
}

type Client struct {
	token       string
	baseURL     string
	httpClient  *http.Client
	minInterval time.Duration
	maxAttempts int
	sleep       func(context.Context, time.Duration) error
	now         func() time.Time

	requestMu   sync.Mutex
	lastRequest time.Time
}

func NewClient(token string) *Client {
	return NewClientWithConfig(token, Config{})
}

func NewClientWithConfig(token string, cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.MinInterval == 0 && cfg.BaseURL == defaultBaseURL {
		cfg.MinInterval = time.Second
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 4
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepContext
	}

	return &Client{
		token:       token,
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		httpClient:  cfg.HTTPClient,
		minInterval: cfg.MinInterval,
		maxAttempts: cfg.MaxAttempts,
		sleep:       cfg.Sleep,
		now:         cfg.Now,
	}
}

func (c *Client) Me(ctx context.Context) (User, error) {
	var user User
	_, err := c.doJSON(ctx, http.MethodGet, "/api/v9/me", nil, &user)
	return user, err
}

func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	query := url.Values{"include_archived": []string{"true"}}
	var raw json.RawMessage
	_, err := c.doJSON(ctx, http.MethodGet, "/api/v9/me/projects?"+query.Encode(), nil, &raw)
	if err != nil {
		return nil, err
	}

	var projects []Project
	if err := json.Unmarshal(raw, &projects); err == nil {
		return projects, nil
	}

	var wrapped struct {
		Items []Project `json:"items"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("decode Toggl projects: %w", err)
	}
	return wrapped.Items, nil
}

func (c *Client) CurrentTimeEntry(ctx context.Context) (*CurrentTimeEntry, error) {
	var raw json.RawMessage
	_, err := c.doJSON(ctx, http.MethodGet, "/api/v9/me/time_entries/current", nil, &raw)
	if err != nil {
		if StatusCode(err) == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return nil, nil
	}

	var entry CurrentTimeEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, fmt.Errorf("decode current Toggl time entry: %w", err)
	}
	if entry.ProjectID == 0 {
		entry.ProjectID = entry.LegacyPID
	}
	if entry.ID == 0 {
		return nil, nil
	}
	return &entry, nil
}

func (c *Client) SearchEntries(ctx context.Context, workspaceID int64, search SearchRequest) (SearchPage, error) {
	path := fmt.Sprintf("/reports/api/v3/workspace/%d/search/time_entries", workspaceID)
	var rows []ReportRow
	headers, err := c.doJSON(ctx, http.MethodPost, path, search, &rows)
	if err != nil {
		return SearchPage{}, err
	}

	page := SearchPage{Rows: rows}
	if value := headers.Get("X-Next-Row-Number"); value != "" {
		next, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			return SearchPage{}, fmt.Errorf("invalid X-Next-Row-Number %q: %w", value, parseErr)
		}
		page.NextRow = &next
	}
	return page, nil
}

func (c *Client) BulkPatchTimeEntries(
	ctx context.Context,
	workspaceID int64,
	entryIDs []int64,
	operations []PatchOperation,
) (PatchResult, error) {
	if len(entryIDs) == 0 {
		return PatchResult{}, errors.New("bulk patch requires at least one time entry ID")
	}
	if len(entryIDs) > 100 {
		return PatchResult{}, fmt.Errorf("bulk patch accepts at most 100 time entry IDs, got %d", len(entryIDs))
	}

	ids := make([]string, len(entryIDs))
	for i, id := range entryIDs {
		ids[i] = strconv.FormatInt(id, 10)
	}
	path := fmt.Sprintf(
		"/api/v9/workspaces/%d/time_entries/%s",
		workspaceID,
		strings.Join(ids, ","),
	)

	var result PatchResult
	_, err := c.doJSON(ctx, http.MethodPatch, path, operations, &result)
	return result, err
}

func (c *Client) doJSON(
	ctx context.Context,
	method string,
	path string,
	body any,
	out any,
) (http.Header, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode Toggl request: %w", err)
		}
	}

	for attempt := 0; attempt < c.maxAttempts; attempt++ {
		if err := c.waitForRateLimit(ctx); err != nil {
			return nil, err
		}

		request, requestErr := http.NewRequestWithContext(
			ctx,
			method,
			c.baseURL+path,
			bytes.NewReader(payload),
		)
		if requestErr != nil {
			return nil, fmt.Errorf("build Toggl request: %w", requestErr)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "github.com/ihoru/toggl-automations")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		request.SetBasicAuth(c.token, "api_token")

		response, requestErr := c.httpClient.Do(request)
		if requestErr != nil {
			if attempt+1 < c.maxAttempts && ctx.Err() == nil {
				if sleepErr := c.sleep(ctx, retryDelay(attempt)); sleepErr != nil {
					return nil, sleepErr
				}
				continue
			}
			return nil, fmt.Errorf("Toggl API %s %s failed: %w", method, path, requestErr)
		}

		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
		response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read Toggl API response: %w", readErr)
		}

		if response.StatusCode >= 200 && response.StatusCode < 300 {
			if out != nil && len(bytes.TrimSpace(responseBody)) != 0 {
				if err := json.Unmarshal(responseBody, out); err != nil {
					return nil, fmt.Errorf("decode Toggl API %s %s response: %w", method, path, err)
				}
			}
			return response.Header.Clone(), nil
		}

		apiErr := &APIError{
			StatusCode: response.StatusCode,
			Method:     method,
			Path:       path,
			Body:       c.safeBody(responseBody),
		}
		if !retryableStatus(response.StatusCode) || attempt+1 == c.maxAttempts {
			return nil, apiErr
		}

		delay := retryAfter(response.Header, c.now())
		if delay == 0 {
			delay = retryDelay(attempt)
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}

	return nil, errors.New("Toggl API request exhausted retries")
}

func (c *Client) waitForRateLimit(ctx context.Context) error {
	if c.minInterval <= 0 {
		return nil
	}

	c.requestMu.Lock()
	defer c.requestMu.Unlock()

	wait := c.minInterval - c.now().Sub(c.lastRequest)
	if !c.lastRequest.IsZero() && wait > 0 {
		if err := c.sleep(ctx, wait); err != nil {
			return err
		}
	}
	c.lastRequest = c.now()
	return nil
}

func (c *Client) safeBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if c.token != "" {
		text = strings.ReplaceAll(text, c.token, "[REDACTED]")
	}
	if len(text) > 500 {
		text = text[:500] + "..."
	}
	return text
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func retryDelay(attempt int) time.Duration {
	return time.Duration(1<<attempt) * 250 * time.Millisecond
}

func retryAfter(headers http.Header, now time.Time) time.Duration {
	value := headers.Get("Retry-After")
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
