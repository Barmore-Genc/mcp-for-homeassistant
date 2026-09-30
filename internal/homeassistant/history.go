package homeassistant

import (
	"context"
	"encoding/json"
	"math"
	"time"
)

const (
	historyTimeout = 2 * time.Minute
	maxHistoryIDs  = 100
)

func unixFloat(sec float64) time.Time {
	s, frac := math.Modf(sec)
	return time.Unix(int64(s), int64(math.Round(frac*1e6))*1e3).UTC()
}

func unixMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// HistoryRequest selects state history. History and logbook use the
// WebSocket API rather than REST: time bounds and ids travel in the JSON body
// instead of the URL path, history comes back in HA's compact format, and the
// logbook can filter by device and context.
type HistoryRequest struct {
	EntityIDs []string
	Start     time.Time
	// End defaults to now.
	End time.Time
	// AllChanges includes attribute-only changes; by default only
	// significant state changes are returned.
	AllChanges bool
	// MinimalResponse drops attributes from all but the first entry per entity.
	MinimalResponse bool
	NoAttributes    bool
	// SkipInitialState omits the state each entity had at Start.
	SkipInitialState bool
}

// HistoryEntry is one recorded state.
type HistoryEntry struct {
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes,omitempty"`
	LastChanged time.Time      `json:"last_changed"`
	LastUpdated time.Time      `json:"last_updated"`
}

// History returns state history keyed by entity id.
func (c *Client) History(ctx context.Context, req HistoryRequest) (map[string][]HistoryEntry, error) {
	if len(req.EntityIDs) == 0 || len(req.EntityIDs) > maxHistoryIDs {
		return nil, invalidArg("between 1 and %d entity ids are required", maxHistoryIDs)
	}
	for _, id := range req.EntityIDs {
		if err := ValidateEntityID(id); err != nil {
			return nil, err
		}
	}
	if req.Start.IsZero() {
		return nil, invalidArg("start time is required")
	}
	payload := map[string]any{
		"entity_ids":               req.EntityIDs,
		"start_time":               formatTime(req.Start),
		"significant_changes_only": !req.AllChanges,
		"minimal_response":         req.MinimalResponse,
		"no_attributes":            req.NoAttributes,
		"include_start_time_state": !req.SkipInitialState,
	}
	if !req.End.IsZero() {
		payload["end_time"] = formatTime(req.End)
	}
	var raw map[string][]struct {
		State       string         `json:"s"`
		Attributes  map[string]any `json:"a"`
		LastUpdated float64        `json:"lu"`
		LastChanged float64        `json:"lc"`
	}
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	if err := c.wsCall(ctx, "history/history_during_period", payload, &raw); err != nil {
		return nil, err
	}
	out := make(map[string][]HistoryEntry, len(raw))
	for id, rows := range raw {
		entries := make([]HistoryEntry, len(rows))
		for i, r := range rows {
			lc := r.LastChanged
			if lc == 0 {
				lc = r.LastUpdated
			}
			entries[i] = HistoryEntry{
				State:       r.State,
				Attributes:  r.Attributes,
				LastChanged: unixFloat(lc),
				LastUpdated: unixFloat(r.LastUpdated),
			}
		}
		out[id] = entries
	}
	return out, nil
}

// LogbookRequest selects logbook entries. With no entity or device ids all
// entries in the period are returned.
type LogbookRequest struct {
	Start time.Time
	// End defaults to now.
	End       time.Time
	EntityIDs []string
	DeviceIDs []string
	ContextID string
}

// LogbookEntry is one logbook line. Context fields describe what caused it.
type LogbookEntry struct {
	When                time.Time `json:"when"`
	EntityID            string    `json:"entity_id,omitempty"`
	State               string    `json:"state,omitempty"`
	Name                string    `json:"name,omitempty"`
	Message             string    `json:"message,omitempty"`
	Domain              string    `json:"domain,omitempty"`
	Icon                string    `json:"icon,omitempty"`
	Source              string    `json:"source,omitempty"`
	ContextID           string    `json:"context_id,omitempty"`
	ContextUserID       string    `json:"context_user_id,omitempty"`
	ContextEventType    string    `json:"context_event_type,omitempty"`
	ContextDomain       string    `json:"context_domain,omitempty"`
	ContextService      string    `json:"context_service,omitempty"`
	ContextEntityID     string    `json:"context_entity_id,omitempty"`
	ContextEntityIDName string    `json:"context_entity_id_name,omitempty"`
	ContextName         string    `json:"context_name,omitempty"`
	ContextMessage      string    `json:"context_message,omitempty"`
	ContextState        string    `json:"context_state,omitempty"`
	ContextSource       string    `json:"context_source,omitempty"`
}

