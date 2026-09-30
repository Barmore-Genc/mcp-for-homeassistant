package homeassistant

import (
	"context"
	"encoding/json"
	"time"
)

// TraceDomain is a domain that records traces.
type TraceDomain string

const (
	TraceAutomation TraceDomain = "automation"
	TraceScript     TraceDomain = "script"
)

func (d TraceDomain) validate() error {
	if d != TraceAutomation && d != TraceScript {
		return invalidArg("trace domain must be automation or script, got %q", d)
	}
	return nil
}

// TraceSummary is one entry of ListTraces.
type TraceSummary struct {
	Domain          string  `json:"domain"`
	ItemID          string  `json:"item_id"`
	RunID           string  `json:"run_id"`
	State           string  `json:"state"`
	ScriptExecution *string `json:"script_execution"`
	LastStep        *string `json:"last_step"`
	Timestamp       struct {
		Start  time.Time  `json:"start"`
		Finish *time.Time `json:"finish"`
	} `json:"timestamp"`
	// Trigger describes what started an automation run.
	Trigger      *string `json:"trigger,omitempty"`
	Error        *string `json:"error,omitempty"`
	NotTriggered bool    `json:"not_triggered,omitempty"`
}

// ListTraces lists stored traces for a domain, optionally only for one item.
// itemID is the automation config id or the script key (object id).
func (c *Client) ListTraces(ctx context.Context, domain TraceDomain, itemID string) ([]TraceSummary, error) {
	if err := domain.validate(); err != nil {
		return nil, err
	}
	payload := map[string]any{"domain": domain}
	if itemID != "" {
		payload["item_id"] = itemID
	}
	var out []TraceSummary
	err := c.wsCall(ctx, "trace/list", payload, &out)
	return out, err
}

// GetTrace returns a full trace: the summary fields plus "trace" (per-step
// details), "config", "blueprint_inputs" and "context".
func (c *Client) GetTrace(ctx context.Context, domain TraceDomain, itemID, runID string) (json.RawMessage, error) {
	if err := domain.validate(); err != nil {
		return nil, err
	}
	if itemID == "" || runID == "" {
		return nil, invalidArg("item id and run id are required")
	}
	var out json.RawMessage
	err := c.wsCall(ctx, "trace/get", map[string]any{"domain": domain, "item_id": itemID, "run_id": runID}, &out)
	return out, err
}

// TraceContext locates the trace recorded for a context id.
type TraceContext struct {
	RunID  string `json:"run_id"`
	Domain string `json:"domain"`
	ItemID string `json:"item_id"`
}

// TraceContexts maps context ids to the traces they produced, which links a
// logbook or state change back to the automation or script run behind it.
// With an empty domain all stored traces are included.
func (c *Client) TraceContexts(ctx context.Context, domain TraceDomain, itemID string) (map[string]TraceContext, error) {
	payload := map[string]any{}
	if domain != "" || itemID != "" {
		if err := domain.validate(); err != nil {
			return nil, err
		}
		if itemID == "" {
			return nil, invalidArg("item id is required together with domain")
		}
		payload["domain"] = domain
		payload["item_id"] = itemID
	}
	var out map[string]TraceContext
	err := c.wsCall(ctx, "trace/contexts", payload, &out)
	return out, err
}

// Blueprint is one entry of ListBlueprints. Error is set instead of Metadata
// when the file failed to load.
type Blueprint struct {
	Metadata map[string]any `json:"metadata,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// ListBlueprints returns the blueprints of a domain keyed by relative path.
func (c *Client) ListBlueprints(ctx context.Context, domain string) (map[string]Blueprint, error) {
	if err := validateBlueprintDomain(domain); err != nil {
		return nil, err
	}
	var out map[string]Blueprint
	err := c.wsCall(ctx, "blueprint/list", map[string]any{"domain": domain}, &out)
	return out, err
}

// ImportedBlueprint is the result of ImportBlueprint. Nothing is saved until
// SaveBlueprint is called with RawData.
type ImportedBlueprint struct {
	SuggestedFilename string `json:"suggested_filename"`
	RawData           string `json:"raw_data"`
	Blueprint         struct {
		Metadata map[string]any `json:"metadata"`
	} `json:"blueprint"`
	ValidationErrors []string `json:"validation_errors"`
	Exists           bool     `json:"exists"`
}

// ImportBlueprint makes HA download a blueprint from a public https URL
// (GitHub, gist, community forum or a direct YAML link) without saving it.
func (c *Client) ImportBlueprint(ctx context.Context, sourceURL string) (*ImportedBlueprint, error) {
	if err := validateBlueprintURL(sourceURL); err != nil {
		return nil, err
	}
	var out ImportedBlueprint
	if err := c.wsCall(ctx, "blueprint/import", map[string]any{"url": sourceURL}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SaveBlueprintRequest saves blueprint YAML under the domain's blueprint folder.
type SaveBlueprintRequest struct {
	Domain string
	// Path is relative to the domain folder; ".yaml" is appended if missing.
	Path          string
	YAML          string
	SourceURL     string
	AllowOverride bool
}

// SaveBlueprint writes a blueprint file. It returns whether an existing file
// was replaced.
func (c *Client) SaveBlueprint(ctx context.Context, req SaveBlueprintRequest) (bool, error) {
	if err := validateBlueprintDomain(req.Domain); err != nil {
		return false, err
	}
	if err := validateBlueprintPath(req.Path); err != nil {
		return false, err
	}
	if req.YAML == "" {
		return false, invalidArg("yaml is required")
	}
	payload := map[string]any{"domain": req.Domain, "path": req.Path, "yaml": req.YAML}
	if req.SourceURL != "" {
		if err := validateBlueprintURL(req.SourceURL); err != nil {
			return false, err
		}
		payload["source_url"] = req.SourceURL
	}
	if req.AllowOverride {
		payload["allow_override"] = true
	}
	var out struct {
		OverridesExisting bool `json:"overrides_existing"`
	}
	err := c.wsCall(ctx, "blueprint/save", payload, &out)
	return out.OverridesExisting, err
}

// DeleteBlueprint deletes a blueprint file. HA refuses while it is in use.
func (c *Client) DeleteBlueprint(ctx context.Context, domain, path string) error {
	if err := validateBlueprintDomain(domain); err != nil {
		return err
	}
	if err := validateBlueprintPath(path); err != nil {
		return err
	}
	return c.wsCall(ctx, "blueprint/delete", map[string]any{"domain": domain, "path": path}, nil)
}
