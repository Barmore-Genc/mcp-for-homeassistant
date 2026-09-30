package homeassistant

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Calendar is a calendar entity.
type Calendar struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
}

func (c *Client) ListCalendars(ctx context.Context) ([]Calendar, error) {
	var out []Calendar
	err := c.doJSON(ctx, restRequest{method: http.MethodGet, path: []string{"api", "calendars"}}, &out)
	return out, err
}

// CalendarTime is either an all-day Date ("2026-01-02") or a DateTime.
type CalendarTime struct {
	Date     string `json:"date,omitempty"`
	DateTime string `json:"dateTime,omitempty"`
}

// CalendarEvent is an event returned by CalendarEvents.
type CalendarEvent struct {
	Summary      string       `json:"summary"`
	Start        CalendarTime `json:"start"`
	End          CalendarTime `json:"end"`
	Description  *string      `json:"description"`
	Location     *string      `json:"location"`
	UID          *string      `json:"uid"`
	RecurrenceID *string      `json:"recurrence_id"`
	RRule        *string      `json:"rrule"`
}

// CalendarEvents returns the events of a calendar entity between start and end.
func (c *Client) CalendarEvents(ctx context.Context, entityID string, start, end time.Time) ([]CalendarEvent, error) {
	if err := ValidateEntityID(entityID); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(entityID, "calendar.") {
		return nil, invalidArg("%q is not a calendar entity", entityID)
	}
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil, invalidArg("start and end are required and end must not be before start")
	}
	var out []CalendarEvent
	err := c.doJSON(ctx, restRequest{
		method: http.MethodGet,
		path:   []string{"api", "calendars", entityID},
		query:  url.Values{"start": {formatTime(start)}, "end": {formatTime(end)}},
	}, &out)
	return out, err
}

// TodoItem is an item of a to-do list. Status is "needs_action" or "completed".
// Add, update and remove items with the todo.add_item, todo.update_item and
// todo.remove_item services.
type TodoItem struct {
	UID         string  `json:"uid"`
	Summary     string  `json:"summary"`
	Status      string  `json:"status"`
	Due         *string `json:"due,omitempty"`
	Description *string `json:"description,omitempty"`
	Completed   *string `json:"completed,omitempty"`
}

func (c *Client) ListTodoItems(ctx context.Context, entityID string) ([]TodoItem, error) {
	if err := ValidateEntityID(entityID); err != nil {
		return nil, err
	}
	var out struct {
		Items []TodoItem `json:"items"`
	}
	if err := c.wsCall(ctx, "todo/item/list", map[string]any{"entity_id": entityID}, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Image is a camera snapshot.
type Image struct {
	ContentType string
	Data        []byte
}

// CameraSnapshot fetches a still image from a camera entity. width and height
// (0 to skip) ask HA to scale the image where the camera supports it.
func (c *Client) CameraSnapshot(ctx context.Context, entityID string, width, height int) (*Image, error) {
	if err := ValidateEntityID(entityID); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(entityID, "camera.") {
		return nil, invalidArg("%q is not a camera entity", entityID)
	}
	if width < 0 || height < 0 || width > 8192 || height > 8192 {
		return nil, invalidArg("width and height must be between 0 and 8192")
	}
	q := url.Values{}
	if width > 0 {
		q.Set("width", strconv.Itoa(width))
	}
	if height > 0 {
		q.Set("height", strconv.Itoa(height))
	}
	resp, err := c.doRaw(ctx, restRequest{
		method:  http.MethodGet,
		path:    []string{"api", "camera_proxy", entityID},
		query:   q,
		maxBody: c.maxImage,
	})
	if err != nil {
		return nil, err
	}
	ct, _, _ := mime.ParseMediaType(resp.contentType)
	if !strings.HasPrefix(ct, "image/") {
		ct = http.DetectContentType(resp.body)
		if !strings.HasPrefix(ct, "image/") {
			return nil, &Error{Op: "GET /api/camera_proxy/" + entityID, Message: "response is not an image"}
		}
	}
	return &Image{ContentType: ct, Data: resp.body}, nil
}

// Dashboard is a storage-mode dashboard. The default dashboard is not listed;
// address it with an empty url path.
type Dashboard struct {
	ID            string  `json:"id"`
	URLPath       string  `json:"url_path"`
	Title         string  `json:"title"`
	Icon          *string `json:"icon"`
	Mode          string  `json:"mode"`
	ShowInSidebar bool    `json:"show_in_sidebar"`
	RequireAdmin  bool    `json:"require_admin"`
}

func (c *Client) ListDashboards(ctx context.Context) ([]Dashboard, error) {
	var out []Dashboard
	err := c.wsCall(ctx, "lovelace/dashboards/list", nil, &out)
	return out, err
}

func dashboardPayload(urlPath string) (map[string]any, error) {
	p := map[string]any{}
	if urlPath != "" {
		if err := validateDashboardURLPath(urlPath); err != nil {
			return nil, err
		}
		p["url_path"] = urlPath
	}
	return p, nil
}

// GetDashboardConfig returns a dashboard's config ("" for the default
// dashboard). A dashboard that was never saved (auto-generated) returns an
// error with code "config_not_found".
func (c *Client) GetDashboardConfig(ctx context.Context, urlPath string) (json.RawMessage, error) {
	p, err := dashboardPayload(urlPath)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	err = c.wsCall(ctx, "lovelace/config", p, &out)
	return out, err
}

// SaveDashboardConfig replaces a storage-mode dashboard's config (admin only).
// The config is sent as given, so HA stores its keys in the caller's order.
func (c *Client) SaveDashboardConfig(ctx context.Context, urlPath string, config json.RawMessage) error {
	if !isJSONObject(config) {
		return invalidArg("config must be a JSON object")
	}
	p, err := dashboardPayload(urlPath)
	if err != nil {
		return err
	}
	p["config"] = config
	return c.wsCall(ctx, "lovelace/config/save", p, nil)
}