func (e *LogbookEntry) UnmarshalJSON(b []byte) error {
	type plain LogbookEntry
	var aux struct {
		plain
		When float64 `json:"when"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*e = LogbookEntry(aux.plain)
	e.When = unixFloat(aux.When)
	return nil
}

// Logbook returns logbook entries for a period.
func (c *Client) Logbook(ctx context.Context, req LogbookRequest) ([]LogbookEntry, error) {
	if req.Start.IsZero() {
		return nil, invalidArg("start time is required")
	}
	payload := map[string]any{"start_time": formatTime(req.Start)}
	if !req.End.IsZero() {
		payload["end_time"] = formatTime(req.End)
	}
	if len(req.EntityIDs) > 0 {
		for _, id := range req.EntityIDs {
			if err := ValidateEntityID(id); err != nil {
				return nil, err
			}
		}
		payload["entity_ids"] = req.EntityIDs
	}
	if len(req.DeviceIDs) > 0 {
		payload["device_ids"] = req.DeviceIDs
	}
	if req.ContextID != "" {
		payload["context_id"] = req.ContextID
	}
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	var out []LogbookEntry
	err := c.wsCall(ctx, "logbook/get_events", payload, &out)
	return out, err
}

// StatisticMetadata describes a long-term statistic.
type StatisticMetadata struct {
	StatisticID                 string  `json:"statistic_id"`
	Name                        *string `json:"name"`
	Source                      string  `json:"source"`
	DisplayUnitOfMeasurement    *string `json:"display_unit_of_measurement"`
	StatisticsUnitOfMeasurement *string `json:"statistics_unit_of_measurement"`
	UnitClass                   *string `json:"unit_class"`
	HasSum                      bool    `json:"has_sum"`
	// MeanType is 0 (none), 1 (arithmetic) or 2 (circular).
	MeanType int `json:"mean_type"`
}

// ListStatisticIDs lists statistics. statisticType may be "", "mean" or "sum".
func (c *Client) ListStatisticIDs(ctx context.Context, statisticType string) ([]StatisticMetadata, error) {
	payload := map[string]any{}
	switch statisticType {
	case "":
	case "mean", "sum":
		payload["statistic_type"] = statisticType
	default:
		return nil, invalidArg("statistic type must be mean or sum")
	}
	var out []StatisticMetadata
	err := c.wsCall(ctx, "recorder/list_statistic_ids", payload, &out)
	return out, err
}

// StatisticsRequest selects aggregated statistics.
type StatisticsRequest struct {
	StatisticIDs []string
	Start        time.Time
	End          time.Time
	// Period is one of 5minute, hour, day, week, month, year.
	Period string
	// Types limits the returned values (change, last_reset, max, mean, min,
	// state, sum). Empty means all.
	Types []string
	// Units converts values, keyed by unit class (for example {"energy": "kWh"}).
	Units map[string]string
}

// StatisticValue is one aggregation period. Fields not requested or not
// applicable are nil.
type StatisticValue struct {
	Start     time.Time  `json:"start"`
	End       time.Time  `json:"end"`
	Mean      *float64   `json:"mean,omitempty"`
	Min       *float64   `json:"min,omitempty"`
	Max       *float64   `json:"max,omitempty"`
	State     *float64   `json:"state,omitempty"`
	Sum       *float64   `json:"sum,omitempty"`
	Change    *float64   `json:"change,omitempty"`
	LastReset *time.Time `json:"last_reset,omitempty"`
}

func (v *StatisticValue) UnmarshalJSON(b []byte) error {
	var aux struct {
		Start     int64    `json:"start"`
		End       int64    `json:"end"`
		Mean      *float64 `json:"mean"`
		Min       *float64 `json:"min"`
		Max       *float64 `json:"max"`
		State     *float64 `json:"state"`
		Sum       *float64 `json:"sum"`
		Change    *float64 `json:"change"`
		LastReset *int64   `json:"last_reset"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*v = StatisticValue{
		Start: unixMillis(aux.Start), End: unixMillis(aux.End),
		Mean: aux.Mean, Min: aux.Min, Max: aux.Max, State: aux.State, Sum: aux.Sum, Change: aux.Change,
	}
	if aux.LastReset != nil {
		t := unixMillis(*aux.LastReset)
		v.LastReset = &t
	}
	return nil
}

var statisticPeriods = map[string]bool{"5minute": true, "hour": true, "day": true, "week": true, "month": true, "year": true}

// StatisticsDuringPeriod returns statistics keyed by statistic id.
func (c *Client) StatisticsDuringPeriod(ctx context.Context, req StatisticsRequest) (map[string][]StatisticValue, error) {
	if len(req.StatisticIDs) == 0 || len(req.StatisticIDs) > maxHistoryIDs {
		return nil, invalidArg("between 1 and %d statistic ids are required", maxHistoryIDs)
	}
	if req.Start.IsZero() {
		return nil, invalidArg("start time is required")
	}
	if !statisticPeriods[req.Period] {
		return nil, invalidArg("period must be one of 5minute, hour, day, week, month, year")
	}
	payload := map[string]any{
		"statistic_ids": req.StatisticIDs,
		"start_time":    formatTime(req.Start),
		"period":        req.Period,
	}
	if !req.End.IsZero() {
		payload["end_time"] = formatTime(req.End)
	}
	if len(req.Types) > 0 {
		payload["types"] = req.Types
	}
	if len(req.Units) > 0 {
		payload["units"] = req.Units
	}
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	var out map[string][]StatisticValue
	err := c.wsCall(ctx, "recorder/statistics_during_period", payload, &out)
	return out, err
}
