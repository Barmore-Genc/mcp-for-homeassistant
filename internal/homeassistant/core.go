package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	checkConfigTimeout  = 2 * time.Minute
	serviceCallTimeout  = 2 * time.Minute
	errorLogTimeout     = time.Minute
	defaultErrorLogTail = 64 << 10
	maxErrorLogTail     = 4 << 20
)

// State is an entity state.
type State struct {
	EntityID     string         `json:"entity_id"`
	State        string         `json:"state"`
	Attributes   map[string]any `json:"attributes"`
	LastChanged  time.Time      `json:"last_changed"`
	LastReported time.Time      `json:"last_reported"`
	LastUpdated  time.Time      `json:"last_updated"`
	Context      Context        `json:"context"`
}

// Domain returns the entity id's domain.
func (s State) Domain() string {
	d, _, _ := strings.Cut(s.EntityID, ".")
	return d
}

// ListStates returns all entity states.
func (c *Client) ListStates(ctx context.Context) ([]State, error) {
	var out []State
	err := c.doJSON(ctx, restRequest{method: http.MethodGet, path: []string{"api", "states"}}, &out)
	return out, err
}

// GetState returns one entity state. IsNotFound(err) is true for unknown entities.
func (c *Client) GetState(ctx context.Context, entityID string) (*State, error) {
	if err := ValidateEntityID(entityID); err != nil {
		return nil, err
	}
	var out State
	err := c.doJSON(ctx, restRequest{method: http.MethodGet, path: []string{"api", "states", entityID}}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ServiceDomain lists the services of one integration domain.
type ServiceDomain struct {
	Domain   string                        `json:"domain"`
	Services map[string]ServiceDescription `json:"services"`
}

// ServiceDescription describes a service's fields, target and response support.
type ServiceDescription struct {
	Name        string                     `json:"name,omitempty"`
	Description string                     `json:"description,omitempty"`
	Fields      map[string]json.RawMessage `json:"fields"`
	Target      json.RawMessage            `json:"target,omitempty"`
	Response    *ServiceResponseSupport    `json:"response,omitempty"`
}

// ServiceResponseSupport is present when a service can return data. Optional
// false means the service must be called with ReturnResponse.
type ServiceResponseSupport struct {
	Optional bool `json:"optional"`
}

// ListServices returns all services grouped by domain.
func (c *Client) ListServices(ctx context.Context) ([]ServiceDomain, error) {
	var out []ServiceDomain
	err := c.doJSON(ctx, restRequest{method: http.MethodGet, path: []string{"api", "services"}}, &out)
	return out, err
}

// Target selects the entities a service acts on.
type Target struct {
	EntityID []string `json:"entity_id,omitempty"`
	DeviceID []string `json:"device_id,omitempty"`
	AreaID   []string `json:"area_id,omitempty"`
	FloorID  []string `json:"floor_id,omitempty"`
	LabelID  []string `json:"label_id,omitempty"`
}

func (t *Target) isEmpty() bool {
	return t == nil || len(t.EntityID)+len(t.DeviceID)+len(t.AreaID)+len(t.FloorID)+len(t.LabelID) == 0
}

// ServiceCall is a service invocation.
type ServiceCall struct {
	Domain  string
	Service string
	Data    map[string]any
	Target  *Target
	// ReturnResponse requests the service response. Required for services that
	// only return data (for example todo.get_items); rejected by services
	// that return nothing.
	ReturnResponse bool
	// Timeout overrides the default service call timeout (2 minutes). HA keeps
	// running the service if the client gives up.
	Timeout time.Duration
}

// ServiceResult is the outcome of CallService.
type ServiceResult struct {
	// ChangedStates are the states changed by the call's context while it ran.
	ChangedStates []State `json:"changed_states"`
	// Response is the service response when ReturnResponse was set.
	Response json.RawMessage `json:"service_response,omitempty"`
}

// CallService calls a service and waits for it to finish.
func (c *Client) CallService(ctx context.Context, call ServiceCall) (*ServiceResult, error) {
	if err := validateSlug("domain", call.Domain); err != nil {
		return nil, err
	}
	if err := validateSlug("service", call.Service); err != nil {
		return nil, err
	}
	data := make(map[string]any, len(call.Data)+5)
	for k, v := range call.Data {
		data[k] = v
	}
	if !call.Target.isEmpty() {
		for key, ids := range map[string][]string{
			"entity_id": call.Target.EntityID, "device_id": call.Target.DeviceID,
			"area_id": call.Target.AreaID, "floor_id": call.Target.FloorID, "label_id": call.Target.LabelID,
		} {
			if len(ids) == 0 {
				continue
			}
			if _, dup := data[key]; dup {
				return nil, invalidArg("%s is set in both data and target", key)
			}
			data[key] = ids
		}
	}
	req := restRequest{
		method:  http.MethodPost,
		path:    []string{"api", "services", call.Domain, call.Service},
		body:    data,
		timeout: call.Timeout,
	}
	if req.timeout <= 0 {
		req.timeout = serviceCallTimeout
	}
	if call.ReturnResponse {
		req.query = url.Values{"return_response": {""}}
		var out ServiceResult
		if err := c.doJSON(ctx, req, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	var changed []State
	if err := c.doJSON(ctx, req, &changed); err != nil {
		return nil, err
	}
	return &ServiceResult{ChangedStates: changed}, nil
}

// FireEvent fires an event on the bus (admin only).
func (c *Client) FireEvent(ctx context.Context, eventType string, data map[string]any) (*Context, error) {
	if err := validateEventType(eventType); err != nil {
		return nil, err
	}
	payload := map[string]any{"event_type": eventType}
	if data != nil {
		payload["event_data"] = data
	}
	var out struct {
		Context Context `json:"context"`
	}
	if err := c.wsCall(ctx, "fire_event", payload, &out); err != nil {
		return nil, err
	}
	return &out.Context, nil
}

// RenderTemplate renders a Jinja template (admin only).
func (c *Client) RenderTemplate(ctx context.Context, template string, variables map[string]any) (string, error) {
	body := map[string]any{"template": template}
	if variables != nil {
		body["variables"] = variables
	}
	resp, err := c.doRaw(ctx, restRequest{method: http.MethodPost, path: []string{"api", "template"}, body: body})
	if err != nil {
		return "", err
	}
	return string(resp.body), nil
}

// Config is the core configuration returned by /api/config.
type Config struct {
	Components            []string          `json:"components"`
	ConfigDir             string            `json:"config_dir"`
	ConfigSource          string            `json:"config_source"`
	Country               *string           `json:"country"`
	Currency              string            `json:"currency"`
	Debug                 bool              `json:"debug"`
	Elevation             float64           `json:"elevation"`
	ExternalURL           *string           `json:"external_url"`
	InternalURL           *string           `json:"internal_url"`
	Language              string            `json:"language"`
	Latitude              float64           `json:"latitude"`
	Longitude             float64           `json:"longitude"`
	LocationName          string            `json:"location_name"`
	Radius                float64           `json:"radius"`
	RecoveryMode          bool              `json:"recovery_mode"`
	SafeMode              bool              `json:"safe_mode"`
	State                 string            `json:"state"`
	TimeZone              string            `json:"time_zone"`
	UnitSystem            map[string]string `json:"unit_system"`
	Version               string            `json:"version"`
	AllowlistExternalDirs []string          `json:"allowlist_external_dirs"`
	AllowlistExternalURLs []string          `json:"allowlist_external_urls"`
}

// GetConfig returns the core configuration.
func (c *Client) GetConfig(ctx context.Context) (*Config, error) {
	var out Config
	if err := c.doJSON(ctx, restRequest{method: http.MethodGet, path: []string{"api", "config"}}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ConfigCheckResult is the result of CheckConfig.
type ConfigCheckResult struct {
	// Result is "valid" or "invalid".
	Result   string  `json:"result"`
	Errors   *string `json:"errors"`
	Warnings *string `json:"warnings"`
}

// CheckConfig validates the YAML configuration on disk (admin only).
func (c *Client) CheckConfig(ctx context.Context) (*ConfigCheckResult, error) {
	var out ConfigCheckResult
	err := c.doJSON(ctx, restRequest{
		method:  http.MethodPost,
		path:    []string{"api", "config", "core", "check_config"},
		timeout: checkConfigTimeout,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Restart restarts Home Assistant. HA refuses when the configuration is
// invalid. The WebSocket connection drops and reconnects on the next call.
func (c *Client) Restart(ctx context.Context) error {
	_, err := c.CallService(ctx, ServiceCall{Domain: "homeassistant", Service: "restart"})
	return err
}

// ErrorLog is the tail of home-assistant.log.
type ErrorLog struct {
	Text string `json:"text"`
	// Truncated is true when older log lines were left out.
	Truncated bool `json:"truncated"`
}

// GetErrorLog returns up to maxBytes (default 64 KiB, max 4 MiB) from the end
// of the log file (admin only).
func (c *Client) GetErrorLog(ctx context.Context, maxBytes int) (*ErrorLog, error) {
	if maxBytes <= 0 {
		maxBytes = defaultErrorLogTail
	}
	if maxBytes > maxErrorLogTail {
		return nil, invalidArg("maxBytes must be at most %d", maxErrorLogTail)
	}
	u, err := c.endpoint("api", "error_log")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, errorLogTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Range", fmt.Sprintf("bytes=-%d", maxBytes))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("homeassistant: GET /api/error_log: %w", scrubURLError(err))
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent, http.StatusOK:
	case http.StatusRequestedRangeNotSatisfiable:
		return &ErrorLog{}, nil
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, restError("GET /api/error_log", resp.StatusCode, b)
	}

	// A server that ignores Range sends the whole file; keep only its tail.
	tail, truncated, err := readTail(resp.Body, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("homeassistant: GET /api/error_log: %w", err)
	}
	if resp.StatusCode == http.StatusPartialContent {
		truncated = !strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes 0-")
	}
	if truncated {
		if i := bytes.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		}
	}
	return &ErrorLog{Text: strings.ToValidUTF8(string(tail), "�"), Truncated: truncated}, nil
}

func readTail(r io.Reader, n int) ([]byte, bool, error) {
	buf := make([]byte, 0, n)
	chunk := make([]byte, 32<<10)
	truncated := false
	for {
		k, err := r.Read(chunk)
		if k > 0 {
			buf = append(buf, chunk[:k]...)
			if len(buf) > n {
				buf = append(buf[:0], buf[len(buf)-n:]...)
				truncated = true
			}
		}
		if err == io.EOF {
			return buf, truncated, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
}
